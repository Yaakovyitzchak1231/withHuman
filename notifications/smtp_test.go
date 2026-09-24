package notifications

import (
	"context"
	"errors"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type capturedMail struct {
	addr string
	from string
	to   []string
	body string
}

func testSMTPChannel(t *testing.T, err error) (*SMTPChannel, *[]capturedMail) {
	t.Helper()
	channel, buildErr := NewSMTPChannel(SMTPConfig{
		Host: "mail.example.com", Port: 587, Username: "relay", Password: "secret",
		From: "withHuman <approvals@example.com>", PublicOrigin: "https://approvals.example.com",
	})
	if buildErr != nil {
		t.Fatalf("NewSMTPChannel: %v", buildErr)
	}
	sent := make([]capturedMail, 0)
	channel.sendMail = func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
		sent = append(sent, capturedMail{addr: addr, from: from, to: to, body: string(msg)})
		return err
	}
	channel.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	return channel, &sent
}

func approvalRequestFixture() domain.ApprovalRequest {
	now := time.Unix(1_700_000_000, 0).UTC()
	return domain.ApprovalRequest{
		ID: "11111111-1111-4111-8111-111111111111", OrganizationID: "org-1",
		AgentID: "agent-1", AgentInstanceID: "instance-1",
		AgentName: "support-bot", AgentInstanceName: "prod runner", AgentSlug: "support",
		Tool:      "stripe.refund",
		Arguments: []byte(`{"customer":"cus_8Kd21","amount_cents":4400}`),
		Context:   []byte(`{"ticket":"ZD-4471"}`),
		Status:    domain.ApprovalPending, IdempotencyKey: "key-1", SemanticHash: "hash-1",
		CreatedAt: now, UpdatedAt: now, DeadlineAt: now.Add(6 * time.Hour),
	}
}

// The reviewer prompt must carry enough to triage and nothing that would
// leak a payload into a mailbox. Arguments and context routinely hold
// customer data, and mail is forwardable and long-lived.
func TestApprovalPromptOmitsRequestArguments(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	request := approvalRequestFixture()
	if err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request: request, RoutingOutcome: domain.RoutingEscalated,
		Recipients: []application.NotificationRecipient{{MembershipID: "m-1", DisplayName: "Ana Suarez", Emails: []string{"ana@example.com"}}},
	}); err != nil {
		t.Fatalf("SendApprovalPrompt: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(*sent))
	}
	message := (*sent)[0]
	for _, secret := range []string{"cus_8Kd21", "4400", "ZD-4471", "amount_cents"} {
		if strings.Contains(message.body, secret) {
			t.Fatalf("approval email leaked request payload %q:\n%s", secret, message.body)
		}
	}
	for _, expected := range []string{
		"Subject: Approval needed: stripe.refund",
		"To: ana@example.com",
		"Hello Ana Suarez,",
		"support-bot",
		"https://approvals.example.com/queue/11111111-1111-4111-8111-111111111111",
	} {
		if !strings.Contains(message.body, expected) {
			t.Fatalf("approval email missing %q:\n%s", expected, message.body)
		}
	}
	if message.addr != "mail.example.com:587" || message.from != "approvals@example.com" {
		t.Fatalf("unexpected transport envelope: %+v", message)
	}
}

// One unreachable mailbox must not stop the rest: a request waiting on a
// human is better served by partial delivery than by none.
func TestApprovalPromptContinuesAfterOneFailure(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	failure := errors.New("mailbox full")
	channel.sendMail = func(_ string, _ smtp.Auth, _ string, to []string, msg []byte) error {
		*sent = append(*sent, capturedMail{to: to, body: string(msg)})
		if to[0] == "broken@example.com" {
			return failure
		}
		return nil
	}
	err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request: approvalRequestFixture(), RoutingOutcome: domain.RoutingEscalated,
		Recipients: []application.NotificationRecipient{
			{MembershipID: "m-1", Emails: []string{"broken@example.com"}},
			{MembershipID: "m-2", Emails: []string{"ok@example.com"}},
			{MembershipID: "m-3", Emails: []string{"not-an-address"}},
		},
	})
	if !errors.Is(err, failure) {
		t.Fatalf("failure was not reported: %v", err)
	}
	if len(*sent) != 2 {
		t.Fatalf("sent %d messages, want 2 (the invalid address is skipped)", len(*sent))
	}
	if (*sent)[1].to[0] != "ok@example.com" {
		t.Fatalf("delivery stopped at the first failure: %+v", *sent)
	}
}

