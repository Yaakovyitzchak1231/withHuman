// Package orgsettings owns the organization policy surface: the approval
// knobs an administrator reads and changes on the settings page. The
// policy is one row per organization; reading it needs organization.read
// and changing it organization.settings.write, both at organization
// scope. The repository audits every change as organization.policy_updated.
package orgsettings

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

// MaximumClaimRetriggerWindow caps how long a claim may hold before it
// lapses; a claim that outlives a day is a forgotten request, not a
// review in progress.
const MaximumClaimRetriggerWindow = 24 * time.Hour

type Service struct {
	identities application.IdentityRepository
	clock      application.Clock
	logger     *slog.Logger
}

func NewService(identities application.IdentityRepository, clock application.Clock, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{identities: identities, clock: clock, logger: logger}
}

// PolicyPatch is a partial update: a nil field keeps the stored value.
type PolicyPatch struct {
	MFAMode                    *domain.MFAMode
	ApprovalReauthWindow       *time.Duration
	ClaimRetriggerWindow       *time.Duration
	UnroutedRequests           *domain.UnroutedRequestsPolicy
	RequestVisibility          *domain.RequestVisibilityPolicy
	PersonalAPIKeysAllowed     *bool
	OrganizationAPIKeysAllowed *bool
}

func (s *Service) require(principal domain.Principal, permission domain.Permission) error {
	if !domain.ManagementPrincipal(principal) {
		return domain.ErrForbidden
	}
	return authorization.Check(principal, permission, domain.OrganizationResource())
}

// Policy reads the organization policy.
func (s *Service) Policy(ctx context.Context, principal domain.Principal) (domain.OrganizationPolicy, error) {
	if err := s.require(principal, domain.PermissionOrganizationRead); err != nil {
		return domain.OrganizationPolicy{}, err
	}
	return s.identities.GetOrganizationPolicy(ctx, application.GetOrganizationPolicyInput{OrganizationID: principal.OrganizationID})
}

// UpdatePolicy applies the set fields of the patch over the stored
// policy, validates the result, and stores it. Validation runs here so a
// bad value answers invalid rather than reaching the database.
func (s *Service) UpdatePolicy(ctx context.Context, principal domain.Principal, patch PolicyPatch) (domain.OrganizationPolicy, error) {
	if err := s.require(principal, domain.PermissionOrganizationSettingsWrite); err != nil {
		return domain.OrganizationPolicy{}, err
	}
	current, err := s.identities.GetOrganizationPolicy(ctx, application.GetOrganizationPolicyInput{OrganizationID: principal.OrganizationID})
	if err != nil {
		return domain.OrganizationPolicy{}, err
	}
	input := application.UpdateOrganizationPolicyInput{
		OrganizationID:             principal.OrganizationID,
		MFAMode:                    current.MFAMode,
		ApprovalReauthWindow:       current.ApprovalReauthWindow,
		ClaimRetriggerWindow:       current.ClaimRetriggerWindow,
		UnroutedRequests:           current.UnroutedRequests,
		RequestVisibility:          current.RequestVisibility,
		PersonalAPIKeysAllowed:     current.PersonalAPIKeysAllowed,
		OrganizationAPIKeysAllowed: current.OrganizationAPIKeysAllowed,
		ActorMembershipID:          principal.MembershipID,
		Now:                        s.clock.Now(),
	}
	if patch.MFAMode != nil {
		input.MFAMode = *patch.MFAMode
	}
	if patch.ApprovalReauthWindow != nil {
		input.ApprovalReauthWindow = *patch.ApprovalReauthWindow
	}
	if patch.ClaimRetriggerWindow != nil {
		input.ClaimRetriggerWindow = *patch.ClaimRetriggerWindow
	}
	if patch.UnroutedRequests != nil {
		input.UnroutedRequests = *patch.UnroutedRequests
	}
	if patch.RequestVisibility != nil {
		input.RequestVisibility = *patch.RequestVisibility
	}
	if patch.PersonalAPIKeysAllowed != nil {
		input.PersonalAPIKeysAllowed = *patch.PersonalAPIKeysAllowed
	}
	if patch.OrganizationAPIKeysAllowed != nil {
		input.OrganizationAPIKeysAllowed = *patch.OrganizationAPIKeysAllowed
	}
	if err := input.Validate(); err != nil {
		return domain.OrganizationPolicy{}, err
	}
	if input.ClaimRetriggerWindow > MaximumClaimRetriggerWindow {
		return domain.OrganizationPolicy{}, fmt.Errorf("claim window must be at most %s: %w", MaximumClaimRetriggerWindow, domain.ErrInvalid)
	}
	updated, err := s.identities.UpdateOrganizationPolicy(ctx, input)
	if err != nil {
		return domain.OrganizationPolicy{}, err
	}
	s.logger.InfoContext(ctx, "organization policy updated",
		"organization_id", principal.OrganizationID, "membership_id", principal.MembershipID,
		"unrouted_requests", updated.UnroutedRequests, "request_visibility", updated.RequestVisibility)
	return updated, nil
}
