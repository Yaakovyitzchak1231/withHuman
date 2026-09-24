package authorization

import (
	"errors"
	"testing"

	"github.com/withHumanAI/withHuman/domain"
)

func TestOrganizationKeyCannotBroadenScopeOrAcquireHumanPowers(t *testing.T) {
	actor := domain.Principal{ActorType: domain.ActorHuman}.WithGrants([]domain.Grant{{Permission: domain.PermissionPipelineWrite, ScopeKind: domain.ScopeAgent, ScopeID: "payments"}, {Permission: domain.PermissionPolicyWrite, ScopeKind: domain.ScopeAgent, ScopeID: "payments"}})
	for _, scope := range []domain.Scope{{Kind: domain.ScopeOrganization}, {Kind: domain.ScopeAgent, ID: "other"}} {
		err := domain.ValidateOrganizationKeyPolicies(actor, []domain.PermissionPolicySpec{{Permissions: []domain.Permission{domain.PermissionPipelineWrite}, ScopeKind: scope.Kind, ResourceIDs: func() []string {
			if scope.ID == "" {
				return nil
			}
			return []string{scope.ID}
		}()}})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("scope %v: %v", scope, err)
		}
	}
	if err := domain.ValidateOrganizationKeyPolicies(actor, []domain.PermissionPolicySpec{{Permissions: []domain.Permission{domain.PermissionPipelineWrite}, ScopeKind: domain.ScopeAgent, ResourceIDs: []string{"payments"}}}); err != nil {
		t.Fatal(err)
	}
	p := domain.Principal{ActorType: domain.ActorAPIKey, Auth: domain.AuthContext{Method: domain.AuthMethodAPIKey}}.WithGrants(domain.OrganizationGrants(domain.HumanPermissions()))
	for _, permission := range []domain.Permission{domain.PermissionRequestClaim, domain.PermissionRequestDecide, domain.PermissionRequestDecideUnrouted, domain.PermissionAPIKeyIssue, domain.PermissionAPIKeyRead, domain.PermissionAPIKeyRevoke, domain.PermissionAPIKeyOrganizationIssue, domain.PermissionPolicyWrite, domain.PermissionOrganizationDelete} {
		if HoldsAnywhere(p, permission) || !Scope(p, permission).Empty() || Check(p, permission, domain.OrganizationResource()) == nil {
			t.Errorf("organization key holds forbidden %s", permission)
		}
	}
	personal := p
	personal.ActorType = domain.ActorHuman
	if HoldsAnywhere(personal, domain.PermissionAPIKeyOrganizationIssue) {
		t.Fatal("personal key can create organization keys")
	}
	if !HoldsAnywhere(p, domain.PermissionRequestReadUnrouted) {
		t.Fatal("valid management grant was lost")
	}
}
