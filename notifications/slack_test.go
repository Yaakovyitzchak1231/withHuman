package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type slackFakes struct {
	application.SlackLifecycleRepository
	fallbackClaimed bool
	disconnected    bool
	token           string
	posted          []application.SlackDirectMessageInput
	fail            map[string]error
}

type fallbackEmailRecorder struct {
	stubChannel
	addresses []string
}

func (c *fallbackEmailRecorder) SendApprovalPrompt(_ context.Context, input application.SendApprovalPromptInput) error {
	for _, recipient := range input.Recipients {
		c.addresses = append(c.addresses, recipient.Emails...)
	}
	return nil
}

func TestSlackRegistryDeliversAccountEmailFallbackOnce(t *testing.T) {
	fakes := &slackFakes{}
	email := &fallbackEmailRecorder{stubChannel: stubChannel{key: application.NotificationChannelEmail}}
	registry := NewRegistry(discardLogger(), email, NewSlackChannel(fakes, fakes, "https://approvals.example.com"))
	input := application.SendApprovalPromptInput{Request: approvalRequestFixture(), Recipients: []application.NotificationRecipient{{UserID: "user", MembershipID: "member", SlackUserID: "U1", SlackInstallationID: "old", SlackFallbackEmail: "owner@example.com"}}}
	for range 2 {
		if err := registry.SendApprovalPrompt(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	if len(email.addresses) != 1 || email.addresses[0] != "owner@example.com" || len(fakes.posted) != 0 {
		t.Fatalf("emails=%v Slack=%v", email.addresses, fakes.posted)
	}
}

func (f *slackFakes) GetSlackConnection(context.Context, application.GetSlackConnectionInput) (domain.SlackConnection, error) {
	return domain.SlackConnection{}, nil
}
func (f *slackFakes) OpenSlackBotToken(context.Context, application.GetSlackConnectionInput) (string, error) {
	if f.token == "" {
		return "", domain.ErrNotFound
	}
	return f.token, nil
}
func (f *slackFakes) UpsertSlackConnection(context.Context, application.UpsertSlackConnectionInput) (domain.SlackConnection, error) {
	return domain.SlackConnection{}, nil
}
func (f *slackFakes) DeleteSlackConnection(context.Context, application.DeleteSlackConnectionInput) (string, error) {
	return "", nil
}
func (f *slackFakes) CreateSlackInstallState(context.Context, application.CreateSlackInstallStateInput) error {
	return nil
}
func (f *slackFakes) ConsumeSlackInstallState(context.Context, application.ConsumeSlackInstallStateInput) (application.SlackInstallState, error) {
	return application.SlackInstallState{}, nil
}
func (f *slackFakes) LookupUserByEmail(context.Context, string, string) (application.SlackUser, error) {
	return application.SlackUser{}, domain.ErrNotFound
}
func (f *slackFakes) PostDirectMessage(_ context.Context, input application.SlackDirectMessageInput) error {
	f.posted = append(f.posted, input)
	return f.fail[input.SlackUserID]
}

// A prompt reaches linked reviewers only, carries the tool and a link, and
// never the arguments.
func TestSlackPromptReachesLinkedReviewersWithoutArguments(t *testing.T) {
	fakes := &slackFakes{token: "xoxb-token"}
	channel := NewSlackChannel(fakes, fakes, "https://approvals.example.com/")
	request := approvalRequestFixture()
	if err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request: request, RoutingOutcome: domain.RoutingEscalated,
		Recipients: []application.NotificationRecipient{
			{MembershipID: "m-1", DisplayName: "Ana", Emails: []string{"ana@example.com"}, SlackUserID: "U0ANA"},
			{MembershipID: "m-2", DisplayName: "Ben", Emails: []string{"ben@example.com"}},
		},
	}); err != nil {
		t.Fatalf("SendApprovalPrompt: %v", err)
	}
	if len(fakes.posted) != 1 || fakes.posted[0].SlackUserID != "U0ANA" || fakes.posted[0].BotToken != "xoxb-token" {
		t.Fatalf("posted = %+v, want one message to the linked reviewer", fakes.posted)
	}
	message := fakes.posted[0]
	body := message.Text + string(message.Blocks)
	for _, secret := range []string{"cus_8Kd21", "4400", "ZD-4471", "amount_cents"} {
		if strings.Contains(body, secret) {
			t.Fatalf("slack message leaked request payload %q: %s", secret, body)
		}
	}
	for _, expected := range []string{"stripe.refund", "support-bot", "https://approvals.example.com/queue/11111111-1111-4111-8111-111111111111"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("slack message missing %q: %s", expected, body)
		}
	}
}

