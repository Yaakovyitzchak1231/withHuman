package application

import (
	"context"
	"errors"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// ErrSlackRevoked is reserved for definitive provider revocation responses.
// In particular, invalid_auth can also mean a network restriction.
var ErrSlackRevoked = errors.New("Slack access was revoked")

type SlackTokenInput struct{ BotToken string }

func (in SlackTokenInput) Validate() error { return requiredString("bot token", in.BotToken) }

type SlackTokenIdentity struct{ TeamID, BotUserID string }

type SlackConnectionVersion struct{ OrganizationID, InstallationID domain.ID }

func (in SlackConnectionVersion) Validate() error {
	return firstError([]error{requiredID("organization ID", in.OrganizationID), requiredID("installation ID", in.InstallationID)})
}

// SlackLifecycleRepository is internal provider bookkeeping. Team lookups are
// used only after a verified provider event or OAuth grant, never by product reads.
type SlackLifecycleRepository interface {
	ListSlackDisconnections(context.Context, ListPendingSlackEventsInput) ([]SlackDisconnection, error)
	CompleteSlackDisconnection(context.Context, CompleteSlackEventInput) error
	GetSlackDelivery(context.Context, GetSlackDeliveryInput) (SlackDelivery, error)
	WithSlackLock(context.Context, SlackLockInput) error
	FindSlackWorkspace(context.Context, FindSlackWorkspaceInput) (domain.SlackConnection, error)
	DisconnectSlackConnection(context.Context, DisconnectSlackConnectionInput) (bool, error)
	InsertSlackEvent(context.Context, SlackEvent) error
	ListPendingSlackEvents(context.Context, ListPendingSlackEventsInput) ([]SlackEvent, error)
	CompleteSlackEvent(context.Context, CompleteSlackEventInput) error
	ClaimSlackEmailFallback(context.Context, ClaimSlackEmailFallbackInput) (bool, error)
}

type SlackDisconnection struct{ ID, OrganizationID, RequestID, InstallationID domain.ID }

type SlackEventDispatcher interface {
	DispatchSlackEvents(context.Context, DispatchSlackEventsInput) error
}

type DispatchSlackEventsInput struct{ Key string }

func (in DispatchSlackEventsInput) Validate() error { return requiredString("dispatch key", in.Key) }

type GetSlackDeliveryInput struct {
	OrganizationID, UserID, InstallationID domain.ID
	SlackUserID                            string
}

func (in GetSlackDeliveryInput) Validate() error {
	return firstError([]error{requiredID("organization ID", in.OrganizationID), requiredID("user ID", in.UserID), requiredID("installation ID", in.InstallationID), requiredString("Slack user ID", in.SlackUserID)})
}

type SlackDelivery struct {
	BotToken       string
	InstallationID domain.ID
}

// SlackUnavailableRecipients contains only the recipients whose email fallback
// has been claimed. The registry routes them exclusively to email channels.
type SlackUnavailableRecipients struct{ Recipients []NotificationRecipient }

func (*SlackUnavailableRecipients) Error() string { return "Slack unavailable; use account email" }

type SlackLockInput struct {
	OrganizationID domain.ID
	TeamID         string
	Work           func(context.Context) error
}

func (in SlackLockInput) Validate() error {
	if in.Work == nil {
		return invalidField("work", "is required")
	}
	return firstError([]error{requiredID("organization ID", in.OrganizationID), requiredString("team ID", in.TeamID)})
}

type FindSlackWorkspaceInput struct{ TeamID string }

func (in FindSlackWorkspaceInput) Validate() error { return requiredString("team ID", in.TeamID) }

type DisconnectSlackConnectionInput struct {
	SlackConnectionVersion
	Reason string
	Now    time.Time
}

func (in DisconnectSlackConnectionInput) Validate() error {
	return firstError([]error{in.SlackConnectionVersion.Validate(), requiredString("reason", in.Reason), requiredTime("current time", in.Now)})
}

type SlackEvent struct {
	ID                                   domain.ID
	AppID, ExternalEventID, TeamID, Type string
	BotUserIDs                           []string
	OccurredAt, ReceivedAt               time.Time
}

func (in SlackEvent) Validate() error {
	if in.Type != "app_uninstalled" && in.Type != "tokens_revoked" {
		return invalidField("event type", "is not supported")
	}
	return firstError([]error{requiredString("app ID", in.AppID), requiredString("event ID", in.ExternalEventID), requiredString("team ID", in.TeamID), requiredTime("event time", in.OccurredAt), requiredTime("received time", in.ReceivedAt)})
}

type ListPendingSlackEventsInput struct{ Limit int }

func (in ListPendingSlackEventsInput) Validate() error {
	if in.Limit < 1 || in.Limit > 1000 {
		return invalidField("limit", "must be between 1 and 1000")
	}
	return nil
}

type CompleteSlackEventInput struct {
	ID  domain.ID
	Now time.Time
}

func (in CompleteSlackEventInput) Validate() error {
	return firstError([]error{requiredID("event ID", in.ID), requiredTime("current time", in.Now)})
}

type ClaimSlackEmailFallbackInput struct {
	OrganizationID, RequestID, UserID domain.ID
	Now                               time.Time
}

func (in ClaimSlackEmailFallbackInput) Validate() error {
	return firstError([]error{requiredID("organization ID", in.OrganizationID), requiredID("request ID", in.RequestID), requiredID("user ID", in.UserID), requiredTime("current time", in.Now)})
}
