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

const refundSummary = "Refund $49.00 for a faulty bag"

func summaryPromptInput(summary string, recipient application.NotificationRecipient) application.SendApprovalPromptInput {
	return application.SendApprovalPromptInput{
		Request: approvalRequestFixture(), RoutingOutcome: domain.RoutingEscalated,
		Recipients: []application.NotificationRecipient{recipient}, Summary: summary,
	}
}

// summaryDigestInput gives the first and third requests a summary and
// leaves the second without one.
func summaryDigestInput(recipient application.NotificationRecipient) application.SendApprovalDigestInput {
	input := digestFixture(3, recipient)
	input.Summaries = map[domain.ID]string{
		"request-00": "Release billing-service v2.4.1",
		"request-02": refundSummary,
	}
	return input
}

func assertContainsAll(t *testing.T, what, body string, expected ...string) {
	t.Helper()
	for _, text := range expected {
		if !strings.Contains(body, text) {
			t.Fatalf("%s missing %q:\n%s", what, text, body)
		}
	}
}

// A summary is the one place argument values may appear; the raw
// arguments still never do.
func assertNoRawArguments(t *testing.T, what, body string) {
	t.Helper()
	for _, secret := range []string{"cus_8Kd21", "4400", "ZD-4471", "amount_cents"} {
		if strings.Contains(body, secret) {
			t.Fatalf("%s leaked request payload %q:\n%s", what, secret, body)
		}
	}
	if strings.Contains(body, "—") {
		t.Fatalf("%s copy uses an em dash:\n%s", what, body)
	}
}

func TestEmailPromptLeadsWithTheSummary(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	input := summaryPromptInput(refundSummary, application.NotificationRecipient{MembershipID: "m-1", DisplayName: "Ana", Emails: []string{"ana@example.com"}})
	if err := channel.SendApprovalPrompt(context.Background(), input); err != nil {
		t.Fatalf("SendApprovalPrompt: %v", err)
	}
	body := (*sent)[0].body
	assertNoRawArguments(t, "email", body)
	assertContainsAll(t, "email", body,
		"Subject: Approval needed: Refund $49.00 for a faulty bag\r\n",
		"Hello Ana,\r\n\r\nRefund $49.00 for a faulty bag is waiting for your approval.\r\n\r\n",
		"Agent: support-bot (prod runner)\r\nTool: stripe.refund\r\nExpires: ",
		"https://approvals.example.com/queue/11111111-1111-4111-8111-111111111111",
	)
}

// Without a summary the prompt is exactly what it was before summaries.
func TestEmailPromptWithoutSummaryIsUnchanged(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	input := summaryPromptInput("", application.NotificationRecipient{MembershipID: "m-1", DisplayName: "Ana", Emails: []string{"ana@example.com"}})
	if err := channel.SendApprovalPrompt(context.Background(), input); err != nil {
		t.Fatalf("SendApprovalPrompt: %v", err)
	}
	want := "From: withHuman <approvals@example.com>\r\nTo: ana@example.com\r\nSubject: Approval needed: stripe.refund\r\nDate: Tue, 14 Nov 2023 22:13:20 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello Ana,\r\n\r\nstripe.refund is waiting for your approval.\r\n\r\nAgent: support-bot (prod runner)\r\nExpires: Wed, 15 Nov 2023 04:13:20 UTC\r\n\r\nReview it here:\r\nhttps://approvals.example.com/queue/11111111-1111-4111-8111-111111111111\r\n\r\nThe request details are in the app, where your decision is recorded.\r\n"
	if got := (*sent)[0].body; got != want {
		t.Fatalf("email changed without a summary:\n got %q\nwant %q", got, want)
	}
}