func TestInvitationCarriesTheInviteLink(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	expires := time.Unix(1_700_600_000, 0).UTC()
	if err := channel.SendTransactional(context.Background(), application.SendTransactionalInput{
		Message: application.TransactionalMessage{
			Kind: application.TransactionalInvitation, OrganizationID: "org-1", OrganizationName: "Acme",
			InviteURL: "https://approvals.example.com/invite/whi_secret", ExpiresAt: expires,
		},
		Recipient: application.NotificationRecipient{Emails: []string{"invitee@example.com"}},
		Address:   "invitee@example.com",
	}); err != nil {
		t.Fatalf("SendTransactional: %v", err)
	}
	message := (*sent)[0]
	if !strings.Contains(message.body, "https://approvals.example.com/invite/whi_secret") {
		t.Fatalf("invitation email omitted the link:\n%s", message.body)
	}
	if !strings.Contains(message.body, "Subject: You are invited to Acme on withHuman") {
		t.Fatalf("unexpected invitation subject:\n%s", message.body)
	}
}

// The invite URL is a credential, so a transport failure must not echo the
// message body into logs or error strings.
func TestSendErrorNeverEchoesTheMessageBody(t *testing.T) {
	channel, _ := testSMTPChannel(t, errors.New("relay refused"))
	err := channel.SendTransactional(context.Background(), application.SendTransactionalInput{
		Message: application.TransactionalMessage{
			Kind: application.TransactionalInvitation, OrganizationID: "org-1", OrganizationName: "Acme",
			InviteURL: "https://approvals.example.com/invite/whi_secret", ExpiresAt: time.Unix(1_700_600_000, 0).UTC(),
		},
		Recipient: application.NotificationRecipient{Emails: []string{"invitee@example.com"}},
		Address:   "invitee@example.com",
	})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "whi_secret") {
		t.Fatalf("error leaked the invitation token: %v", err)
	}
}

func TestNewSMTPChannelRejectsUnusableConfiguration(t *testing.T) {
	valid := SMTPConfig{Host: "mail.example.com", Port: 587, From: "a@example.com", PublicOrigin: "https://example.com"}
	for _, test := range []struct {
		name   string
		mutate func(*SMTPConfig)
	}{
		{"no host", func(c *SMTPConfig) { c.Host = "" }},
		{"port out of range", func(c *SMTPConfig) { c.Port = 70000 }},
		{"missing from", func(c *SMTPConfig) { c.From = "" }},
		{"from is not an address", func(c *SMTPConfig) { c.From = "not an address" }},
		{"no public origin", func(c *SMTPConfig) { c.PublicOrigin = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := NewSMTPChannel(config); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("got %v, want an invalid-configuration error", err)
			}
		})
	}
}

// The configured sender carries a display name for the From header, but
// the SMTP envelope takes a bare address: relays answer "501 Bad sender
// address syntax" to a MAIL FROM with a name in it.
func TestEnvelopeSenderIsBareAddress(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	if err := channel.SendTransactional(context.Background(), application.SendTransactionalInput{
		Message: application.TransactionalMessage{Kind: application.TransactionalContactVerification, VerificationCode: "123456", ExpiresAt: time.Unix(1_700_000_900, 0).UTC()},
		Address: "reviewer@example.com",
	}); err != nil {
		t.Fatalf("SendTransactional: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("expected one message, got %d", len(*sent))
	}
	if got := (*sent)[0].from; got != "approvals@example.com" {
		t.Fatalf("envelope from = %q, want bare address", got)
	}
	if !strings.Contains((*sent)[0].body, "From: withHuman <approvals@example.com>\r\n") {
		t.Fatalf("From header lost the display name:\n%s", (*sent)[0].body)
	}
}

// The signup link is the founder's vouch, so the message must carry it
// and name the organization being created.
func TestSignupVerificationCarriesTheLink(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	expires := time.Unix(1_700_600_000, 0).UTC()
	if err := channel.SendTransactional(context.Background(), application.SendTransactionalInput{
		Message: application.TransactionalMessage{
			Kind: application.TransactionalSignupVerification, OrganizationName: "Acme",
			VerifyURL: "https://approvals.example.com/signup/whs_secret", ExpiresAt: expires,
		},
		Recipient: application.NotificationRecipient{Emails: []string{"founder@example.com"}},
		Address:   "founder@example.com",
	}); err != nil {
		t.Fatalf("SendTransactional: %v", err)
	}
	message := (*sent)[0]
	if !strings.Contains(message.body, "https://approvals.example.com/signup/whs_secret") {
		t.Fatalf("signup email omitted the link:\n%s", message.body)
	}
	if !strings.Contains(message.body, "Subject: Confirm your email to create Acme on withHuman") {
		t.Fatalf("unexpected signup subject:\n%s", message.body)
	}
}
