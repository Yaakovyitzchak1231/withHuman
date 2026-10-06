package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// SlackChannel delivers approval prompts as direct messages in the
// organization's connected workspace. It is registered whenever Slack is
// configured for the deployment; whether a given organization has a
// workspace, and which reviewers are linked in it, is decided per message.
//
// Transactional messages are not delivered here: an invitation goes to
// someone who is not a member yet, and a verification code proves an
// email address, so both belong to email.
type SlackChannel struct {
	dispatcher   application.SlackEventDispatcher
	connections  application.SlackConnectionRepository
	messenger    application.SlackMessenger
	publicOrigin string
}

func (c *SlackChannel) WithEventDispatcher(dispatcher application.SlackEventDispatcher) *SlackChannel {
	c.dispatcher = dispatcher
	return c
}

func NewSlackChannel(connections application.SlackConnectionRepository, messenger application.SlackMessenger, publicOrigin string) *SlackChannel {
	return &SlackChannel{connections: connections, messenger: messenger, publicOrigin: strings.TrimRight(publicOrigin, "/")}
}

func (c *SlackChannel) Key() application.NotificationChannelKey {
	return application.NotificationChannelSlack
}

// SendApprovalPrompt messages every linked recipient. An organization with
// no workspace, or recipients with no link, are simply not Slack's to
// reach; only a failure to deliver to someone who should have received it
// is an error.
func (c *SlackChannel) SendApprovalPrompt(ctx context.Context, input application.SendApprovalPromptInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	text, blocks := approvalPromptBlocks(input.Request, c.publicOrigin+"/queue/"+string(input.Request.ID))
	var failures []error
	unavailable := &application.SlackUnavailableRecipients{}
	for _, recipient := range input.Recipients {
		if recipient.SlackUserID == "" && !recipient.SlackFallback {
			continue
		}
		missing := recipient.SlackFallback
		if !missing {
			delivery, err := c.connections.GetSlackDelivery(ctx, application.GetSlackDeliveryInput{
				OrganizationID: input.Request.OrganizationID, UserID: recipient.UserID,
				InstallationID: recipient.SlackInstallationID, SlackUserID: recipient.SlackUserID,
			})
			if errors.Is(err, domain.ErrNotFound) {
				missing = true
			} else if err != nil {
				failures = append(failures, err)
				continue
			} else {
				err = c.messenger.PostDirectMessage(ctx, application.SlackDirectMessageInput{BotToken: delivery.BotToken, SlackUserID: recipient.SlackUserID, Text: text, Blocks: blocks})
				if errors.Is(err, application.ErrSlackRevoked) {
					changed, disconnectErr := c.connections.DisconnectSlackConnection(ctx, application.DisconnectSlackConnectionInput{
						SlackConnectionVersion: application.SlackConnectionVersion{OrganizationID: input.Request.OrganizationID, InstallationID: delivery.InstallationID}, Reason: "token_revoked", Now: time.Now().UTC(),
					})
					if disconnectErr != nil {
						failures = append(failures, disconnectErr)
					}
					if changed && disconnectErr == nil && c.dispatcher != nil {
						bounded, cancel := context.WithTimeout(ctx, time.Second)
						if err := c.dispatcher.DispatchSlackEvents(bounded, application.DispatchSlackEventsInput{Key: "disconnection/" + string(delivery.InstallationID)}); err != nil {
							failures = append(failures, err)
						}
						cancel()
					}
					missing = true
				} else if err != nil {
					failures = append(failures, fmt.Errorf("slack message to %s: %w", recipient.MembershipID, err))
				}
			}
		}
		if missing && recipient.SlackFallbackEmail != "" {
			claimed, err := c.connections.ClaimSlackEmailFallback(ctx, application.ClaimSlackEmailFallbackInput{OrganizationID: input.Request.OrganizationID, RequestID: input.Request.ID, UserID: recipient.UserID, Now: time.Now().UTC()})
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if claimed {
				recipient.Emails = []string{recipient.SlackFallbackEmail}
				recipient.SlackUserID = ""
				unavailable.Recipients = append(unavailable.Recipients, recipient)
			}
		}
	}
	if len(unavailable.Recipients) > 0 {
		failures = append(failures, unavailable)
	}
	return errors.Join(failures...)
}

