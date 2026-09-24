package notifications

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// LogChannel is the correlated logging channel every deployment registers.
// It is the open edition's floor: with no transport configured a
// self-hoster can still see that a notification was due and to whom, and
// the invite link stays available through the management API.
type LogChannel struct {
	logger *slog.Logger
}

func NewLogChannel(logger *slog.Logger) *LogChannel { return &LogChannel{logger: logger} }

func (c *LogChannel) Key() application.NotificationChannelKey {
	return application.NotificationChannelLog
}

func (c *LogChannel) SendApprovalPrompt(ctx context.Context, input application.SendApprovalPromptInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	request := input.Request
	memberships := make([]string, 0, len(input.Recipients))
	for _, recipient := range input.Recipients {
		memberships = append(memberships, string(recipient.MembershipID))
	}
	c.logger.InfoContext(ctx, fmt.Sprintf("approval notification due - %s, %d recipients", request.Tool, len(input.Recipients)), "organization_id", request.OrganizationID,
		"approval_request_id", request.ID, "tool", request.Tool, "deadline_at", request.DeadlineAt,
		"routing_outcome", string(input.RoutingOutcome),
		"recipient_count", len(input.Recipients),
		"recipient_membership_ids", memberships)
	return nil
}

// SendTransactional logs that a message was due without its credential:
// the invite URL embeds a one-time token and a verification code is the
// answer to a live challenge, so neither is ever written here. A phone
// number is named by its contact method rather than written out.
func (c *LogChannel) SendTransactional(ctx context.Context, input application.SendTransactionalInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	fields := []any{
		"kind", string(input.Message.Kind),
		"organization_id", input.Message.OrganizationID,
		"address_kind", string(input.Kind),
		"expires_at", input.Message.ExpiresAt,
	}
	if input.Kind != "" && input.Kind != domain.ContactEmail {
		fields = append(fields, "contact_method_id", input.ContactMethodID)
	} else {
		fields = append(fields, "address", input.Address)
	}
	c.logger.InfoContext(ctx, "transactional notification due - "+string(input.Message.Kind), fields...)
	return nil
}