// A summary is folded onto one line, so it cannot add a mail header.
func TestEmailPromptSummaryCannotBreakTheSubject(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	input := summaryPromptInput("Refund $49.00\r\nBcc: someone@example.com", application.NotificationRecipient{MembershipID: "m-1", Emails: []string{"ana@example.com"}})
	if err := channel.SendApprovalPrompt(context.Background(), input); err != nil {
		t.Fatalf("SendApprovalPrompt: %v", err)
	}
	body := (*sent)[0].body
	assertContainsAll(t, "email", body, "Subject: Approval needed: Refund $49.00 Bcc: someone@example.com\r\n")
	if strings.Contains(body, "\r\nBcc:") {
		t.Fatalf("summary added a header:\n%s", body)
	}
}

func TestEmailDigestNamesRequestsBySummary(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	input := summaryDigestInput(application.NotificationRecipient{MembershipID: "m-1", Emails: []string{"ana@example.com"}})
	if err := channel.SendApprovalDigest(context.Background(), input); err != nil {
		t.Fatalf("SendApprovalDigest: %v", err)
	}
	body := (*sent)[0].body
	assertNoRawArguments(t, "email digest", body)
	assertContainsAll(t, "email digest", body,
		"Subject: 3 approvals waiting\r\n",
		"- Release billing-service v2.4.1 from support-bot (prod runner), expires ",
		"- deploy.step01 from support-bot (prod runner), expires ",
		"- Refund $49.00 for a faulty bag from release-bot (prod runner), expires ",
	)
	if strings.Contains(body, "deploy.step00") || strings.Contains(body, "deploy.step02") {
		t.Fatalf("a request with a summary should not be named by its tool:\n%s", body)
	}
}

func slackPromptFor(t *testing.T, summary string) application.SlackDirectMessageInput {
	t.Helper()
	fakes := &slackFakes{token: "xoxb-token"}
	channel := NewSlackChannel(fakes, fakes, "https://approvals.example.com")
	if err := channel.SendApprovalPrompt(context.Background(), summaryPromptInput(summary, application.NotificationRecipient{MembershipID: "m-1", SlackUserID: "U0ANA"})); err != nil {
		t.Fatalf("SendApprovalPrompt: %v", err)
	}
	if len(fakes.posted) != 1 {
		t.Fatalf("posted = %+v, want one message", fakes.posted)
	}
	return fakes.posted[0]
}

type slackBlock struct {
	Type   string                  `json:"type"`
	Text   *struct{ Text string }  `json:"text"`
	Fields []struct{ Text string } `json:"fields"`
}

func TestSlackPromptLeadsWithTheSummary(t *testing.T) {
	message := slackPromptFor(t, refundSummary)
	assertNoRawArguments(t, "slack", message.Text+string(message.Blocks))
	if message.Text != "Refund $49.00 for a faulty bag is waiting for your approval" {
		t.Fatalf("text = %q", message.Text)
	}
	var blocks []slackBlock
	if err := json.Unmarshal(message.Blocks, &blocks); err != nil {
		t.Fatalf("blocks: %v", err)
	}
	if blocks[0].Type != "header" || blocks[0].Text.Text != refundSummary {
		t.Fatalf("header = %+v, want the summary", blocks[0])
	}
	if blocks[1].Text.Text != "Waiting for your approval." {
		t.Fatalf("lead = %q", blocks[1].Text.Text)
	}
	fields := blocks[2].Fields
	if len(fields) != 3 || fields[0].Text != "*Agent*\nsupport-bot (prod runner)" || fields[1].Text != "*Tool*\n`stripe.refund`" || !strings.HasPrefix(fields[2].Text, "*Expires*\n") {
		t.Fatalf("fields = %+v, want agent, tool, expires", fields)
	}
}

