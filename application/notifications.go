package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// NotificationChannelKey identifies one delivery transport. Channels differ
// in credentials, retry policy, and licensing, so each is registered
// separately rather than hidden behind one adapter. An edition's channels
// name their own keys (the hosted sms, whatsapp and push channels).
type NotificationChannelKey string

const (
	NotificationChannelLog     NotificationChannelKey = "log"
	NotificationChannelEmail   NotificationChannelKey = "email"
	NotificationChannelSlack   NotificationChannelKey = "slack"
	NotificationChannelDiscord NotificationChannelKey = "discord"
)

// NotificationChannel delivers withHuman's two message families over one
// transport. Approval prompts ask a named reviewer to decide; transactional
// messages carry identity events such as invitations. A channel never
// decides who should be told: it receives resolved recipients.
type NotificationChannel interface {
	Key() NotificationChannelKey
	SendApprovalPrompt(ctx context.Context, input SendApprovalPromptInput) error
	SendTransactional(ctx context.Context, input SendTransactionalInput) error
}

// NotificationDispatcher fans a message out across every registered
// channel. Delivery never controls an approval outcome, so a channel
// failure is reported for operational signals and never propagated as a
// reason to fail the action that triggered it.
type NotificationDispatcher interface {
	SendApprovalPrompt(ctx context.Context, input SendApprovalPromptInput) error
	SendTransactional(ctx context.Context, input SendTransactionalInput) error
}

// NotificationRecipient is one resolved person to reach. MembershipID is
// empty for someone who is not a member yet, such as an invited email.
// Emails holds every verified address that person has: their account's own
// address plus any contact method they added, so a reviewer who wants
// approvals at a personal mailbox gets them at both.
type NotificationRecipient struct {
	MembershipID domain.ID
	DisplayName  string
	Emails       []string
	// SlackUserID is the person's identity in the organization's connected
	// workspace, empty when unlinked or when no workspace is connected.
	SlackUserID         string
	SlackInstallationID domain.ID
	SlackFallbackEmail  string
	SlackFallback       bool
	// DiscordUserID is the person's identity in the organization's connected
	// Discord server, empty when unlinked or when no server is connected.
	DiscordUserID string
	// Targets are this step's targets on the kinds an edition registered
	// (domain.RegisterContactKinds), keyed by kind: the contact methods
	// behind them and their addresses, so an edition's channel records a
	// send against the method without a plan ever carrying more than the
	// address, and resolves what it needs (a device token) itself.
	Targets map[domain.ContactMethodKind][]RecipientTarget
	// UserID identifies the person behind the membership, which is what
	// their notification rules hang off.
	UserID domain.ID
	// Methods is everything the person can be reached on, keyed for rules
	// to name. Emails, SlackUserID, and DiscordUserID above are the targets
	// of the current delivery; Methods is the full set the plan chooses from.
	Methods []RecipientMethod
}

// Reachable reports whether any channel could deliver to this person.
func (recipient NotificationRecipient) Reachable() bool {
	if len(recipient.Emails) > 0 || recipient.SlackUserID != "" || recipient.DiscordUserID != "" {
		return true
	}
	for _, targets := range recipient.Targets {
		if len(targets) > 0 {
			return true
		}
	}
	return false
}

// RecipientTarget is one contact method of an edition kind due on a step.
type RecipientTarget struct {
	MethodID domain.ID
	Address  string
}

// TargetsOf lists the step's targets on one kind.
func (recipient NotificationRecipient) TargetsOf(kind domain.ContactMethodKind) []RecipientTarget {
	return recipient.Targets[kind]
}

func (recipient NotificationRecipient) Validate() error {
	for _, address := range recipient.Emails {
		if err := requiredString("recipient email", address); err != nil {
			return err
		}
	}
	return nil
}

// SendApprovalPromptInput asks recipients to decide a pending request.
// RoutingOutcome records how the recipient set was chosen, so channels and
// operators can tell a targeted page from a default-queue broadcast.
type SendApprovalPromptInput struct {
	Request        domain.ApprovalRequest
	RoutingOutcome domain.RoutingOutcome
	Recipients     []NotificationRecipient
}

