package authorization

import (
	"errors"
	"testing"

	"github.com/withHumanAI/withHuman/domain"
)

func builtIn(key domain.Role) domain.RoleRow {
	def, _ := domain.BuiltInRole(key)
	return domain.RoleRow{ID: def.ID, Key: def.Key, Name: def.Name, Permissions: def.Permissions, BindableScopeKinds: def.BindableScopeKinds, BuiltIn: true}
}

func TestEvaluateGrant(t *testing.T) {
	owner := domain.Principal{}.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleOwner}))
	admin := domain.Principal{}.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleAdmin}))
	reviewer := domain.Principal{}.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleReviewer}))
	var scopedAdmin domain.Principal
	for _, key := range builtIn(domain.RoleAdmin).Permissions {
		scopedAdmin.Grants = append(scopedAdmin.Grants, domain.Grant{Permission: key, ScopeKind: domain.ScopeAgent, ScopeID: "refund-bot"})
	}
	org := domain.Scope{Kind: domain.ScopeOrganization}
	refund := domain.Scope{Kind: domain.ScopeAgent, ID: "refund-bot"}
	release := domain.Scope{Kind: domain.ScopeAgent, ID: "release-bot"}
	editor := domain.RoleRow{ID: "custom", Key: "pipeline-editor", Permissions: []domain.Permission{domain.PermissionPipelineRead, domain.PermissionPipelineWrite, domain.PermissionPolicyWrite},
		BindableScopeKinds: []domain.ScopeKind{domain.ScopeOrganization, domain.ScopeAgent}}

	tests := []struct {
		name  string
		input GrantInput
		code  string
		err   error
	}{
		{"owner grants owner", GrantInput{Actor: owner, ActorIsOwner: true, Role: builtIn(domain.RoleOwner), Scope: org}, "", nil},
		{"admin cannot grant owner", GrantInput{Actor: admin, Role: builtIn(domain.RoleOwner), Scope: org}, "owner_required", domain.ErrForbidden},
		{"owner is not bindable at a team", GrantInput{Actor: owner, ActorIsOwner: true, Role: builtIn(domain.RoleOwner), Scope: domain.Scope{Kind: domain.ScopeTeam, ID: "t"}}, "scope_not_bindable", domain.ErrInvalid},
		{"admin grants reviewer", GrantInput{Actor: admin, Role: builtIn(domain.RoleReviewer), Scope: org}, "", nil},
		{"admin grants reviewer without holding request.decide, built-in roles are exempt", GrantInput{Actor: admin, Role: builtIn(domain.RoleReviewer), Scope: org}, "", nil},
		{"admin cannot grant a custom role holding request.decide", GrantInput{Actor: admin, Role: domain.RoleRow{ID: "c2", Key: "decider", Permissions: []domain.Permission{domain.PermissionRequestDecide}, BindableScopeKinds: []domain.ScopeKind{domain.ScopeOrganization}}, Scope: org}, "grant_exceeds_actor", domain.ErrForbidden},
		{"reviewer cannot assign at all", GrantInput{Actor: reviewer, Role: builtIn(domain.RoleReviewer), Scope: org}, "forbidden", domain.ErrForbidden},
		{"admin grants admin at an agent type", GrantInput{Actor: admin, Role: builtIn(domain.RoleAdmin), Scope: refund}, "", nil},
		{"scoped admin grants admin at the same type", GrantInput{Actor: scopedAdmin, Role: builtIn(domain.RoleAdmin), Scope: refund}, "", nil},
		{"scoped admin cannot grant admin at another type", GrantInput{Actor: scopedAdmin, Role: builtIn(domain.RoleAdmin), Scope: release}, "forbidden", domain.ErrForbidden},
		{"scoped admin cannot grant admin everywhere", GrantInput{Actor: scopedAdmin, Role: builtIn(domain.RoleAdmin), Scope: org}, "forbidden", domain.ErrForbidden},
		{"admin delegates permissions it holds at the same scope", GrantInput{Actor: admin, Role: editor, Scope: refund}, "", nil},
		{"owner grants the custom role", GrantInput{Actor: owner, ActorIsOwner: true, Role: editor, Scope: refund}, "", nil},
		{"dangerous role never arrives unattended", GrantInput{Actor: owner, ActorIsOwner: true, Role: editor, Scope: org, Unattended: true}, "dangerous_role_unattended", domain.ErrInvalid},
		{"reviewer role may arrive unattended", GrantInput{Actor: owner, ActorIsOwner: true, Role: builtIn(domain.RoleReviewer), Scope: org, Unattended: true}, "", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rejection := EvaluateGrant(test.input)
			if test.err == nil {
				if rejection != nil {
					t.Fatalf("unexpected rejection %s: %v", rejection.Code, rejection.Err)
				}
				return
			}
			if rejection == nil || rejection.Code != test.code || !errors.Is(rejection.Err, test.err) {
				t.Fatalf("got %#v, want %s (%v)", rejection, test.code, test.err)
			}
		})
	}
}