// Without a summary the prompt is exactly what it was before summaries.
func TestSlackPromptWithoutSummaryIsUnchanged(t *testing.T) {
	message := slackPromptFor(t, "")
	wantBlocks := "[{\"text\":{\"text\":\"Approval needed\",\"type\":\"plain_text\"},\"type\":\"header\"},{\"text\":{\"text\":\"`stripe.refund` is waiting for your approval.\",\"type\":\"mrkdwn\"},\"type\":\"section\"},{\"fields\":[{\"text\":\"*Agent*\\nsupport-bot (prod runner)\",\"type\":\"mrkdwn\"},{\"text\":\"*Expires*\\nWed, 15 Nov 2023 04:13:20 UTC\",\"type\":\"mrkdwn\"}],\"type\":\"section\"},{\"elements\":[{\"style\":\"primary\",\"text\":{\"text\":\"Open in withHuman\",\"type\":\"plain_text\"},\"type\":\"button\",\"url\":\"https://approvals.example.com/queue/11111111-1111-4111-8111-111111111111\"}],\"type\":\"actions\"},{\"elements\":[{\"text\":\"The request details are in the app, where your decision is recorded.\",\"type\":\"mrkdwn\"}],\"type\":\"context\"}]"
	if message.Text != "stripe.refund is waiting for your approval" || string(message.Blocks) != wantBlocks {
		t.Fatalf("slack changed without a summary:\ntext %q\nblocks %s", message.Text, message.Blocks)
	}
}

// A summary stays literal: it cannot form a Slack link, and a long one
// still fits the header.
func TestSlackPromptSummaryIsEscapedAndFitsTheHeader(t *testing.T) {
	message := slackPromptFor(t, "Pay <https://evil.example|the invoice> & close "+strings.Repeat("x", 200))
	if !strings.HasPrefix(message.Text, "Pay &lt;https://evil.example|the invoice&gt; &amp; close ") {
		t.Fatalf("text = %q", message.Text)
	}
	var blocks []slackBlock
	if err := json.Unmarshal(message.Blocks, &blocks); err != nil {
		t.Fatalf("blocks: %v", err)
	}
	if header := []rune(blocks[0].Text.Text); len(header) != slackHeaderLimit || header[len(header)-1] != '…' {
		t.Fatalf("header = %q, want it cut to %d characters", blocks[0].Text.Text, slackHeaderLimit)
	}
}

func TestSlackDigestNamesRequestsBySummary(t *testing.T) {
	fakes := &slackFakes{token: "xoxb-token"}
	channel := NewSlackChannel(fakes, fakes, "https://approvals.example.com")
	if err := channel.SendApprovalDigest(context.Background(), summaryDigestInput(application.NotificationRecipient{MembershipID: "m-1", SlackUserID: "U0ANA"})); err != nil {
		t.Fatalf("SendApprovalDigest: %v", err)
	}
	var blocks []slackBlock
	if err := json.Unmarshal(fakes.posted[0].Blocks, &blocks); err != nil {
		t.Fatalf("blocks: %v", err)
	}
	list := blocks[1].Text.Text
	assertNoRawArguments(t, "slack digest", list)
	want := strings.Join([]string{
		"• <https://approvals.example.com/queue/request-00|Release billing-service v2.4.1> from support-bot (prod runner)",
		"• <https://approvals.example.com/queue/request-01|`deploy.step01`> from support-bot (prod runner)",
		"• <https://approvals.example.com/queue/request-02|Refund $49.00 for a faulty bag> from release-bot (prod runner)",
	}, "\n")
	if list != want {
		t.Fatalf("slack digest list:\n got %q\nwant %q", list, want)
	}
}

func discordPromptFor(t *testing.T, summary string) application.DiscordDirectMessageInput {
	t.Helper()
	fakes := &discordFakes{connected: true}
	channel := NewDiscordChannel(fakes, fakes, "https://approvals.example.com")
	if err := channel.SendApprovalPrompt(context.Background(), summaryPromptInput(summary, application.NotificationRecipient{MembershipID: "m-1", DiscordUserID: "100200300"})); err != nil {
		t.Fatalf("SendApprovalPrompt: %v", err)
	}
	if len(fakes.sent) != 1 {
		t.Fatalf("sent = %+v, want one message", fakes.sent)
	}
	return fakes.sent[0]
}