// No workspace and no linked reviewers are not failures: Slack simply has
// nobody to reach. One failed message does not stop the others.
func TestSlackPromptQuietWhenNothingToDeliver(t *testing.T) {
	unconnected := &slackFakes{}
	channel := NewSlackChannel(unconnected, unconnected, "https://approvals.example.com")
	if err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request:    approvalRequestFixture(),
		Recipients: []application.NotificationRecipient{{MembershipID: "m-1", SlackUserID: "U0ANA"}},
	}); err != nil {
		t.Fatalf("no workspace = %v, want nothing to do", err)
	}
	connected := &slackFakes{token: "xoxb-token"}
	channel = NewSlackChannel(connected, connected, "https://approvals.example.com")
	if err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request:    approvalRequestFixture(),
		Recipients: []application.NotificationRecipient{{MembershipID: "m-1", Emails: []string{"a@example.com"}}},
	}); err != nil || len(connected.posted) != 0 {
		t.Fatalf("no linked reviewers = %v with %d posts, want nothing to do", err, len(connected.posted))
	}

	failing := errors.New("channel_not_found")
	partial := &slackFakes{token: "xoxb-token", fail: map[string]error{"U0BAD": failing}}
	channel = NewSlackChannel(partial, partial, "https://approvals.example.com")
	err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request: approvalRequestFixture(),
		Recipients: []application.NotificationRecipient{
			{MembershipID: "m-1", SlackUserID: "U0BAD"}, {MembershipID: "m-2", SlackUserID: "U0OK"},
		},
	})
	if !errors.Is(err, failing) || len(partial.posted) != 2 {
		t.Fatalf("partial failure = %v with %d posts, want the error reported and both attempted", err, len(partial.posted))
	}
}

func (f *slackFakes) GetSlackDelivery(_ context.Context, in application.GetSlackDeliveryInput) (application.SlackDelivery, error) {
	if f.token == "" || f.disconnected {
		return application.SlackDelivery{}, domain.ErrNotFound
	}
	return application.SlackDelivery{BotToken: f.token, InstallationID: in.InstallationID}, nil
}
func (f *slackFakes) DisconnectSlackConnection(context.Context, application.DisconnectSlackConnectionInput) (bool, error) {
	f.disconnected = true
	return true, nil
}
func (f *slackFakes) ClaimSlackEmailFallback(context.Context, application.ClaimSlackEmailFallbackInput) (bool, error) {
	if f.fallbackClaimed {
		return false, nil
	}
	f.fallbackClaimed = true
	return true, nil
}

func TestSlackDeliveryRevocationFallsBackOnce(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(fmt.Sprint(revoked), func(t *testing.T) {
			fakes := &slackFakes{}
			if revoked {
				fakes.token = "token"
				fakes.fail = map[string]error{"U1": application.ErrSlackRevoked}
			}
			channel := NewSlackChannel(fakes, fakes, "https://approvals.example.com")
			input := application.SendApprovalPromptInput{Request: approvalRequestFixture(), Recipients: []application.NotificationRecipient{{UserID: "user", MembershipID: "member", SlackUserID: "U1", SlackInstallationID: "generation", SlackFallbackEmail: "owner@example.com"}}}
			err := channel.SendApprovalPrompt(context.Background(), input)
			var fallback *application.SlackUnavailableRecipients
			if !errors.As(err, &fallback) || len(fallback.Recipients) != 1 || fallback.Recipients[0].Emails[0] != "owner@example.com" {
				t.Fatalf("fallback=%+v error=%v", fallback, err)
			}
			if revoked && !fakes.disconnected {
				t.Fatal("revoked grant still active")
			}
			if err := channel.SendApprovalPrompt(context.Background(), input); err != nil {
				t.Fatalf("duplicate fallback=%v", err)
			}
		})
	}
}
