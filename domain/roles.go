package domain

import "slices"

// Role is a role key. The five built-in keys below are seeded from code;
// custom roles carry organization-chosen keys.
type Role string

const (
	RoleOwner    Role = "owner"
	RoleAdmin    Role = "admin"
	RoleReviewer Role = "reviewer"
	RoleAuditor  Role = "auditor"
	RoleAgent    Role = "agent"
)

// Fixed identifiers for the built-in role rows, shared by the seeding
// migration and the boot reconciler so both agree on the rows.
const (
	RoleOwnerID    ID = "00000000-0000-4000-8000-000000000001"
	RoleAdminID    ID = "00000000-0000-4000-8000-000000000002"
	RoleReviewerID ID = "00000000-0000-4000-8000-000000000003"
	RoleAuditorID  ID = "00000000-0000-4000-8000-000000000004"
	RoleAgentID    ID = "00000000-0000-4000-8000-000000000005"
)

// RoleDefinition is a built-in role as code defines it. Permissions are
// resolved against the registry at call time so the owner role always
// holds every human key, including ones an edition registers.
type RoleDefinition struct {
	ID                 ID
	Key                Role
	Name               string
	Description        string
	Permissions        []Permission
	BindableScopeKinds []ScopeKind
}

var adminPermissions = []Permission{
	PermissionOrganizationRead, PermissionOrganizationSettingsWrite,
	PermissionMembershipRead, PermissionMembershipInvite, PermissionMembershipWrite, PermissionPolicyWrite,
	PermissionPolicyRead,
	PermissionTeamRead, PermissionTeamWrite, PermissionTeamMemberWrite, PermissionTeamEscalationWrite, PermissionTeamEscalationActivate,
	PermissionAgentRead, PermissionAgentWrite, PermissionAgentCredentialIssue, PermissionAgentCredentialRevoke,
	PermissionPipelineRead, PermissionPipelineWrite, PermissionPipelineActivate,
	PermissionEscalationPathRead, PermissionEscalationPathWrite, PermissionEscalationPathActivate,
	PermissionWebhookRead, PermissionWebhookWrite,
	PermissionConnectionRead, PermissionConnectionWrite,
	PermissionAPIKeyIssue, PermissionAPIKeyRead, PermissionAPIKeyRevoke,
}

var reviewerPermissions = []Permission{
	PermissionOrganizationRead, PermissionTeamRead, PermissionAgentRead,
	PermissionRequestRead, PermissionRequestClaim, PermissionRequestDecide,
	PermissionAPIKeyIssue,
}

var auditorPermissions = []Permission{
	PermissionOrganizationRead, PermissionTeamRead, PermissionAgentRead, PermissionPolicyRead,
	PermissionRequestRead, PermissionRequestReadUnrouted,
	PermissionAPIKeyIssue, PermissionAPIKeyRead,
}

var agentPermissions = []Permission{PermissionRequestCreate, PermissionRequestRead}

// BuiltInRoles lists the five roles every deployment seeds. Admin is
// bindable at team, agent, and pipeline scope so the open edition can
// express scoped management with built-in roles alone; a narrowly bound
// admin carries only the permissions whose resource exists at that scope. A
// role's list is its core keys plus every registered edition key that
// names the role, resolved at call time like the owner's.
func BuiltInRoles() []RoleDefinition {
	return []RoleDefinition{
		{ID: RoleOwnerID, Key: RoleOwner, Name: "Owner", Description: "Full control, including billing, deleting the organization, and deciding outside routing.",
			Permissions: HumanPermissions(), BindableScopeKinds: []ScopeKind{ScopeOrganization}},
		{ID: RoleAdminID, Key: RoleAdmin, Name: "Admin", Description: "Connects agents, writes pipelines and paths, and manages members and teams.",
			Permissions: rolePermissions(RoleAdmin, adminPermissions), BindableScopeKinds: []ScopeKind{ScopeOrganization, ScopeTeam, ScopeAgent, ScopePipeline}},
		{ID: RoleReviewerID, Key: RoleReviewer, Name: "Reviewer", Description: "Reviews and decides the requests routed to them.",
			Permissions: rolePermissions(RoleReviewer, reviewerPermissions), BindableScopeKinds: []ScopeKind{ScopeOrganization}},
		{ID: RoleAuditorID, Key: RoleAuditor, Name: "Auditor", Description: "Reads every request, decision, and role without changing anything.",
			Permissions: rolePermissions(RoleAuditor, auditorPermissions), BindableScopeKinds: []ScopeKind{ScopeOrganization}},
		{ID: RoleAgentID, Key: RoleAgent, Name: "Agent", Description: "What every agent identity holds: asking for approval and reading its own requests.",
			Permissions: slices.Clone(agentPermissions), BindableScopeKinds: []ScopeKind{ScopeOrganization}},
	}
}

// rolePermissions is a built-in role's core keys followed by the
// registered keys that name it.
func rolePermissions(role Role, core []Permission) []Permission {
	keys := slices.Clone(core)
	for _, def := range Permissions() {
		if slices.Contains(def.BuiltInRoles, role) && !slices.Contains(keys, def.Key) {
			keys = append(keys, def.Key)
		}
	}
	return keys
}

// BuiltInRole finds a built-in definition by key.
func BuiltInRole(key Role) (RoleDefinition, bool) {
	for _, def := range BuiltInRoles() {
		if def.Key == key {
			return def, true
		}
	}
	return RoleDefinition{}, false
}

// BuiltInRoleByID finds a built-in definition by its fixed row id.
func BuiltInRoleByID(id ID) (RoleDefinition, bool) {
	for _, def := range BuiltInRoles() {
		if def.ID == id {
			return def, true
		}
	}
	return RoleDefinition{}, false
}

// ValidRole reports whether the key names a built-in human role: the
// predefined roles a person can be invited with or given.
func ValidRole(role Role) bool {
	_, ok := BuiltInRole(role)
	return ok && role != RoleAgent
}

// HumanRoles lists the predefined roles a person can hold, in the order
// the pickers offer them: owner first, the agent's role left out.
func HumanRoles() []RoleDefinition {
	roles := make([]RoleDefinition, 0, 4)
	for _, def := range BuiltInRoles() {
		if def.Key != RoleAgent {
			roles = append(roles, def)
		}
	}
	return roles
}

// GrantsForRoles flattens built-in roles held at organization scope into
// grants: the shape a test fixture needs without a database. Production
// code loads grants from role assignments.
func GrantsForRoles(roles []Role) []Grant {
	seen := make(map[Permission]struct{})
	for _, role := range roles {
		def, ok := BuiltInRole(role)
		if !ok {
			continue
		}
		for _, permission := range def.Permissions {
			seen[permission] = struct{}{}
		}
	}
	keys := make([]Permission, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return OrganizationGrants(keys)
}
