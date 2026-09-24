package authn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

// requireDirectoryGroupAccess gates the directory group mapping surface:
// reading needs connection.read, changing a grant needs connection.write,
// and both need the role repository the grants are validated against.
func (s *Service) requireDirectoryGroupAccess(principal domain.Principal, permission domain.Permission) error {
	if err := requireHuman(principal, permission, domain.OrganizationResource()); err != nil {
		return err
	}
	if s.roles == nil {
		return ErrEnterpriseNotConfigured
	}
	return nil
}

// ListDirectoryGroupLinks lists every provider group the directory has
// linked to a team, with the grant the team holds because of it.
func (s *Service) ListDirectoryGroupLinks(ctx context.Context, principal domain.Principal) ([]domain.DirectoryGroupLink, error) {
	if err := s.requireDirectoryGroupAccess(principal, domain.PermissionConnectionRead); err != nil {
		return nil, err
	}
	return s.roles.ListDirectoryGroupLinks(ctx, application.ListDirectoryGroupLinksInput{OrganizationID: principal.OrganizationID})
}

// SetDirectoryGroupGrant maps a provider group to a grant held by the team
// it fills. The grant is unattended: it takes effect for everyone the
// directory places in the group later, with nobody confirming, so it
// passes the kernel with that flag and can never carry a dangerous
// permission.
func (s *Service) SetDirectoryGroupGrant(ctx context.Context, principal domain.Principal, providerObjectID string, spec domain.AssignmentSpec) error {
	if err := s.requireDirectoryGroupAccess(principal, domain.PermissionConnectionWrite); err != nil {
		return err
	}
	if err := s.AuthorizeUnattendedGrant(ctx, principal, spec); err != nil {
		return err
	}
	return s.roles.SetDirectoryGroupGrant(ctx, application.SetDirectoryGroupGrantInput{
		OrganizationID: principal.OrganizationID, ProviderObjectID: strings.TrimSpace(providerObjectID),
		Grant: &spec, ActorMembershipID: principal.MembershipID, Now: s.clock.Now(),
	})
}

// ClearDirectoryGroupGrant removes the grant a provider group confers;
// the team link itself stays, since the directory owns it.
func (s *Service) ClearDirectoryGroupGrant(ctx context.Context, principal domain.Principal, providerObjectID string) error {
	if err := s.requireDirectoryGroupAccess(principal, domain.PermissionConnectionWrite); err != nil {
		return err
	}
	return s.roles.SetDirectoryGroupGrant(ctx, application.SetDirectoryGroupGrantInput{
		OrganizationID: principal.OrganizationID, ProviderObjectID: strings.TrimSpace(providerObjectID),
		ActorMembershipID: principal.MembershipID, Now: s.clock.Now(),
	})
}

// AuthorizeUnattendedGrant runs the grant kernel for a role nobody will
// be present to confirm when it takes effect: a directory group mapping,
// or an edition's default role for unattended admission. The actor's
// ownership is read from their current assignments, never the session.
func (s *Service) AuthorizeUnattendedGrant(ctx context.Context, principal domain.Principal, spec domain.AssignmentSpec) error {
	if s.roles == nil {
		return ErrEnterpriseNotConfigured
	}
	role, err := s.roles.GetRole(ctx, application.GetRoleInput{OrganizationID: principal.OrganizationID, RoleID: spec.RoleID})
	if errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("role %s does not exist: %w", spec.RoleID, domain.ErrInvalid)
	}
	if err != nil {
		return err
	}
	actor, err := s.identities.GetMembership(ctx, application.GetMembershipInput{
		OrganizationID: principal.OrganizationID, MembershipID: principal.MembershipID,
	})
	if err != nil {
		return err
	}
	if rejection := authorization.EvaluateGrant(authorization.GrantInput{
		Actor: principal, ActorIsOwner: domain.HoldsRoleEverywhere(actor.Assignments, domain.RoleOwner),
		Role: role, Scope: spec.Scope(), Unattended: true,
	}); rejection != nil {
		return rejection.Err
	}
	return nil
}
