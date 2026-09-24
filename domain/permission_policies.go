package domain

import "fmt"

// PermissionPolicySpec is a permission set over one kind of resource.
// It deliberately contains no role identity: bundles are server owned.
type PermissionPolicySpec struct {
	ScopeKind   ScopeKind    `json:"scope_kind"`
	ResourceIDs []string     `json:"resource_ids"`
	Permissions []Permission `json:"permissions"`
}

func (d PermissionDefinition) AppliesAt(kind ScopeKind) bool {
	return kind == ScopeOrganization || d.Resource == ResourceScope ||
		(kind == ScopeTeam && d.Resource == ResourceTeam) ||
		(kind == ScopeAgent && d.Resource == ResourceAgent)
}

func (s PermissionPolicySpec) Validate() error {
	return s.ValidateFor(HolderHuman)
}

func (s PermissionPolicySpec) ValidateFor(holder PermissionHolder) error {
	invalid := func(message string) error { return fmt.Errorf("%s: %w", message, ErrInvalid) }
	if !ValidScopeKind(s.ScopeKind) {
		return invalid("select an organization, team, or agent scope")
	}
	if len(s.Permissions) == 0 || len(s.Permissions) > len(Permissions()) {
		return invalid("select at least one permission")
	}
	if (s.ScopeKind == ScopeOrganization) != (len(s.ResourceIDs) == 0) {
		return invalid("select resources only for team or agent scope")
	}
	if len(s.ResourceIDs) > 200 {
		return invalid("select at most 200 resources")
	}
	seenIDs := map[string]bool{}
	for _, id := range s.ResourceIDs {
		if id == "" || seenIDs[id] {
			return invalid("resource IDs must be nonempty and distinct")
		}
		seenIDs[id] = true
	}
	seen := map[Permission]bool{}
	for _, key := range s.Permissions {
		d, ok := LookupPermission(key)
		if !ok || !d.HeldBy(holder) || !d.AppliesAt(s.ScopeKind) || seen[key] {
			return invalid("permission is unknown, repeated, or unavailable at this scope: " + string(key))
		}
		seen[key] = true
	}
	return nil
}

func (s PermissionPolicySpec) Scopes() []Scope {
	if s.ScopeKind == ScopeOrganization {
		return []Scope{{Kind: ScopeOrganization}}
	}
	scopes := make([]Scope, 0, len(s.ResourceIDs))
	for _, id := range s.ResourceIDs {
		scopes = append(scopes, Scope{Kind: s.ScopeKind, ID: id})
	}
	return scopes
}

type PermissionPolicy struct {
	PermissionPolicySpec
	ID             ID                   `json:"id"`
	Revision       int64                `json:"revision"`
	Resources      []PermissionResource `json:"resources"`
	Source         AssignmentSource     `json:"source"`
	ViaTeamID      ID                   `json:"via_team_id,omitempty"`
	ViaTeamName    string               `json:"via_team_name,omitempty"`
	CanEdit        bool                 `json:"can_edit"`
	ReadOnlyReason string               `json:"read_only_reason,omitempty"`
}

type PermissionResource struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Kind ScopeKind `json:"kind"`
}
