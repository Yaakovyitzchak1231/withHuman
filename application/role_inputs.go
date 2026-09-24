package application

import (
	"context"
	"slices"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// RoleRepository owns role rows and role assignments. Built-in rows are
// seeded by migration and reconciled from the registry at boot; custom
// rows are written only by the hosted edition's authoring service, but the
// SQL and the audit rows live here so there is one writer of the table.
// Implementations must enforce tenant scoping in every query: a custom
// role is visible only to its organization, a built-in one to every
// organization.
type RoleRepository interface {
	ListRoles(ctx context.Context, input ListRolesInput) ([]domain.RoleRow, error)
	GetRole(ctx context.Context, input GetRoleInput) (domain.RoleRow, error)
	CreateRole(ctx context.Context, input CreateRoleInput) (domain.RoleRow, error)
	UpdateRole(ctx context.Context, input UpdateRoleInput) (domain.RoleRow, error)
	DeleteRole(ctx context.Context, input DeleteRoleInput) error
	ReconcileBuiltInRoles(ctx context.Context, input ReconcileBuiltInRolesInput) (ReconcileReport, error)
	// ListAssignments lists a principal's assignments; for a membership
	// that includes the ones inherited through its teams.
	ListAssignments(ctx context.Context, input ListAssignmentsInput) ([]domain.RoleAssignment, error)
	// ReplaceAssignments makes the principal's manual assignments equal
	// the given set, inserting and deleting the difference and auditing
	// each change. Directory-sourced rows are never touched.
	ReplaceAssignments(ctx context.Context, input ReplaceAssignmentsInput) ([]domain.RoleAssignment, error)
	SetDirectoryGroupGrant(ctx context.Context, input SetDirectoryGroupGrantInput) error
	ListDirectoryGroupLinks(ctx context.Context, input ListDirectoryGroupLinksInput) ([]domain.DirectoryGroupLink, error)
	// LoadGrants flattens a principal's assignments, direct and inherited
	// through teams for memberships, into the grant set the authorizer
	// evaluates. The same query runs inside the decision transaction.
	LoadGrants(ctx context.Context, input LoadGrantsInput) ([]domain.Grant, error)
}

type LoadGrantsInput struct {
	OrganizationID domain.ID
	PrincipalKind  domain.PrincipalKind
	PrincipalID    domain.ID
}

func (input LoadGrantsInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("principal ID", input.PrincipalID),
	}); err != nil {
		return err
	}
	if !domain.ValidPrincipalKind(input.PrincipalKind) {
		return invalidField("principal kind", "must be membership, team, or agent")
	}
	return nil
}

type ListRolesInput struct {
	OrganizationID domain.ID
}

func (input ListRolesInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}

type GetRoleInput struct {
	OrganizationID domain.ID
	RoleID         domain.ID
}

func (input GetRoleInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("role ID", input.RoleID),
	})
}

// validRoleShape checks what every role write shares: registered,
// human-holdable permissions and bindable kinds that include the
// organization.
func validRoleShape(permissions []domain.Permission, kinds []domain.ScopeKind) error {
	if len(permissions) == 0 {
		return invalidField("permissions", "must contain at least one permission")
	}
	seen := make(map[domain.Permission]struct{}, len(permissions))
	for _, key := range permissions {
		def, ok := domain.LookupPermission(key)
		if !ok {
			return invalidField("permissions", "unknown permission "+string(key))
		}
		if !def.HeldBy(domain.HolderHuman) {
			return invalidField("permissions", string(key)+" cannot be held by a person")
		}
		if _, duplicate := seen[key]; duplicate {
			return invalidField("permissions", "must not repeat "+string(key))
		}
		seen[key] = struct{}{}
	}
	if len(kinds) == 0 || !slices.Contains(kinds, domain.ScopeOrganization) {
		return invalidField("bindable scope kinds", "must include organization")
	}
	for _, kind := range kinds {
		if !domain.ValidScopeKind(kind) {
			return invalidField("bindable scope kinds", "unknown scope kind "+string(kind))
		}
	}
	return nil
}

