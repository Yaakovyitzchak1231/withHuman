package application

import (
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// validAssignmentSpecs checks the shape of a grant list: at least one,
// a role id on each, a valid scope kind, an id exactly when the scope is
// not the organization, and no duplicates. Whether the role exists and
// is bindable at that scope is the repository's and the grant kernel's.
func validAssignmentSpecs(field string, specs []domain.AssignmentSpec) error {
	if len(specs) == 0 {
		return invalidField(field, "must contain at least one grant")
	}
	seen := make(map[domain.AssignmentSpec]struct{}, len(specs))
	for _, spec := range specs {
		if spec.RoleID == "" {
			return invalidField(field, "every grant needs a role id")
		}
		if !domain.ValidScopeKind(spec.ScopeKind) {
			return invalidField(field, "scope kind must be organization, team, or agent_slug")
		}
		if (spec.ScopeKind == domain.ScopeOrganization) != (spec.ScopeID == "") {
			return invalidField(field, "scope id is required exactly when the scope is a team or agent type")
		}
		if spec.ScopeKind == domain.ScopeAgent && !domain.AgentSlug(spec.ScopeID).Valid() {
			return invalidField(field, "agent type scope id is malformed")
		}
		if _, duplicate := seen[spec]; duplicate {
			return invalidField(field, "must not repeat a grant")
		}
		seen[spec] = struct{}{}
	}
	return nil
}

type CreateInvitationInput struct {
	OrganizationID        domain.ID
	InvitationID          domain.ID
	Email                 string
	Grants                []domain.AssignmentSpec
	InvitedByMembershipID domain.ID
	TokenHash             []byte
	ExpiresAt             time.Time
	// SeatQuota is enforced inside the creating transaction: a pending
	// invitation reserves a seat.
	SeatQuota AllocationQuota
	Now       time.Time
}

func (input CreateInvitationInput) Validate() error {
	checks := []error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("invitation ID", input.InvitationID),
		requiredString("email", input.Email),
		validAssignmentSpecs("invitation grants", input.Grants),
		requiredID("inviting membership ID", input.InvitedByMembershipID),
		requiredTokenHash("invitation token hash", input.TokenHash),
		requiredTime("invitation expiry time", input.ExpiresAt),
		input.SeatQuota.Validate(),
		requiredTime("current time", input.Now),
	}
	if err := firstError(checks); err != nil {
		return err
	}
	if !input.ExpiresAt.After(input.Now) {
		return invalidField("invitation expiry time", "must be in the future")
	}
	return nil
}

type ListInvitationsInput struct {
	OrganizationID domain.ID
	Limit          int
}

func (input ListInvitationsInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		nonNegative("limit", int64(input.Limit)),
	})
}

type RotateInvitationTokenInput struct {
	OrganizationID domain.ID
	InvitationID   domain.ID
	TokenHash      []byte
	ExpiresAt      time.Time
	// SeatQuota is enforced when the rotation revives an expired
	// invitation, which reserves a seat again; a live one already holds
	// its seat.
	SeatQuota AllocationQuota
	Now       time.Time
}

func (input RotateInvitationTokenInput) Validate() error {
	checks := []error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("invitation ID", input.InvitationID),
		requiredTokenHash("invitation token hash", input.TokenHash),
		requiredTime("invitation expiry time", input.ExpiresAt),
		input.SeatQuota.Validate(),
		requiredTime("current time", input.Now),
	}
	if err := firstError(checks); err != nil {
		return err
	}
	if !input.ExpiresAt.After(input.Now) {
		return invalidField("invitation expiry time", "must be in the future")
	}
	return nil
}

type RevokeInvitationInput struct {
	OrganizationID domain.ID
	InvitationID   domain.ID
	Now            time.Time
}

func (input RevokeInvitationInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("invitation ID", input.InvitationID),
		requiredTime("current time", input.Now),
	})
}

type GetInvitationByTokenInput struct {
	InvitationID domain.ID
	TokenHash    []byte
	Now          time.Time
}

func (input GetInvitationByTokenInput) Validate() error {
	return firstError([]error{
		requiredID("invitation ID", input.InvitationID),
		requiredTokenHash("invitation token hash", input.TokenHash),
		requiredTime("current time", input.Now),
	})
}

// InvitationPreview is everything the public accept page needs before the
// invitee authenticates.
type InvitationPreview struct {
	Invitation         domain.Invitation
	OrganizationName   string
	OrganizationSlug   string
	InviterDisplayName string
	UserExists         bool
}

type FindPendingInvitationByEmailInput struct {
	OrganizationID domain.ID
	Email          string
	Now            time.Time
}

func (input FindPendingInvitationByEmailInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("email", input.Email),
		requiredTime("current time", input.Now),
	})
}

