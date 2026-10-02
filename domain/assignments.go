package domain

import (
	"regexp"
	"slices"
	"time"
)

// PrincipalKind is what a role assignment is granted to.
type PrincipalKind string

const (
	PrincipalMembership PrincipalKind = "membership"
	PrincipalTeam       PrincipalKind = "team"
	PrincipalAgent      PrincipalKind = "agent"
	PrincipalAPIKey     PrincipalKind = "api_key"
)

func ValidPrincipalKind(kind PrincipalKind) bool {
	return kind == PrincipalMembership || kind == PrincipalTeam || kind == PrincipalAgent || kind == PrincipalAPIKey
}

// AssignmentSource records how an assignment came to exist: a person
// granted it, or a directory group mapping produced it.
type AssignmentSource string

const (
	AssignmentManual    AssignmentSource = "manual"
	AssignmentDirectory AssignmentSource = "directory"
)

var roleKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidRoleKey reports whether a key may name a role row.
func ValidRoleKey(key Role) bool { return roleKeyPattern.MatchString(string(key)) }

// RoleRow is a role as stored: a built-in row (no organization) or a
// custom row owned by one organization. Permissions are validated against
// the registry on write and reconciled against it at boot.
type RoleRow struct {
	ID                 ID           `json:"id"`
	OrganizationID     ID           `json:"organization_id,omitempty"`
	Key                Role         `json:"key"`
	Name               string       `json:"name"`
	Description        string       `json:"description"`
	Permissions        []Permission `json:"permissions"`
	BindableScopeKinds []ScopeKind  `json:"bindable_scope_kinds"`
	BuiltIn            bool         `json:"built_in"`
	// AssignmentCount is how many assignments reference the role in the
	// reading organization, populated by list and get.
	AssignmentCount int       `json:"assignment_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Dangerous reports whether the role holds any dangerous permission.
func (r RoleRow) Dangerous() bool {
	for _, key := range r.Permissions {
		if PermissionIsDangerous(key) {
			return true
		}
	}
	return false
}

// BindableAt reports whether the role may be assigned at the scope kind.
func (r RoleRow) BindableAt(kind ScopeKind) bool {
	return slices.Contains(r.BindableScopeKinds, kind)
}

// AssignmentSpec is the wire and stored shape of one grant: a role at a
// scope. ScopeID is empty for the organization, a team id, or an agent
// type.
type AssignmentSpec struct {
	RoleID    ID        `json:"role_id"`
	ScopeKind ScopeKind `json:"scope_kind"`
	ScopeID   string    `json:"scope_id,omitempty"`
}

// Scope returns the spec's scope.
func (s AssignmentSpec) Scope() Scope { return Scope{Kind: s.ScopeKind, ID: s.ScopeID} }

// RoleAssignment is one stored grant with the role resolved for display.
// ViaTeamID is set when a membership inherits the assignment through a
// team rather than holding it directly.
type RoleAssignment struct {
	Permissions []Permission `json:"permissions"`
	IsOwner     bool         `json:"is_owner"`
	// BuiltInRole names the predefined role the assignment grants; it is
	// empty for the private role a permission policy mints.
	BuiltInRole        Role             `json:"built_in_role,omitempty"`
	PermissionPolicyID ID               `json:"permission_policy_id,omitempty"`
	ID                 ID               `json:"id"`
	OrganizationID     ID               `json:"organization_id"`
	PrincipalKind      PrincipalKind    `json:"principal_kind"`
	PrincipalID        ID               `json:"principal_id"`
	RoleID             ID               `json:"-"`
	RoleKey            Role             `json:"-"`
	RoleName           string           `json:"-"`
	ScopeKind          ScopeKind        `json:"scope_kind"`
	ScopeID            string           `json:"scope_id,omitempty"`
	ScopeName          string           `json:"scope_name,omitempty"`
	Source             AssignmentSource `json:"source"`
	SourceKey          string           `json:"source_key,omitempty"`
	GrantedByActorID   ID               `json:"granted_by_actor_id,omitempty"`
	ViaTeamID          ID               `json:"via_team_id,omitempty"`
	ViaTeamName        string           `json:"via_team_name,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
}

// Spec projects the assignment back to its grant shape.
func (a RoleAssignment) Spec() AssignmentSpec {
	return AssignmentSpec{RoleID: a.RoleID, ScopeKind: a.ScopeKind, ScopeID: a.ScopeID}
}

// HoldsRoleEverywhere reports whether any assignment in the list grants
// the built-in role at organization scope, directly or through a team.
func HoldsRoleEverywhere(assignments []RoleAssignment, key Role) bool {
	for _, assignment := range assignments {
		if assignment.RoleKey == key && assignment.ScopeKind == ScopeOrganization {
			return true
		}
	}
	return false
}

// DirectoryGroupLink is one provider group as the admin sees it: which
// team it fills and, optionally, the grant the team holds because of it.
type DirectoryGroupLink struct {
	ProviderObjectID string          `json:"provider_object_id"`
	TeamID           ID              `json:"team_id"`
	TeamName         string          `json:"team_name"`
	Grant            *AssignmentSpec `json:"grant,omitempty"`
	RoleKey          Role            `json:"role_key,omitempty"`
	RoleName         string          `json:"role_name,omitempty"`
	UpdatedAt        time.Time       `json:"updated_at"`
}
