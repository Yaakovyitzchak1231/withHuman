package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

func digestFixture(count int, recipient application.NotificationRecipient) application.SendApprovalDigestInput {
	requests := make([]domain.ApprovalRequest, 0, count)
	for index := range count {
		request := approvalRequestFixture()
		request.ID = domain.ID(fmt.Sprintf("request-%02d", index))
		request.Tool = fmt.Sprintf("deploy.step%02d", index)
		if index%3 == 2 {
			request.AgentName = "release-bot"
		}
		requests = append(requests, request)
	}
	return application.SendApprovalDigestInput{OrganizationID: "org-1", Recipient: recipient, Requests: requests}
}

func assertNoArguments(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"cus_8Kd21", "4400", "ZD-4471", "amount_cents"} {
		if strings.Contains(body, secret) {
			t.Fatalf("digest leaked request payload %q:\n%s", secret, body)
		}
	}
	if strings.Contains(body, "—") {
		t.Fatalf("digest copy uses an em dash:\n%s", body)
	}
}

// An email digest names the first ten requests with their links, counts
// the rest, and sends the reviewer to the queue.
func TestEmailDigestListsTenRequestsAndCountsTheRest(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	input := digestFixture(13, application.NotificationRecipient{MembershipID: "m-1", UserID: "u-1", DisplayName: "Ana", Emails: []string{"ana@example.com"}})
	if err := channel.SendApprovalDigest(context.Background(), input); err != nil {
		t.Fatalf("SendApprovalDigest: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d messages, want one digest", len(*sent))
	}
	body := (*sent)[0].body
	assertNoArguments(t, body)
	for _, expected := range []string{
		"Subject: 13 approvals waiting", "Hello Ana,", "13 requests are waiting for your approval:",
		"- deploy.step00 from support-bot (prod runner)", "https://approvals.example.com/queue/request-09",
		"And 3 more.", "Review them all here:\r\nhttps://approvals.example.com/queue\r\n",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("digest missing %q:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "deploy.step10") {
		t.Fatalf("digest should name only ten requests:\n%s", body)
	}
}

func TestSlackDigestPostsOneListToTheLinkedReviewer(t *testing.T) {
	fakes := &slackFakes{token: "xoxb-token"}
	channel := NewSlackChannel(fakes, fakes, "https://approvals.example.com/")
	input := digestFixture(3, application.NotificationRecipient{MembershipID: "m-1", UserID: "u-1", SlackUserID: "U0ANA", SlackInstallationID: "install-1"})
	if err := channel.SendApprovalDigest(context.Background(), input); err != nil {
		t.Fatalf("SendApprovalDigest: %v", err)
	}
	if len(fakes.posted) != 1 || fakes.posted[0].SlackUserID != "U0ANA" {
		t.Fatalf("posted = %+v, want one digest", fakes.posted)
	}
	var blocks any
	if err := json.Unmarshal(fakes.posted[0].Blocks, &blocks); err != nil {
		t.Fatalf("blocks: %v", err)
	}
	body := fmt.Sprint(fakes.posted[0].Text, blocks)
	assertNoArguments(t, body)
	for _, expected := range []string{"3 approvals waiting", "<https://approvals.example.com/queue/request-01|`deploy.step01`>", "Open the queue"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("slack digest missing %q: %s", expected, body)
		}
	}
}

func TestDiscordDigestPostsOneListToTheLinkedReviewer(t *testing.T) {
	fakes := &discordFakes{connected: true}
	channel := NewDiscordChannel(fakes, fakes, "https://approvals.example.com")
	input := digestFixture(2, application.NotificationRecipient{MembershipID: "m-1", UserID: "u-1", DiscordUserID: "100200300"})
	if err := channel.SendApprovalDigest(context.Background(), input); err != nil {
		t.Fatalf("SendApprovalDigest: %v", err)
	}
	if len(fakes.sent) != 1 {
		t.Fatalf("sent = %+v, want one digest", fakes.sent)
	}
	body := fakes.sent[0].Content + string(fakes.sent[0].Embeds) + string(fakes.sent[0].Components)
	assertNoArguments(t, body)
	for _, expected := range []string{"2 approvals waiting", "[`deploy.step00`](https://approvals.example.com/queue/request-00)", "Open the queue"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("discord digest missing %q: %s", expected, body)
		}
	}
}

// A digest narrowed to one kind is only that channel's to deliver.
func TestDigestReachesOnlyTheChannelItIsNarrowedTo(t *testing.T) {
	smtpChannel, sent := testSMTPChannel(t, nil)
	slack := &slackFakes{token: "xoxb-token"}
	registry := NewRegistry(discardLogger(), smtpChannel, NewSlackChannel(slack, slack, "https://approvals.example.com"))
	recipient := application.NotificationRecipient{MembershipID: "m-1", UserID: "u-1", Emails: []string{"ana@example.com"}, SlackUserID: "U0ANA"}
	if err := registry.SendApprovalDigest(context.Background(), digestFixture(2, recipient.Only(domain.ContactSlack))); err != nil {
		t.Fatalf("SendApprovalDigest: %v", err)
	}
	if len(*sent) != 0 || len(slack.posted) != 1 {
		t.Fatalf("emails=%d slack=%d, want Slack only", len(*sent), len(slack.posted))
	}
}

func TestDigestRefusesASingleRequest(t *testing.T) {
	registry := NewRegistry(discardLogger())
	if err := registry.SendApprovalDigest(context.Background(), digestFixture(1, application.NotificationRecipient{UserID: "u-1"})); err == nil {
		t.Fatal("a digest of one request should be refused: it is a prompt")
	}
}

func TestDigestAgentsCountsWhoAsked(t *testing.T) {
	agents := digestFixture(6, application.NotificationRecipient{}).DigestAgents()
	if len(agents) != 2 || agents[0] != (application.DigestAgent{Name: "support-bot", Count: 4}) || agents[1] != (application.DigestAgent{Name: "release-bot", Count: 2}) {
		t.Fatalf("agents = %+v", agents)
	}
}
