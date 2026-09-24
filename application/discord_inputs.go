package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// DiscordConnector speaks Discord's OAuth2 surface with the application
// credentials. Two grants pass through it: an organization install, which
// adds the deployment's bot to a guild, and a person's identity link, which
// only proves who they are. The bot token is adapter configuration and is
// never an input here.
type DiscordConnector interface {
	// AuthorizeInstallURL is where an installing admin is sent: the bot
	// scope with no guild permissions, since direct messages need none.
	AuthorizeInstallURL(state, redirectURL string) string
	// AuthorizeLinkURL is where a person linking their account is sent:
	// the identify scope alone.
	AuthorizeLinkURL(state, redirectURL string) string
	// ExchangeInstall trades an install callback code for the guild the
	// bot joined.
	ExchangeInstall(ctx context.Context, input DiscordExchangeInput) (DiscordInstallation, error)
	// IdentifyUser trades a link callback code for the account behind it.
	// The user token it briefly holds is discarded, never stored.
	IdentifyUser(ctx context.Context, input DiscordExchangeInput) (DiscordUser, error)
	// LeaveGuild removes the bot from a guild when the organization
	// disconnects. A guild the bot is no longer in is not an error.
	LeaveGuild(ctx context.Context, guildID string) error
}

// DiscordMessenger speaks the bot-token parts of Discord: confirming that
// a person shares a guild with the bot, and reaching them directly.
type DiscordMessenger interface {
	// GetGuildMember returns the member, or ErrNotFound when the person is
	// not in the guild. A bot can only message people it shares a guild
	// with, so this is checked before a link is recorded.
	GetGuildMember(ctx context.Context, input DiscordGuildMemberInput) (DiscordUser, error)
	// SendDirectMessage delivers one message to the person's DM with the bot.
	SendDirectMessage(ctx context.Context, input DiscordDirectMessageInput) error
}

// DiscordUser is a Discord account. ID is the snowflake, a string
// everywhere because it exceeds what JSON numbers carry safely.
type DiscordUser struct {
	ID          string
	Username    string
	DisplayName string
}

// DiscordInstallation is what an accepted install yields: the guild the
// bot was added to.
type DiscordInstallation struct {
	GuildID   string
	GuildName string
}

type DiscordExchangeInput struct {
	Code        string
	RedirectURL string
}

func (input DiscordExchangeInput) Validate() error {
	return firstError([]error{
		requiredString("authorization code", input.Code),
		requiredString("redirect URL", input.RedirectURL),
	})
}

type DiscordGuildMemberInput struct {
	GuildID string
	UserID  string
}

func (input DiscordGuildMemberInput) Validate() error {
	return firstError([]error{
		requiredString("Discord guild ID", input.GuildID),
		requiredString("Discord user ID", input.UserID),
	})
}

// DiscordDirectMessageInput carries a rendered message. Content is the
// plain text Discord shows in notifications; Embeds and Components are
// already serialized by the channel that knows the message's shape.
type DiscordDirectMessageInput struct {
	DiscordUserID string
	Content       string
	Embeds        json.RawMessage
	Components    json.RawMessage
}

func (input DiscordDirectMessageInput) Validate() error {
	if err := firstError([]error{
		requiredString("Discord user ID", input.DiscordUserID),
		requiredString("message content", input.Content),
	}); err != nil {
		return err
	}
	if err := optionalJSON("message embeds", input.Embeds); err != nil {
		return err
	}
	return optionalJSON("message components", input.Components)
}

// DiscordOAuthPurpose names which round trip a state was minted for. It is
// part of redemption, so a state minted by any member to link their own
// account can never complete an organization install.
type DiscordOAuthPurpose string

const (
	DiscordPurposeInstall DiscordOAuthPurpose = "install"
	DiscordPurposeLink    DiscordOAuthPurpose = "link"
)

func (purpose DiscordOAuthPurpose) Valid() bool {
	return purpose == DiscordPurposeInstall || purpose == DiscordPurposeLink
}

// DiscordConnectionRepository owns the per-organization guild row and the
// short-lived OAuth states that bind a callback to whoever began it.
type DiscordConnectionRepository interface {
	GetDiscordConnection(ctx context.Context, input GetDiscordConnectionInput) (domain.DiscordConnection, error)
	UpsertDiscordConnection(ctx context.Context, input UpsertDiscordConnectionInput) (domain.DiscordConnection, error)
	// DeleteDiscordConnection removes the row and every member's Discord
	// link in the organization, and returns the guild id so the caller can
	// make the bot leave it.
	DeleteDiscordConnection(ctx context.Context, input DeleteDiscordConnectionInput) (string, error)
	CreateDiscordOAuthState(ctx context.Context, input CreateDiscordOAuthStateInput) error
	// ConsumeDiscordOAuthState redeems a state exactly once, for the
	// purpose it was minted for, and returns who began the flow, or
	// ErrNotFound for an unknown, expired, already used, or mismatched
	// state.
	ConsumeDiscordOAuthState(ctx context.Context, input ConsumeDiscordOAuthStateInput) (DiscordOAuthState, error)
}

type GetDiscordConnectionInput struct {
	OrganizationID domain.ID
}

func (input GetDiscordConnectionInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}

type UpsertDiscordConnectionInput struct {
	OrganizationID          domain.ID
	Installation            DiscordInstallation
	InstalledByMembershipID domain.ID
	Now                     time.Time
}

func (input UpsertDiscordConnectionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("Discord guild ID", input.Installation.GuildID),
		requiredString("Discord guild name", input.Installation.GuildName),
		requiredID("installing membership ID", input.InstalledByMembershipID),
		requiredTime("current time", input.Now),
	})
}

type DeleteDiscordConnectionInput struct {
	OrganizationID    domain.ID
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input DeleteDiscordConnectionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	})
}

type CreateDiscordOAuthStateInput struct {
	StateHash      []byte
	Purpose        DiscordOAuthPurpose
	OrganizationID domain.ID
	MembershipID   domain.ID
	UserID         domain.ID
	ExpiresAt      time.Time
	Now            time.Time
}

func (input CreateDiscordOAuthStateInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("membership ID", input.MembershipID),
		requiredID("user ID", input.UserID),
		requiredTime("state expiry time", input.ExpiresAt),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if len(input.StateHash) == 0 {
		return invalidField("state hash", "must not be empty")
	}
	if !input.Purpose.Valid() {
		return invalidField("state purpose", "must be install or link")
	}
	return nil
}

type ConsumeDiscordOAuthStateInput struct {
	StateHash []byte
	Purpose   DiscordOAuthPurpose
	Now       time.Time
}

func (input ConsumeDiscordOAuthStateInput) Validate() error {
	if len(input.StateHash) == 0 {
		return invalidField("state hash", "must not be empty")
	}
	if !input.Purpose.Valid() {
		return invalidField("state purpose", "must be install or link")
	}
	return requiredTime("current time", input.Now)
}

type DiscordOAuthState struct {
	OrganizationID domain.ID
	MembershipID   domain.ID
	UserID         domain.ID
}
