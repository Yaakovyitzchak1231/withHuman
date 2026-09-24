package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// SlackConnector speaks Slack's OAuth surface for the organization-level
// install. It knows nothing about organizations; the service binds the
// install to one through the state it hands the connector.
type SlackConnector interface {
	CheckToken(ctx context.Context, input SlackTokenInput) (SlackTokenIdentity, error)
	// AuthorizeURL is where the installing admin is sent. State is opaque
	// to Slack and returns unchanged on the callback.
	AuthorizeURL(state, redirectURL string) string
	// Exchange trades the callback code for the workspace's bot token.
	Exchange(ctx context.Context, input SlackExchangeInput) (SlackInstallation, error)
	// Revoke invalidates a bot token at Slack when the workspace is
	// disconnected, so a copy of the sealed token is useless afterwards.
	Revoke(ctx context.Context, botToken string) error
}

// SlackMessenger speaks the workspace-scoped parts of Slack with a bot
// token the caller opens: finding a member and reaching them directly.
type SlackMessenger interface {
	// LookupUserByEmail returns the Slack user matching an address, or
	// ErrNotFound when the workspace has no such member.
	LookupUserByEmail(ctx context.Context, botToken, email string) (SlackUser, error)
	// PostDirectMessage delivers one message to a person's DM with the bot.
	PostDirectMessage(ctx context.Context, input SlackDirectMessageInput) error
}

type SlackUser struct {
	ID          string
	Handle      string
	DisplayName string
}

// SlackDirectMessageInput carries a rendered message. Text is the plain
// fallback Slack shows in notifications; Blocks is the Block Kit payload,
// already serialized by the channel that knows the message's shape.
type SlackDirectMessageInput struct {
	BotToken    string
	SlackUserID string
	Text        string
	Blocks      json.RawMessage
}

func (input SlackDirectMessageInput) Validate() error {
	if err := firstError([]error{
		requiredString("bot token", input.BotToken),
		requiredString("Slack user ID", input.SlackUserID),
		requiredString("message text", input.Text),
	}); err != nil {
		return err
	}
	return optionalJSON("message blocks", input.Blocks)
}

type SlackExchangeInput struct {
	Code        string
	RedirectURL string
}

func (input SlackExchangeInput) Validate() error {
	return firstError([]error{
		requiredString("authorization code", input.Code),
		requiredString("redirect URL", input.RedirectURL),
	})
}

// SlackInstallation is what an accepted install yields. BotToken is a
// credential: it is sealed before storage and never logged.
type SlackInstallation struct {
	TeamID    string
	TeamName  string
	BotUserID string
	Scopes    []string
	BotToken  string
}

// SlackConnectionRepository owns the per-organization workspace row and
// the short-lived install states that bind a callback to the
// organization that started it.
type SlackConnectionRepository interface {
	SlackLifecycleRepository
	GetSlackConnection(ctx context.Context, input GetSlackConnectionInput) (domain.SlackConnection, error)
	// OpenSlackBotToken returns the unsealed token for the adapter that
	// posts to Slack. Nothing else should call it.
	OpenSlackBotToken(ctx context.Context, input GetSlackConnectionInput) (string, error)
	UpsertSlackConnection(ctx context.Context, input UpsertSlackConnectionInput) (domain.SlackConnection, error)
	DeleteSlackConnection(ctx context.Context, input DeleteSlackConnectionInput) (string, error)
	CreateSlackInstallState(ctx context.Context, input CreateSlackInstallStateInput) error
	// ConsumeSlackInstallState redeems a state exactly once and returns
	// who began the install, or ErrNotFound for an unknown, expired, or
	// already used state.
	ConsumeSlackInstallState(ctx context.Context, input ConsumeSlackInstallStateInput) (SlackInstallState, error)
}

type GetSlackConnectionInput struct {
	InstallationID domain.ID
	OrganizationID domain.ID
}

func (input GetSlackConnectionInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}

type UpsertSlackConnectionInput struct {
	InstallationID          domain.ID
	PreviousOwner           *SlackConnectionVersion
	OrganizationID          domain.ID
	Installation            SlackInstallation
	SealedBotToken          []byte
	InstalledByMembershipID domain.ID
	Now                     time.Time
}

func (input UpsertSlackConnectionInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("Slack team ID", input.Installation.TeamID),
		requiredString("Slack team name", input.Installation.TeamName),
		requiredString("Slack bot user ID", input.Installation.BotUserID),
		requiredID("installing membership ID", input.InstalledByMembershipID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if len(input.SealedBotToken) == 0 {
		return invalidField("sealed bot token", "must not be empty")
	}
	return nil
}

type DeleteSlackConnectionInput struct {
	InstallationID    domain.ID
	OrganizationID    domain.ID
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input DeleteSlackConnectionInput) Validate() error {
	return firstError([]error{
		requiredID("installation ID", input.InstallationID),
		requiredID("organization ID", input.OrganizationID),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	})
}

type CreateSlackInstallStateInput struct {
	StateHash      []byte
	OrganizationID domain.ID
	MembershipID   domain.ID
	ExpiresAt      time.Time
	Now            time.Time
}

func (input CreateSlackInstallStateInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("membership ID", input.MembershipID),
		requiredTime("state expiry time", input.ExpiresAt),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if len(input.StateHash) == 0 {
		return invalidField("state hash", "must not be empty")
	}
	return nil
}

type ConsumeSlackInstallStateInput struct {
	StateHash []byte
	Now       time.Time
}

func (input ConsumeSlackInstallStateInput) Validate() error {
	if len(input.StateHash) == 0 {
		return invalidField("state hash", "must not be empty")
	}
	return requiredTime("current time", input.Now)
}

type SlackInstallState struct {
	OrganizationID domain.ID
	MembershipID   domain.ID
}