// AcceptInvitationInput runs the admission transaction. UserID names an
// existing user; when it is empty a new user is created as NewUserID with
// DisplayName, the invitation email, and, when PasswordHash is set, a local
// credential. TokenHash is verified when present; the SSO admission path
// omits it because the verified email match is the capability there. The
// external identity (ProviderKey, Issuer, Subject) is the durable link
// written for the admitted user; it is skipped when ProviderKey is empty
// (session-based acceptance by an already linked user). AssertedEmail is
// the authenticated identity's email and must match the invitation email.
// GetInvitationInput reads one invitation of the organization by id.
type GetInvitationInput struct {
	OrganizationID domain.ID
	InvitationID   domain.ID
}

func (input GetInvitationInput) Validate() error {
	return firstError([]error{requiredID("organization ID", input.OrganizationID), requiredID("invitation ID", input.InvitationID)})
}

type AcceptInvitationInput struct {
	InvitationID  domain.ID
	TokenHash     []byte
	AssertedEmail string
	UserID        domain.ID
	NewUserID     domain.ID
	// LinkExistingUser lets the acceptance attach an existing global user
	// found by the invitation email when UserID is empty. Only an
	// enterprise assertion for an email domain the organization itself
	// verified may set it; every other unauthenticated acceptance refuses
	// with ErrUserExists, since the token alone does not prove the
	// account is the acceptor's.
	LinkExistingUser bool
	DisplayName      string
	PasswordHash     string
	ProviderKey      string
	Issuer           string
	Subject          string
	MembershipID     domain.ID
	// SeatQuota is re-checked at acceptance because the quota may have
	// shrunk since the invitation was sent. The accepted invitation's own
	// reservation is not counted against it.
	SeatQuota AllocationQuota
	Now       time.Time
}

func (input AcceptInvitationInput) Validate() error {
	checks := []error{
		requiredID("invitation ID", input.InvitationID),
		requiredString("asserted email", input.AssertedEmail),
		requiredID("membership ID", input.MembershipID),
		input.SeatQuota.Validate(),
		requiredTime("current time", input.Now),
	}
	if err := firstError(checks); err != nil {
		return err
	}
	if len(input.TokenHash) > 0 {
		if err := requiredTokenHash("invitation token hash", input.TokenHash); err != nil {
			return err
		}
	}
	if input.ProviderKey != "" {
		if err := requiredString("issuer", input.Issuer); err != nil {
			return err
		}
	}
	if input.UserID == "" {
		return firstError([]error{
			requiredID("new user ID", input.NewUserID),
			requiredString("display name", input.DisplayName),
			requiredString("provider key", input.ProviderKey),
		})
	}
	return nil
}

type ListMembersInput struct {
	OrganizationID domain.ID
	Limit          int
}

func (input ListMembersInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		nonNegative("limit", int64(input.Limit)),
	})
}

// MemberRecord joins a membership with the user identity the management
// surface displays.
type MemberRecord struct {
	Membership  domain.Membership
	DisplayName string
	Email       string
}

type SetMembershipStatusInput struct {
	OrganizationID domain.ID
	MembershipID   domain.ID
	Status         domain.MembershipStatus
	RevokeSessions bool
	// Force suspends or deprovisions the member even while an active
	// escalation path revision targets them; the actor accepts that those
	// targets no longer reach anyone. Without it the write is refused
	// with MembershipInEscalationPathError. Reactivation never consults it.
	Force             bool
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input SetMembershipStatusInput) Validate() error {
	checks := []error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("membership ID", input.MembershipID),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	}
	if err := firstError(checks); err != nil {
		return err
	}
	switch input.Status {
	case domain.MembershipActive, domain.MembershipSuspended, domain.MembershipDeprovisioned:
		return nil
	}
	return invalidField("membership status", "must be active, suspended, or deprovisioned")
}

type FindOrganizationBySlugInput struct {
	OrganizationSlug string
}

func (input FindOrganizationBySlugInput) Validate() error {
	return requiredString("organization slug", input.OrganizationSlug)
}

type GetOrganizationInput struct {
	OrganizationID domain.ID
}

func (input GetOrganizationInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}

// ResolveExternalIdentityInput looks up the user linked to a provider
// assertion and records the observation.
type ResolveExternalIdentityInput struct {
	ProviderKey   string
	Issuer        string
	Subject       string
	ObservedEmail string
	Now           time.Time
}

func (input ResolveExternalIdentityInput) Validate() error {
	return firstError([]error{
		requiredString("provider key", input.ProviderKey),
		requiredString("issuer", input.Issuer),
		requiredString("subject", input.Subject),
		requiredTime("current time", input.Now),
	})
}
