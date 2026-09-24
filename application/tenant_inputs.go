package application

import (
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// ActivateOrganizationInput creates an organization and its founding owner
// in one transaction. The email must already be verified by the caller: the
// bootstrap ceremony trusts the operator, the hosted signup flow verifies
// by email round-trip or through an identity provider that verified it.
// UserID and MembershipID are the identifiers to use when the user does
// not exist yet; an existing user keeps their identity and credentials and
// simply gains the organization. An existing user is matched first by the
// provider identity triple when one is given, then by email.
type ActivateOrganizationInput struct {
	OrganizationID   domain.ID
	OrganizationSlug string
	OrganizationName string
	UserID           domain.ID
	MembershipID     domain.ID
	DisplayName      string
	Email            string
	// PasswordHash is optional: empty when the founder is an existing user
	// or authenticates through a provider.
	PasswordHash string
	// ProviderKey, Issuer, and Subject are the founder's provider identity
	// when an identity provider verified the email. All three are set or
	// all empty; when set, the founder gets that identity instead of the
	// local one and never a password.
	ProviderKey     string
	Issuer          string
	Subject         string
	AdmissionSource domain.AdmissionSource
	Now             time.Time
}

// HasProviderIdentity reports whether the founder authenticates through a
// provider identity rather than a local password.
func (input ActivateOrganizationInput) HasProviderIdentity() bool {
	return input.ProviderKey != "" || input.Issuer != "" || input.Subject != ""
}

func (input ActivateOrganizationInput) Validate() error {
	checks := []error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("organization slug", input.OrganizationSlug),
		requiredString("organization name", input.OrganizationName),
		requiredID("user ID", input.UserID),
		requiredID("membership ID", input.MembershipID),
		requiredString("display name", input.DisplayName),
		requiredString("email", input.Email),
		requiredTime("current time", input.Now),
	}
	if err := firstError(checks); err != nil {
		return err
	}
	if input.AdmissionSource != domain.AdmissionManual && input.AdmissionSource != domain.AdmissionSignup {
		return invalidField("admission source", "must be manual or signup")
	}
	if input.HasProviderIdentity() {
		if input.ProviderKey == "" || input.Issuer == "" || input.Subject == "" {
			return invalidField("provider identity", "requires provider key, issuer, and subject together")
		}
		if input.PasswordHash != "" {
			return invalidField("password hash", "must be empty for a provider-verified founder")
		}
	}
	return nil
}

// ActivationResult carries the created organization together with the
// founding owner's principal, ready for session issuance.
type ActivationResult struct {
	Organization domain.Organization
	Principal    domain.Principal
}

// SetOrganizationStatusInput moves an organization through the lifecycle
// state machine. The adapter enforces the legal transitions: suspended is
// reachable from active, pending_deletion from active or suspended, and
// active from suspended or pending_deletion.
type SetOrganizationStatusInput struct {
	OrganizationID domain.ID
	Status         domain.OrganizationStatus
	Reason         string
	// DeletionScheduledFor is required when Status is pending_deletion.
	DeletionScheduledFor *time.Time
	// ActorType and ActorID attribute the audit event. ActorType is
	// "human" for an operator action and "system" for automated policy
	// such as dunning; ActorID may be empty for system actors.
	ActorType string
	ActorID   domain.ID
	Now       time.Time
}

func (input SetOrganizationStatusInput) Validate() error {
	checks := []error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("actor type", input.ActorType),
		requiredTime("current time", input.Now),
	}
	if err := firstError(checks); err != nil {
		return err
	}
	if !domain.ValidOrganizationStatus(input.Status) {
		return invalidField("organization status", "must be active, suspended, or pending_deletion")
	}
	if input.ActorType != "human" && input.ActorType != "system" {
		return invalidField("actor type", "must be human or system")
	}
	if input.Status == domain.OrganizationPendingDeletion && (input.DeletionScheduledFor == nil || input.DeletionScheduledFor.IsZero()) {
		return invalidField("deletion schedule", "is required for pending_deletion")
	}
	return nil
}

// PurgeOrganizationInput permanently deletes every organization-scoped row.
// The adapter refuses unless the organization is pending_deletion with a
// schedule at or before Now, so a cancellation that raced the purge
// workflow results in a no-op instead of data loss.
type PurgeOrganizationInput struct {
	OrganizationID domain.ID
	Now            time.Time
}

func (input PurgeOrganizationInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredTime("current time", input.Now),
	})
}

type StartOrganizationDeletionInput struct {
	OrganizationID domain.ID
	PurgeAt        time.Time
}

func (input StartOrganizationDeletionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredTime("purge time", input.PurgeAt),
	})
}

type CancelOrganizationDeletionInput struct {
	OrganizationID domain.ID
}

func (input CancelOrganizationDeletionInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}