type CreateRoleInput struct {
	OrganizationID     domain.ID
	RoleID             domain.ID
	Key                domain.Role
	Name               string
	Description        string
	Permissions        []domain.Permission
	BindableScopeKinds []domain.ScopeKind
	ActorMembershipID  domain.ID
	Now                time.Time
}

func (input CreateRoleInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("role ID", input.RoleID),
		requiredString("role key", string(input.Key)),
		requiredString("role name", input.Name),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if !domain.ValidRoleKey(input.Key) {
		return invalidField("role key", "must be lowercase letters, digits, dots, underscores, or hyphens")
	}
	if _, builtIn := domain.BuiltInRole(input.Key); builtIn {
		return invalidField("role key", "is reserved for a built-in role")
	}
	return validRoleShape(input.Permissions, input.BindableScopeKinds)
}

type UpdateRoleInput struct {
	OrganizationID     domain.ID
	RoleID             domain.ID
	Name               string
	Description        string
	Permissions        []domain.Permission
	BindableScopeKinds []domain.ScopeKind
	ActorMembershipID  domain.ID
	Now                time.Time
}

func (input UpdateRoleInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("role ID", input.RoleID),
		requiredString("role name", input.Name),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	return validRoleShape(input.Permissions, input.BindableScopeKinds)
}

type DeleteRoleInput struct {
	OrganizationID    domain.ID
	RoleID            domain.ID
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input DeleteRoleInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("role ID", input.RoleID),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	})
}

// ReconcileBuiltInRolesInput carries the code definitions and the full
// registered vocabulary. Built-in rows are upserted from the definitions;
// every role row loses keys the registry no longer knows.
type ReconcileBuiltInRolesInput struct {
	Roles       []domain.RoleDefinition
	Permissions []domain.Permission
	Now         time.Time
}

func (input ReconcileBuiltInRolesInput) Validate() error {
	if len(input.Roles) == 0 {
		return invalidField("built-in roles", "must not be empty")
	}
	if len(input.Permissions) == 0 {
		return invalidField("permissions", "must not be empty")
	}
	return requiredTime("current time", input.Now)
}

// ReconcileReport says what the boot reconciler did, for the startup log.
type ReconcileReport struct {
	Seeded       int
	Updated      int
	StrippedKeys []domain.Permission
}

type ListAssignmentsInput struct {
	OrganizationID domain.ID
	PrincipalKind  domain.PrincipalKind
	PrincipalID    domain.ID
}

func (input ListAssignmentsInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("principal ID", input.PrincipalID),
	}); err != nil {
		return err
	}
	if !domain.ValidPrincipalKind(input.PrincipalKind) {
		return invalidField("principal kind", "must be membership, team, or agent")
	}
	return nil
}

type ReplaceAssignmentsInput struct {
	OrganizationID    domain.ID
	PrincipalKind     domain.PrincipalKind
	PrincipalID       domain.ID
	Assignments       []domain.AssignmentSpec
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input ReplaceAssignmentsInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("principal ID", input.PrincipalID),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if input.PrincipalKind != domain.PrincipalMembership && input.PrincipalKind != domain.PrincipalTeam {
		return invalidField("principal kind", "must be membership or team")
	}
	if len(input.Assignments) > 0 {
		return validAssignmentSpecs("assignments", input.Assignments)
	}
	return nil
}

// SetDirectoryGroupGrantInput maps a provider group to a grant held by
// the team the group links to, or clears it when Grant is nil.
type SetDirectoryGroupGrantInput struct {
	OrganizationID    domain.ID
	ProviderObjectID  string
	Grant             *domain.AssignmentSpec
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input SetDirectoryGroupGrantInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("provider object ID", input.ProviderObjectID),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if input.Grant != nil {
		return validAssignmentSpecs("grant", []domain.AssignmentSpec{*input.Grant})
	}
	return nil
}

type ListDirectoryGroupLinksInput struct {
	OrganizationID domain.ID
}

func (input ListDirectoryGroupLinksInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}
