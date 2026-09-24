package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// ContactMethodRepository owns the addresses a person adds beyond their
// account's own email. Everything here is scoped by user, never by
// organization: a person has one personal mailbox across every
// organization they review for.
type ContactMethodRepository interface {
	ListContactMethods(ctx context.Context, input ListContactMethodsInput) ([]domain.ContactMethod, error)
	// LinkOrganizationContactMethod records or refreshes one of the
	// person's organization-scoped chat identities, such as their Slack or
	// Discord account. It is verified at link time: the provider vouched
	// for the account, so there is no code to answer.
	LinkOrganizationContactMethod(ctx context.Context, input LinkOrganizationContactMethodInput) (domain.ContactMethod, error)
	CreateContactMethod(ctx context.Context, input CreateContactMethodInput) (domain.ContactMethod, error)
	// CreateVouchedContactMethod records or refreshes a person-wide method
	// that needs no code: something other than its owner already vouched
	// for it (the session that registered an installed app). Verified on
	// insert; adding the same value again refreshes the label.
	CreateVouchedContactMethod(ctx context.Context, input CreateVouchedContactMethodInput) (domain.ContactMethod, error)
	SetContactMethodChallenge(ctx context.Context, input SetContactMethodChallengeInput) (domain.ContactMethod, error)
	VerifyContactMethod(ctx context.Context, input VerifyContactMethodInput) (domain.ContactMethod, error)
	// VerifyContactMethodByValue answers a challenge from the address side
	// rather than the owner's: a message arrives at one of the
	// deployment's numbers carrying a code, and whichever pending method
	// with that value holds the matching code is verified. Every other
	// pending method with the value spends an attempt.
	VerifyContactMethodByValue(ctx context.Context, input VerifyContactMethodByValueInput) (domain.ContactMethod, error)
	DeleteContactMethod(ctx context.Context, input DeleteContactMethodInput) error
	// ListMemberAddresses lists every active member of an organization with
	// the addresses that could identify them in a workspace: the account
	// email plus confirmed contact emails.
	ListMemberAddresses(ctx context.Context, input ListMemberAddressesInput) ([]MemberAddresses, error)
}

type ListMemberAddressesInput struct {
	OrganizationID domain.ID
	// UserID, when set, narrows the list to that one member.
	UserID domain.ID
}

func (input ListMemberAddressesInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}

type MemberAddresses struct {
	UserID       domain.ID
	MembershipID domain.ID
	DisplayName  string
	Emails       []string
}

// ListContactMethodsInput lists a person's person-wide methods plus the
// organization-scoped ones for OrganizationID, when given.
type ListContactMethodsInput struct {
	UserID         domain.ID
	OrganizationID domain.ID
}

func (input ListContactMethodsInput) Validate() error {
	return requiredID("user ID", input.UserID)
}

// LinkOrganizationContactMethodInput names the external account to link.
// ExternalID is the provider's opaque user id; Label is what people see.
type LinkOrganizationContactMethodInput struct {
	SlackInstallationID domain.ID
	MethodID            domain.ID
	UserID              domain.ID
	OrganizationID      domain.ID
	Kind                domain.ContactMethodKind
	ExternalID          string
	Label               string
	Now                 time.Time
}

