package application

import (
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// Handing out authority is a human act with a fresh session: a principal
// that authenticated with a personal API key is refused before any policy
// write, whatever kind of principal the policy targets.
func TestMutatePermissionPolicyRefusesAPIKeyActors(t *testing.T) {
	actor := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
		Auth: domain.AuthContext{Method: "password", AuthenticatedAt: time.Unix(1, 0)}}
	input := MutatePermissionPolicyInput{
		ReadPermissionPoliciesInput: ReadPermissionPoliciesInput{Actor: actor, PrincipalKind: domain.PrincipalMembership, PrincipalID: "membership-2"},
		Spec:                        domain.PermissionPolicySpec{ScopeKind: domain.ScopeOrganization, Permissions: []domain.Permission{domain.PermissionRequestRead}},
		IdempotencyKey:              "key-1", Now: time.Unix(2, 0),
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("session actor: %v", err)
	}
	input.Actor.Auth.Method = domain.AuthMethodAPIKey
	if err := input.Validate(); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("API-key actor = %v, want forbidden", err)
	}
}
