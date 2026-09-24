// Package notifications holds the open edition's notification channels and
// the registry that dispatches to them. Channels are registered rather than
// hidden behind one adapter because they differ in credentials, retry
// policy, and licensing; the hosted edition registers its own channels
// through the runtime options without core knowing they exist.
package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/loggermdc"
)

// Registry dispatches one message to every registered channel.
//
// Delivery never controls an approval outcome. A channel that fails is
// logged as an operational signal and the remaining channels still run, so
// one broken transport cannot silence the others or fail the action that
// produced the message. The combined error is returned for the caller's
// retry policy, which for approvals is the Temporal activity's.
type Registry struct {
	channels []application.NotificationChannel
	logger   *slog.Logger
}

func NewRegistry(logger *slog.Logger, channels ...application.NotificationChannel) *Registry {
	return &Registry{channels: channels, logger: logger}
}

// Keys lists the registered channels, for startup logging and health.
func (r *Registry) Keys() []application.NotificationChannelKey {
	keys := make([]application.NotificationChannelKey, 0, len(r.channels))
	for _, channel := range r.channels {
		keys = append(keys, channel.Key())
	}
	return keys
}

// SendApprovalPrompt fans the prompt out to every channel and reports
// nothing back: a prompt is best effort per channel, and a failure in
// one must never make the caller try again, because a retry re-sends
// through every channel that already succeeded. Failures are logged with
// the channel that produced them.
func (r *Registry) SendApprovalPrompt(ctx context.Context, input application.SendApprovalPromptInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	_ = r.dispatch(ctx, "approval prompt", func(channel application.NotificationChannel) error {
		err := channel.SendApprovalPrompt(ctx, input)
		var unavailable *application.SlackUnavailableRecipients
		if errors.As(err, &unavailable) {
			fallback := input
			fallback.Recipients = unavailable.Recipients
			for _, email := range r.channels {
				if email.Key() == application.NotificationChannelEmail {
					if sendErr := email.SendApprovalPrompt(ctx, fallback); sendErr != nil && r.logger != nil {
						r.logger.WarnContext(ctx, "approval notification fallback failed - Slack to email", "error", sendErr)
					}
				}
			}
		}
		return err
	})
	return nil
}

func (r *Registry) SendTransactional(ctx context.Context, input application.SendTransactionalInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	return r.dispatch(ctx, "transactional message", func(channel application.NotificationChannel) error {
		return channel.SendTransactional(ctx, input)
	})
}

func (r *Registry) dispatch(ctx context.Context, description string, send func(application.NotificationChannel) error) error {
	var failures []error
	for _, channel := range r.channels {
		if err := send(channel); err != nil {
			failures = append(failures, fmt.Errorf("%s channel: %w", channel.Key(), err))
			if r.logger != nil {
				r.logger.WarnContext(ctx, fmt.Sprintf("notification delivery failed - %s via %s", description, channel.Key()),
					append(loggermdc.KeyValues(ctx, nil), "channel", string(channel.Key()),
						"message", description, "error", err.Error())...)
			}
		}
	}
	return errors.Join(failures...)
}