func (input LinkOrganizationContactMethodInput) Validate() error {
	if err := firstError([]error{
		requiredID("contact method ID", input.MethodID),
		requiredID("user ID", input.UserID),
		requiredID("organization ID", input.OrganizationID),
		requiredString("external user ID", input.ExternalID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if !input.Kind.OrganizationScoped() {
		return invalidField("contact method kind", "is not an organization-scoped chat kind")
	}
	return nil
}

type CreateContactMethodInput struct {
	MethodID domain.ID
	UserID   domain.ID
	Kind     domain.ContactMethodKind
	Value    string
	// CodeHash and ExpiresAt are the verification challenge the address
	// must answer before anything is delivered to it.
	CodeHash  []byte
	ExpiresAt time.Time
	Now       time.Time
}

func (input CreateContactMethodInput) Validate() error {
	if err := firstError([]error{
		requiredID("contact method ID", input.MethodID),
		requiredID("user ID", input.UserID),
		requiredString("contact method value", input.Value),
		requiredTime("verification expiry time", input.ExpiresAt),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if !input.Kind.Challengeable() {
		return invalidField("contact method kind", "is not proved by a code")
	}
	if len(input.CodeHash) == 0 {
		return invalidField("verification code hash", "must not be empty")
	}
	return nil
}

// SetContactMethodChallengeInput replaces the live challenge, which is how
// a resend works. Rotating invalidates the previous code.
type SetContactMethodChallengeInput struct {
	MethodID  domain.ID
	UserID    domain.ID
	CodeHash  []byte
	ExpiresAt time.Time
	Now       time.Time
}

func (input SetContactMethodChallengeInput) Validate() error {
	if err := firstError([]error{
		requiredID("contact method ID", input.MethodID),
		requiredID("user ID", input.UserID),
		requiredTime("verification expiry time", input.ExpiresAt),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if len(input.CodeHash) == 0 {
		return invalidField("verification code hash", "must not be empty")
	}
	return nil
}

// VerifyContactMethodInput answers a live challenge. The comparison
// happens in the adapter so the attempt counter and the check share one
// transaction.
type VerifyContactMethodInput struct {
	MethodID domain.ID
	UserID   domain.ID
	CodeHash []byte
	// MaxAttempts bounds guessing against one challenge.
	MaxAttempts int64
	Now         time.Time
}

type VerifyContactMethodByValueInput struct {
	Kind     domain.ContactMethodKind
	Value    string
	CodeHash []byte
	// MaxAttempts bounds guessing against one challenge.
	MaxAttempts int64
	Now         time.Time
}

func (input VerifyContactMethodByValueInput) Validate() error {
	if err := firstError([]error{
		requiredString("contact method value", input.Value),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if !input.Kind.Challengeable() {
		return invalidField("contact method kind", "is not proved by a code")
	}
	if len(input.CodeHash) == 0 {
		return invalidField("verification code hash", "must not be empty")
	}
	if input.MaxAttempts <= 0 {
		return invalidField("maximum verification attempts", "must be positive")
	}
	return nil
}

func (input VerifyContactMethodInput) Validate() error {
	if err := firstError([]error{
		requiredID("contact method ID", input.MethodID),
		requiredID("user ID", input.UserID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if len(input.CodeHash) == 0 {
		return invalidField("verification code hash", "must not be empty")
	}
	if input.MaxAttempts <= 0 {
		return invalidField("maximum verification attempts", "must be positive")
	}
	return nil
}

type DeleteContactMethodInput struct {
	MethodID domain.ID
	UserID   domain.ID
}

func (input DeleteContactMethodInput) Validate() error {
	return firstError([]error{
		requiredID("contact method ID", input.MethodID),
		requiredID("user ID", input.UserID),
	})
}

type CreateVouchedContactMethodInput struct {
	MethodID domain.ID
	UserID   domain.ID
	Kind     domain.ContactMethodKind
	Value    string
	Label    string
	Now      time.Time
}

func (input CreateVouchedContactMethodInput) Validate() error {
	if input.Kind.OrganizationScoped() || input.Kind.Challengeable() {
		return invalidField("contact method kind", "must be a vouched, person-wide kind")
	}
	if !input.Kind.Known() {
		return invalidField("contact method kind", "is not a kind this deployment knows")
	}
	return firstError([]error{
		requiredID("contact method ID", input.MethodID),
		requiredID("user ID", input.UserID),
		requiredString("contact method value", input.Value),
		requiredTime("current time", input.Now),
	})
}

// ContactKindAdapter is how an edition's contact kind behaves in the
// contacts service: what the person types becomes the stored form, how
// a challenge reaches them, and what may stop a code from going out. The
// kind itself is registered with domain.RegisterContactKinds; a kind
// without an adapter cannot be added.
type ContactKindAdapter struct {
	Kind domain.ContactMethodKind
	// Normalize turns what the person typed into the stored form, refusing
	// a value the deployment could never deliver a code to.
	Normalize func(value string) (string, error)
	// DeliverCode gets the challenge to the person and returns the fields
	// the challenge response carries beside the method (a click-to-chat
	// confirmation link, say), or none.
	DeliverCode func(ctx context.Context, method domain.ContactMethod, code string, expiresAt time.Time) (map[string]string, error)
	// CodeBudget refuses a code the person may not ask for yet: a cooldown
	// on the number, a daily allowance. MethodID is empty when the value
	// is being added. Nil means no budget.
	CodeBudget func(ctx context.Context, userID, methodID domain.ID, now time.Time) error
}