// SendApprovalDigest messages the narrowed recipient one list of the
// waiting requests. Unlike a prompt it has no email fallback: the
// requests already reached the person once, and they are in the queue.
func (c *SlackChannel) SendApprovalDigest(ctx context.Context, input application.SendApprovalDigestInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	recipient := input.Recipient
	if recipient.SlackUserID == "" {
		return nil
	}
	delivery, err := c.connections.GetSlackDelivery(ctx, application.GetSlackDeliveryInput{
		OrganizationID: input.OrganizationID, UserID: recipient.UserID,
		InstallationID: recipient.SlackInstallationID, SlackUserID: recipient.SlackUserID,
	})
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	text, blocks := approvalDigestBlocks(input.Requests, c.publicOrigin+"/queue")
	if err := c.messenger.PostDirectMessage(ctx, application.SlackDirectMessageInput{BotToken: delivery.BotToken, SlackUserID: recipient.SlackUserID, Text: text, Blocks: blocks}); err != nil {
		return fmt.Errorf("slack digest to %s: %w", recipient.MembershipID, err)
	}
	return nil
}

func (c *SlackChannel) SendTransactional(context.Context, application.SendTransactionalInput) error {
	return nil
}

// approvalPromptBlocks renders the prompt as Block Kit. As with email, it
// carries the tool, the agent, and the deadline, and never the request
// arguments: a Slack message is searchable and forwardable, and the
// reviewer decides in the product where authorization applies. The
// URL button also sends an interaction callback, which the server acknowledges
// without recording a decision.
func approvalPromptBlocks(request domain.ApprovalRequest, requestURL string) (string, json.RawMessage) {
	text := fmt.Sprintf("%s is waiting for your approval", request.Tool)
	deadline := request.DeadlineAt.UTC().Format(time.RFC1123)
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": "Approval needed"}},
		{"type": "section", "text": map[string]any{"type": "mrkdwn",
			"text": fmt.Sprintf("`%s` is waiting for your approval.", request.Tool)}},
		{"type": "section", "fields": []map[string]any{
			{"type": "mrkdwn", "text": fmt.Sprintf("*Agent*\n%s (%s)", request.AgentName, request.AgentInstanceName)},
			{"type": "mrkdwn", "text": fmt.Sprintf("*Expires*\n%s", deadline)},
		}},
		{"type": "actions", "elements": []map[string]any{
			{"type": "button", "text": map[string]any{"type": "plain_text", "text": "Open in withHuman"},
				"url": requestURL, "style": "primary"},
		}},
		{"type": "context", "elements": []map[string]any{
			{"type": "mrkdwn", "text": "The request details are in the app, where your decision is recorded."},
		}},
	}
	encoded, _ := json.Marshal(blocks)
	return text, encoded
}

// approvalDigestBlocks renders a digest as one message: a line per
// request linking to it, a count of the rest, and a button to the queue.
func approvalDigestBlocks(requests []domain.ApprovalRequest, queueURL string) (string, json.RawMessage) {
	text := fmt.Sprintf("%d approvals waiting", len(requests))
	shown, more := digestShown(requests)
	lines := make([]string, 0, len(shown)+1)
	for _, request := range shown {
		lines = append(lines, fmt.Sprintf("• <%s/%s|`%s`> from %s (%s)", queueURL, request.ID, request.Tool, request.AgentName, request.AgentInstanceName))
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("And %d more.", more))
	}
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": text}},
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": strings.Join(lines, "\n")}},
		{"type": "actions", "elements": []map[string]any{
			{"type": "button", "text": map[string]any{"type": "plain_text", "text": "Open the queue"},
				"url": queueURL, "style": "primary"},
		}},
		{"type": "context", "elements": []map[string]any{
			{"type": "mrkdwn", "text": "The request details are in the app, where your decisions are recorded."},
		}},
	}
	encoded, _ := json.Marshal(blocks)
	return text, encoded
}