func (input SendApprovalPromptInput) Validate() error {
	if err := validateApprovalRequest(input.Request); err != nil {
		return err
	}
	for _, recipient := range input.Recipients {
		if err := recipient.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// TransactionalKind names an identity message. Transactional mail shares
// the channel registry with approval prompts rather than growing a
// parallel email path.
type TransactionalKind string

const (
	TransactionalInvitation          TransactionalKind = "invitation"
	TransactionalContactVerification TransactionalKind = "contact_verification"
	// TransactionalSignupVerification is the hosted edition's founder
	// vouch: the link proves the self-serve signup address before any
	// organization exists (design doc 07). Core renders it so the hosted
	// module never grows a parallel email path.
	TransactionalSignupVerification TransactionalKind = "signup_verification"
	// TransactionalPasswordReset carries the single-use link that sets a
	// new password. It goes only to the address on the account.
	TransactionalPasswordReset TransactionalKind = "password_reset"
)

// TransactionalMessage is one identity event to deliver.
//
// Every kind carries a credential. InviteURL and VerifyURL embed a
// one-time token and VerificationCode is the answer to a live challenge,
// so channels must deliver them only to the addressed recipient and must
// never log or persist them. Delivery is never the only path to an
// invitation, and never gates the action that produced the message.
type TransactionalMessage struct {
	Kind TransactionalKind
	// OrganizationID and OrganizationName identify the inviting
	// organization. A contact verification is user-scoped and carries
	// neither: proving an address is yours has nothing to do with which
	// organizations you review for. A signup verification carries only
	// the name, since the organization does not exist yet.
	OrganizationID   domain.ID
	OrganizationName string
	InviteURL        string
	// VerifyURL is the signup verification link.
	VerifyURL string
	// ResetURL is the password reset link.
	ResetURL         string
	VerificationCode string
	ExpiresAt        time.Time
}

func (message TransactionalMessage) Validate() error {
	switch message.Kind {
	case TransactionalInvitation:
		return firstError([]error{
			requiredID("organization ID", message.OrganizationID),
			requiredString("organization name", message.OrganizationName),
			requiredString("invite URL", message.InviteURL),
			requiredTime("invitation expiry time", message.ExpiresAt),
		})
	case TransactionalContactVerification:
		return firstError([]error{
			requiredString("verification code", message.VerificationCode),
			requiredTime("verification expiry time", message.ExpiresAt),
		})
	case TransactionalSignupVerification:
		return firstError([]error{
			requiredString("organization name", message.OrganizationName),
			requiredString("verify URL", message.VerifyURL),
			requiredTime("signup expiry time", message.ExpiresAt),
		})
	case TransactionalPasswordReset:
		return firstError([]error{
			requiredString("reset URL", message.ResetURL),
			requiredTime("reset expiry time", message.ExpiresAt),
		})
	default:
		return invalidField("transactional message kind", "is not a known message")
	}
}

// SendTransactionalInput addresses one message to one address. Unlike an
// approval prompt, which fans out to a person's whole set, a
// transactional message is answered by whoever holds that single mailbox
// or phone. Kind says which channel the address belongs to, so every
// channel can tell its own addresses from the others'; empty means email,
// which is what invitations and signup verifications always are.
// ContactMethodID is set for contact verifications, so a phone channel can
// record the send against the method it proves.
type SendTransactionalInput struct {
	Message         TransactionalMessage
	Recipient       NotificationRecipient
	Address         string
	Kind            domain.ContactMethodKind
	ContactMethodID domain.ID
}

func (input SendTransactionalInput) Validate() error {
	if err := input.Message.Validate(); err != nil {
		return err
	}
	if err := requiredString("recipient address", input.Address); err != nil {
		return err
	}
	if input.Kind != "" && !input.Kind.Challengeable() {
		return invalidField("transactional address kind", "is not a code-proved kind")
	}
	return input.Recipient.Validate()
}