type discordEmbed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Fields      []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"fields"`
}

func TestDiscordPromptLeadsWithTheSummary(t *testing.T) {
	message := discordPromptFor(t, refundSummary)
	assertNoRawArguments(t, "discord", message.Content+string(message.Embeds))
	if message.Content != "Refund $49.00 for a faulty bag is waiting for your approval" {
		t.Fatalf("content = %q", message.Content)
	}
	var embeds []discordEmbed
	if err := json.Unmarshal(message.Embeds, &embeds); err != nil {
		t.Fatalf("embeds: %v", err)
	}
	embed := embeds[0]
	if embed.Title != refundSummary || embed.Description != "Waiting for your approval." {
		t.Fatalf("embed = %+v, want the summary as its title", embed)
	}
	if len(embed.Fields) != 3 || embed.Fields[0].Name != "Tool" || embed.Fields[0].Value != "stripe.refund" || embed.Fields[1].Name != "Agent" {
		t.Fatalf("fields = %+v, want tool and agent", embed.Fields)
	}
}

// Without a summary the prompt is exactly what it was before summaries.
func TestDiscordPromptWithoutSummaryIsUnchanged(t *testing.T) {
	message := discordPromptFor(t, "")
	wantEmbeds := "[{\"description\":\"`stripe.refund` is waiting for your approval.\",\"fields\":[{\"inline\":true,\"name\":\"Tool\",\"value\":\"stripe.refund\"},{\"inline\":true,\"name\":\"Agent\",\"value\":\"support-bot (prod runner)\"},{\"name\":\"Expires\",\"value\":\"<t:1700021600:R> (<t:1700021600:f>)\"}],\"footer\":{\"text\":\"The request details are in the app, where your decision is recorded.\"},\"title\":\"Approval needed\"}]"
	if message.Content != "stripe.refund is waiting for your approval" || string(message.Embeds) != wantEmbeds {
		t.Fatalf("discord changed without a summary:\ncontent %q\nembeds %s", message.Content, message.Embeds)
	}
}

// A summary stays literal: it cannot form a Discord link or a mention,
// and a long one still fits the title.
func TestDiscordPromptSummaryIsEscapedAndFitsTheTitle(t *testing.T) {
	message := discordPromptFor(t, "Pay [the invoice](https://evil.example) for <@123> "+strings.Repeat("x", 300))
	if !strings.HasPrefix(message.Content, `Pay \[the invoice\](https://evil.example) for \<@123\> `) {
		t.Fatalf("content = %q", message.Content)
	}
	var embeds []discordEmbed
	if err := json.Unmarshal(message.Embeds, &embeds); err != nil {
		t.Fatalf("embeds: %v", err)
	}
	if title := []rune(embeds[0].Title); len(title) != discordTitleLimit || title[len(title)-1] != '…' {
		t.Fatalf("title = %q, want it cut to %d characters", embeds[0].Title, discordTitleLimit)
	}
}

func TestDiscordDigestNamesRequestsBySummary(t *testing.T) {
	fakes := &discordFakes{connected: true}
	channel := NewDiscordChannel(fakes, fakes, "https://approvals.example.com")
	if err := channel.SendApprovalDigest(context.Background(), summaryDigestInput(application.NotificationRecipient{MembershipID: "m-1", DiscordUserID: "100200300"})); err != nil {
		t.Fatalf("SendApprovalDigest: %v", err)
	}
	var embeds []discordEmbed
	if err := json.Unmarshal(fakes.sent[0].Embeds, &embeds); err != nil {
		t.Fatalf("embeds: %v", err)
	}
	list := embeds[0].Description
	assertNoRawArguments(t, "discord digest", list)
	want := strings.Join([]string{
		"- [Release billing-service v2.4.1](https://approvals.example.com/queue/request-00) from support-bot (prod runner)",
		"- [`deploy.step01`](https://approvals.example.com/queue/request-01) from support-bot (prod runner)",
		"- [Refund $49.00 for a faulty bag](https://approvals.example.com/queue/request-02) from release-bot (prod runner)",
	}, "\n")
	if list != want {
		t.Fatalf("discord digest list:\n got %q\nwant %q", list, want)
	}
	if fmt.Sprint(fakes.sent[0].Content) != "3 approvals waiting" {
		t.Fatalf("content = %q", fakes.sent[0].Content)
	}
}
