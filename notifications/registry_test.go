package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/config"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/loggermdc"
)

type stubChannel struct {
	key       application.NotificationChannelKey
	err       error
	prompts   int
	messages  int
	lastInput application.SendApprovalPromptInput
}

func (c *stubChannel) Key() application.NotificationChannelKey { return c.key }

func (c *stubChannel) SendApprovalPrompt(_ context.Context, input application.SendApprovalPromptInput) error {
	c.prompts++
	c.lastInput = input
	return c.err
}

func (c *stubChannel) SendTransactional(context.Context, application.SendTransactionalInput) error {
	c.messages++
	return c.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
}

// One broken transport must not silence the others, and the combined
// failure still reaches the caller's retry policy.
func TestRegistryDeliversToEveryChannelDespiteFailures(t *testing.T) {
	failure := errors.New("relay refused")
	broken := &stubChannel{key: "broken", err: failure}
	working := &stubChannel{key: "working"}
	registry := NewRegistry(discardLogger(), broken, working)

	err := registry.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request:    approvalRequestFixture(),
		Recipients: []application.NotificationRecipient{{MembershipID: "m-1", Emails: []string{"ana@example.com"}}},
	})
	// The failure is logged, not returned: returning it would make the
	// caller retry and re-send through the channel that worked.
	if err != nil {
		t.Fatalf("a channel failure must not fail the prompt: %v", err)
	}
	if broken.prompts != 1 || working.prompts != 1 {
		t.Fatalf("channels called %d and %d times, want 1 each", broken.prompts, working.prompts)
	}
}

func TestRegistryRejectsInvalidInputBeforeDispatch(t *testing.T) {
	channel := &stubChannel{key: "email"}
	registry := NewRegistry(discardLogger(), channel)
	if err := registry.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{}); err == nil {
		t.Fatal("expected validation to reject an empty prompt")
	}
	if err := registry.SendTransactional(context.Background(), application.SendTransactionalInput{}); err == nil {
		t.Fatal("expected validation to reject an empty message")
	}
	if channel.prompts != 0 || channel.messages != 0 {
		t.Fatalf("invalid input reached the channel: %d prompts, %d messages", channel.prompts, channel.messages)
	}
}

// Without SMTP configured the deployment still runs, on the logging
// channel alone; configuring it adds email rather than replacing the log.
func TestBuildRegistersConfiguredChannels(t *testing.T) {
	base := config.Config{PublicOrigin: "https://approvals.example.com"}
	registry, err := Build(base, discardLogger())
	if err != nil {
		t.Fatalf("Build without SMTP: %v", err)
	}
	if keys := registry.Keys(); len(keys) != 1 || keys[0] != application.NotificationChannelLog {
		t.Fatalf("channels without SMTP = %v", keys)
	}

	withEmail := base
	withEmail.SMTPHost, withEmail.SMTPPort, withEmail.SMTPFrom = "mail.example.com", 587, "approvals@example.com"
	registry, err = Build(withEmail, discardLogger())
	if err != nil {
		t.Fatalf("Build with SMTP: %v", err)
	}
	if keys := registry.Keys(); len(keys) != 2 || keys[1] != application.NotificationChannelEmail {
		t.Fatalf("channels with SMTP = %v", keys)
	}

	extra := &stubChannel{key: "push"}
	registry, err = Build(base, discardLogger(), extra)
	if err != nil {
		t.Fatalf("Build with an edition channel: %v", err)
	}
	if keys := registry.Keys(); len(keys) != 2 || keys[1] != "push" {
		t.Fatalf("edition channel was not registered: %v", keys)
	}
}

// A configured but unusable transport fails at startup: dropping every
// notification silently is worse than refusing to start.
func TestBuildRejectsBrokenSMTPConfiguration(t *testing.T) {
	broken := config.Config{PublicOrigin: "https://approvals.example.com", SMTPHost: "mail.example.com", SMTPPort: 587, SMTPFrom: "not an address"}
	if _, err := Build(broken, discardLogger()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("got %v, want an invalid-configuration error", err)
	}
}

// The log channel is the open edition's floor, and it must never write the
// invite URL: the token inside it is a credential.
func TestLogChannelOmitsTheInviteURL(t *testing.T) {
	buffer := bytes.NewBuffer(nil)
	logger := slog.New(loggermdc.NewHandler(slog.NewJSONHandler(buffer, nil)))
	channel := NewLogChannel(logger)
	ctx := loggermdc.With(context.Background(), loggermdc.Fields{loggermdc.KeyTraceID: "trace-1"})

	if err := channel.SendTransactional(ctx, application.SendTransactionalInput{
		Message: application.TransactionalMessage{
			Kind: application.TransactionalInvitation, OrganizationID: "org-1", OrganizationName: "Acme",
			InviteURL: "https://approvals.example.com/invite/whi_secret", ExpiresAt: time.Unix(1_700_600_000, 0).UTC(),
		},
		Recipient: application.NotificationRecipient{Emails: []string{"invitee@example.com"}},
		Address:   "invitee@example.com",
	}); err != nil {
		t.Fatalf("SendTransactional: %v", err)
	}
	if strings.Contains(buffer.String(), "whi_secret") {
		t.Fatalf("log channel wrote the invitation token: %s", buffer.String())
	}

	buffer.Reset()
	if err := channel.SendApprovalPrompt(ctx, application.SendApprovalPromptInput{
		Request:    approvalRequestFixture(),
		Recipients: []application.NotificationRecipient{{MembershipID: "m-1", Emails: []string{"ana@example.com"}}},
	}); err != nil {
		t.Fatalf("SendApprovalPrompt: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatalf("log record is not JSON: %v", err)
	}
	if record["approval_request_id"] != "11111111-1111-4111-8111-111111111111" || record["trace_id"] != "trace-1" {
		t.Fatalf("approval log lost its correlation: %v", record)
	}
	// Recipient addresses stay out of the log; membership IDs identify
	// people without copying contact details into the operational stream.
	if strings.Contains(buffer.String(), "ana@example.com") {
		t.Fatalf("log channel wrote a recipient address: %s", buffer.String())
	}
}
