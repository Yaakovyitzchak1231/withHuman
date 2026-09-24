package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type discordFakes struct {
	connected bool
	sent      []application.DiscordDirectMessageInput
	fail      map[string]error
}

func (f *discordFakes) GetDiscordConnection(context.Context, application.GetDiscordConnectionInput) (domain.DiscordConnection, error) {
	if !f.connected {
		return domain.DiscordConnection{}, domain.ErrNotFound
	}
	return domain.DiscordConnection{GuildID: "G0ACME"}, nil
}
func (f *discordFakes) UpsertDiscordConnection(context.Context, application.UpsertDiscordConnectionInput) (domain.DiscordConnection, error) {
	return domain.DiscordConnection{}, nil
}
func (f *discordFakes) DeleteDiscordConnection(context.Context, application.DeleteDiscordConnectionInput) (string, error) {
	return "", nil
}
func (f *discordFakes) CreateDiscordOAuthState(context.Context, application.CreateDiscordOAuthStateInput) error {
	return nil
}
func (f *discordFakes) ConsumeDiscordOAuthState(context.Context, application.ConsumeDiscordOAuthStateInput) (application.DiscordOAuthState, error) {
	return application.DiscordOAuthState{}, nil
}
func (f *discordFakes) GetGuildMember(context.Context, application.DiscordGuildMemberInput) (application.DiscordUser, error) {
	return application.DiscordUser{}, domain.ErrNotFound
}
func (f *discordFakes) SendDirectMessage(_ context.Context, input application.DiscordDirectMessageInput) error {
	f.sent = append(f.sent, input)
	return f.fail[input.DiscordUserID]
}

// A prompt reaches linked reviewers only, carries the tool, the agent,
// the deadline, and a link button, and never the arguments.
func TestDiscordPromptReachesLinkedReviewersWithoutArguments(t *testing.T) {
	fakes := &discordFakes{connected: true}
	channel := NewDiscordChannel(fakes, fakes, "https://approvals.example.com/")
	request := approvalRequestFixture()
	if err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request: request, RoutingOutcome: domain.RoutingEscalated,
		Recipients: []application.NotificationRecipient{
			{MembershipID: "m-1", DisplayName: "Ana", Emails: []string{"ana@example.com"}, DiscordUserID: "100200300"},
			{MembershipID: "m-2", DisplayName: "Ben", Emails: []string{"ben@example.com"}, SlackUserID: "U0BEN"},
		},
	}); err != nil {
		t.Fatalf("SendApprovalPrompt: %v", err)
	}
	if len(fakes.sent) != 1 || fakes.sent[0].DiscordUserID != "100200300" {
		t.Fatalf("sent = %+v, want one message to the linked reviewer", fakes.sent)
	}
	message := fakes.sent[0]
	body := message.Content + string(message.Embeds) + string(message.Components)
	for _, secret := range []string{"cus_8Kd21", "4400", "ZD-4471", "amount_cents"} {
		if strings.Contains(body, secret) {
			t.Fatalf("discord message leaked request payload %q: %s", secret, body)
		}
	}
	for _, expected := range []string{"stripe.refund", "support-bot", "https://approvals.example.com/queue/11111111-1111-4111-8111-111111111111", "<t:"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("discord message missing %q: %s", expected, body)
		}
	}
	var components []struct {
		Type       int `json:"type"`
		Components []struct {
			Type  int    `json:"type"`
			Style int    `json:"style"`
			URL   string `json:"url"`
			Label string `json:"label"`
		} `json:"components"`
	}
	if err := json.Unmarshal(message.Components, &components); err != nil {
		t.Fatalf("components are not an action row: %v", err)
	}
	if len(components) != 1 || components[0].Type != 1 || len(components[0].Components) != 1 {
		t.Fatalf("components = %+v", components)
	}
	if button := components[0].Components[0]; button.Type != 2 || button.Style != 5 || button.Label != "Open in withHuman" || !strings.HasSuffix(button.URL, "/queue/"+string(request.ID)) {
		t.Fatalf("button = %+v, want a style-5 link button to the request", button)
	}
}

// No server and no linked reviewers are not failures: Discord simply has
// nobody to reach. One failed message does not stop the others.
func TestDiscordPromptQuietWhenNothingToDeliver(t *testing.T) {
	unconnected := &discordFakes{}
	channel := NewDiscordChannel(unconnected, unconnected, "https://approvals.example.com")
	if err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request:    approvalRequestFixture(),
		Recipients: []application.NotificationRecipient{{MembershipID: "m-1", DiscordUserID: "100200300"}},
	}); err != nil || len(unconnected.sent) != 0 {
		t.Fatalf("no server = %v with %d sends, want nothing to do", err, len(unconnected.sent))
	}
	connected := &discordFakes{connected: true}
	channel = NewDiscordChannel(connected, connected, "https://approvals.example.com")
	if err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request:    approvalRequestFixture(),
		Recipients: []application.NotificationRecipient{{MembershipID: "m-1", Emails: []string{"a@example.com"}, SlackUserID: "U0A"}},
	}); err != nil || len(connected.sent) != 0 {
		t.Fatalf("no linked reviewers = %v with %d sends, want nothing to do", err, len(connected.sent))
	}

	failing := errors.New("discord user does not accept direct messages")
	partial := &discordFakes{connected: true, fail: map[string]error{"1": failing}}
	channel = NewDiscordChannel(partial, partial, "https://approvals.example.com")
	err := channel.SendApprovalPrompt(context.Background(), application.SendApprovalPromptInput{
		Request: approvalRequestFixture(),
		Recipients: []application.NotificationRecipient{
			{MembershipID: "m-1", DiscordUserID: "1"}, {MembershipID: "m-2", DiscordUserID: "2"},
		},
	})
	if !errors.Is(err, failing) || len(partial.sent) != 2 {
		t.Fatalf("partial failure = %v with %d sends, want the error reported and both attempted", err, len(partial.sent))
	}
}
