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
	content, embeds, components := discordPromptMessage(input.Request, c.publicOrigin+"/queue/"+string(input.Request.ID))
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

func (c *DiscordChannel) SendTransactional(context.Context, application.SendTransactionalInput) error {
	return nil
}

// discordPromptMessage renders the prompt as one embed and a link button.
// As with email and Slack, it carries the tool, the agent, and the
// deadline, and never the request arguments: a direct message is
// searchable and forwardable, and the reviewer decides in the product
// where authorization applies. The button is a link (style 5), not an
// interaction: deciding from Discord is a later question with its own
// authentication answer.
func discordPromptMessage(request domain.ApprovalRequest, requestURL string) (string, json.RawMessage, json.RawMessage) {
	content := fmt.Sprintf("%s is waiting for your approval", request.Tool)
	deadline := strconv.FormatInt(request.DeadlineAt.Unix(), 10)
	embeds := []map[string]any{{
		"title":       "Approval needed",
		"description": fmt.Sprintf("`%s` is waiting for your approval.", request.Tool),
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
