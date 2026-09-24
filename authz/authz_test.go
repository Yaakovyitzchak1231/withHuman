package authz

import (
	"errors"
	"testing"

	"github.com/withHumanAI/withHuman/domain"
)

func TestCheckScopeLadder(t *testing.T) {
	org := domain.Grant{Permission: domain.PermissionPipelineWrite, ScopeKind: domain.ScopeOrganization}
	refund := domain.Grant{Permission: domain.PermissionPipelineWrite, ScopeKind: domain.ScopeAgent, ScopeID: "refund-bot"}
	payments := domain.Grant{Permission: domain.PermissionTeamWrite, ScopeKind: domain.ScopeTeam, ScopeID: "payments"}
	decide := domain.Grant{Permission: domain.PermissionRequestDecide, ScopeKind: domain.ScopeOrganization}

	tests := []struct {
		name       string
		grants     []domain.Grant
		permission domain.Permission
		resource   domain.Resource
		allowed    bool
	}{
		{"organization grant covers an agent type", []domain.Grant{org}, domain.PermissionPipelineWrite, domain.AgentResource("refund-bot"), true},
		{"organization grant covers the organization pipeline", []domain.Grant{org}, domain.PermissionPipelineWrite, domain.AgentResource(""), true},
		{"agent type grant covers its own type", []domain.Grant{refund}, domain.PermissionPipelineWrite, domain.AgentResource("refund-bot"), true},
		{"agent type grant does not cover another type", []domain.Grant{refund}, domain.PermissionPipelineWrite, domain.AgentResource("release-bot"), false},
		{"agent type grant does not cover the organization pipeline", []domain.Grant{refund}, domain.PermissionPipelineWrite, domain.AgentResource(""), false},
		{"team grant covers its team", []domain.Grant{payments}, domain.PermissionTeamWrite, domain.TeamResource("payments"), true},
		{"team grant does not cover another team", []domain.Grant{payments}, domain.PermissionTeamWrite, domain.TeamResource("legal"), false},
		{"team grant does not cover creating a team", []domain.Grant{payments}, domain.PermissionTeamWrite, domain.OrganizationResource(), false},
		{"request resource needs an organization grant", []domain.Grant{decide}, domain.PermissionRequestDecide, domain.RequestResource("r1"), true},
		{"wrong permission never matches", []domain.Grant{org}, domain.PermissionPipelineRead, domain.AgentResource("refund-bot"), false},
		{"resource kind mismatch fails closed", []domain.Grant{org}, domain.PermissionPipelineWrite, domain.TeamResource("payments"), false},
		{"unknown permission fails closed", []domain.Grant{{Permission: "made.up", ScopeKind: domain.ScopeOrganization}}, "made.up", domain.OrganizationResource(), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Check(domain.Principal{Grants: test.grants}, test.permission, test.resource)
			if test.allowed && err != nil {
				t.Fatalf("expected allow, got %v", err)
			}
			if !test.allowed {
				var denied Denied
				if !errors.As(err, &denied) || !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("expected Denied wrapping ErrForbidden, got %v", err)
				}
				if denied.Permission != test.permission {
					t.Fatalf("denied names %q, want %q", denied.Permission, test.permission)
				}
			}
		})
	}
}

func TestScopeAndHoldsAnywhere(t *testing.T) {
	principal := domain.Principal{Grants: []domain.Grant{
		{Permission: domain.PermissionPipelineRead, ScopeKind: domain.ScopeAgent, ScopeID: "refund-bot"},
		{Permission: domain.PermissionPipelineRead, ScopeKind: domain.ScopeAgent, ScopeID: "refund-bot"},
		{Permission: domain.PermissionPipelineRead, ScopeKind: domain.ScopeAgent, ScopeID: "billing-bot"},
		{Permission: domain.PermissionTeamRead, ScopeKind: domain.ScopeOrganization},
	}}
	set := Scope(principal, domain.PermissionPipelineRead)
	if set.All || len(set.Scopes) != 2 {
		t.Fatalf("expected two distinct agent-type scopes, got %#v", set)
	}
	if ids := set.IDs(domain.ScopeAgent); len(ids) != 2 || ids[0] != "billing-bot" || ids[1] != "refund-bot" {
		t.Fatalf("expected sorted distinct ids, got %v", ids)
	}
	if all := Scope(principal, domain.PermissionTeamRead); !all.All {
		t.Fatalf("organization grant must yield All, got %#v", all)
	}
	if !Scope(principal, domain.PermissionRequestReadUnrouted).Empty() {
		t.Fatal("unheld permission must yield an empty set")
	}
	if !HoldsAnywhere(principal, domain.PermissionPipelineRead) || HoldsAnywhere(principal, domain.PermissionRequestReadUnrouted) {
		t.Fatal("HoldsAnywhere disagrees with Scope")
	}
}

func TestProjectPermissionsIsSortedAndDistinct(t *testing.T) {
	keys := domain.ProjectPermissions([]domain.Grant{
		{Permission: domain.PermissionTeamRead, ScopeKind: domain.ScopeOrganization},
		{Permission: domain.PermissionAgentRead, ScopeKind: domain.ScopeAgent, ScopeID: "x"},
		{Permission: domain.PermissionAgentRead, ScopeKind: domain.ScopeAgent, ScopeID: "y"},
	})
	if len(keys) != 2 || keys[0] != domain.PermissionAgentRead || keys[1] != domain.PermissionTeamRead {
		t.Fatalf("unexpected projection %v", keys)
	}
}
