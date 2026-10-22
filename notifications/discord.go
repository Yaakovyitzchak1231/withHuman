package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// DiscordChannel delivers approval prompts as direct messages from the
// deployment's bot to members who linked their Discord account in the
// organization's connected server. It is registered whenever Discord is
// configured for the deployment; whether a given organization has a
// server, and which reviewers are linked, is decided per message.
//
// Transactional messages are not delivered here for the same reason as
// Slack: invitations and verification codes belong to email.
type DiscordChannel struct {
	connections  application.DiscordConnectionRepository
	messenger    application.DiscordMessenger
	publicOrigin string
}

func NewDiscordChannel(connections application.DiscordConnectionRepository, messenger application.DiscordMessenger, publicOrigin string) *DiscordChannel {
	return &DiscordChannel{connections: connections, messenger: messenger, publicOrigin: strings.TrimRight(publicOrigin, "/")}
}

func (c *DiscordChannel) Key() application.NotificationChannelKey {
	return application.NotificationChannelDiscord
}

// SendApprovalPrompt messages every linked recipient. An organization with
// no server, or recipients with no link, are simply not Discord's to
// reach; only a failure to deliver to someone who should have received it
// is an error.
func (c *DiscordChannel) SendApprovalPrompt(ctx context.Context, input application.SendApprovalPromptInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	linked := 0
	for _, recipient := range input.Recipients {
		if recipient.DiscordUserID != "" {
			linked++
		}
	}
	if linked == 0 {
		return nil
	}
	_, err := c.connections.GetDiscordConnection(ctx, application.GetDiscordConnectionInput{OrganizationID: input.Request.OrganizationID})
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	content, embeds, components := discordPromptMessage(approvalMessageFor(input.Request, input.Summary), c.publicOrigin+"/queue/"+string(input.Request.ID))
	var failures []error
	for _, recipient := range input.Recipients {
		if recipient.DiscordUserID == "" {
			continue
		}
		if err := c.messenger.SendDirectMessage(ctx, application.DiscordDirectMessageInput{
			DiscordUserID: recipient.DiscordUserID, Content: content, Embeds: embeds, Components: components,
		}); err != nil {
			failures = append(failures, fmt.Errorf("discord message to %s: %w", recipient.MembershipID, err))
		}
	}
	return errors.Join(failures...)
}

// SendApprovalDigest messages the narrowed recipient one list of the
// waiting requests.
func (c *DiscordChannel) SendApprovalDigest(ctx context.Context, input application.SendApprovalDigestInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if input.Recipient.DiscordUserID == "" {
		return nil
	}
	_, err := c.connections.GetDiscordConnection(ctx, application.GetDiscordConnectionInput{OrganizationID: input.OrganizationID})
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	content, embeds, components := discordDigestMessage(approvalMessagesFor(input.Requests, input.Summaries), c.publicOrigin+"/queue")
	if err := c.messenger.SendDirectMessage(ctx, application.DiscordDirectMessageInput{
		DiscordUserID: input.Recipient.DiscordUserID, Content: content, Embeds: embeds, Components: components,
	}); err != nil {
		return fmt.Errorf("discord digest to %s: %w", input.Recipient.MembershipID, err)
	}
	return nil
}

func (c *DiscordChannel) SendTransactional(context.Context, application.SendTransactionalInput) error {
	return nil
}

// discordPromptMessage renders the prompt as one embed and a link button.
// As with email and Slack, it carries the request's summary when it has
// one, the tool, the agent, and the deadline, and never the request
// arguments: a direct message is searchable and forwardable, and the
// reviewer decides in the product where authorization applies. The button
// is a link (style 5), not an interaction: deciding from Discord is a later
// question with its own authentication answer.
func discordPromptMessage(request approvalMessageRequest, requestURL string) (string, json.RawMessage, json.RawMessage) {
	content := fmt.Sprintf("%s is waiting for your approval", request.Tool)
	title, description := "Approval needed", fmt.Sprintf("`%s` is waiting for your approval.", request.Tool)
	if request.Summary != "" {
		content = fmt.Sprintf("%s is waiting for your approval", discordEscape(request.Summary))
		title, description = truncateRunes(request.Summary, discordTitleLimit), "Waiting for your approval."
	}
	deadline := strconv.FormatInt(request.DeadlineAt.Unix(), 10)
	embeds := []map[string]any{{
		"title":       title,
		"description": description,
		"fields": []map[string]any{
			{"name": "Tool", "value": request.Tool, "inline": true},
			{"name": "Agent", "value": fmt.Sprintf("%s (%s)", request.AgentName, request.AgentInstanceName), "inline": true},
			{"name": "Expires", "value": fmt.Sprintf("<t:%s:R> (<t:%s:f>)", deadline, deadline)},
		},
		"footer": map[string]any{"text": "The request details are in the app, where your decision is recorded."},
	}}
	components := []map[string]any{{
		"type": 1,
		"components": []map[string]any{
			{"type": 2, "style": 5, "label": "Open in withHuman", "url": requestURL},
		},
	}}
	return content, marshalForDiscord(embeds), marshalForDiscord(components)
}

// discordDigestMessage renders a digest as one embed listing the waiting
// requests, each linking to itself, and a link button to the queue.
func discordDigestMessage(requests []approvalMessageRequest, queueURL string) (string, json.RawMessage, json.RawMessage) {
	content := fmt.Sprintf("%d approvals waiting", len(requests))
	shown, more := digestShown(requests)
	lines := make([]string, 0, len(shown)+1)
	for _, request := range shown {
		label := fmt.Sprintf("`%s`", request.Tool)
		if request.Summary != "" {
			label = discordEscape(request.Summary)
		}
		lines = append(lines, fmt.Sprintf("- [%s](%s/%s) from %s (%s)", label, queueURL, request.ID, request.AgentName, request.AgentInstanceName))
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("And %d more.", more))
	}
	embeds := []map[string]any{{
		"title":       content,
		"description": strings.Join(lines, "\n"),
		"footer":      map[string]any{"text": "The request details are in the app, where your decisions are recorded."},
	}}
	components := []map[string]any{{
		"type": 1,
		"components": []map[string]any{
			{"type": 2, "style": 5, "label": "Open the queue", "url": queueURL},
		},
	}}
	return content, marshalForDiscord(embeds), marshalForDiscord(components)
}

// discordTitleLimit is the most characters an embed title takes; Discord
// rejects the whole message over it.
const discordTitleLimit = 256

// discordEscape keeps a summary literal in Discord markdown, so it cannot
// form a link, a mention or formatting.
var discordEscape = strings.NewReplacer(
	`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`,
	"[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`,
).Replace

// marshalForDiscord keeps Discord's <t:...> timestamp markup readable:
// the default encoder escapes angle brackets for HTML, which Discord
// would decode but which makes logs and tests harder to read.
func marshalForDiscord(value any) json.RawMessage {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return json.RawMessage(bytes.TrimSpace(buffer.Bytes()))
}
