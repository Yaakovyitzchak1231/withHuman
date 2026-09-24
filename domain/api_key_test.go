package domain

import (
	"testing"
	"time"
)

// Narrowing keeps scope with the grant and never adds: a listed
// permission the member lacks contributes nothing, and a nil list means
// the key inherits everything.
func TestNarrowGrantsIsAnIntersection(t *testing.T) {
	grants := []Grant{
		{Permission: PermissionPipelineWrite, ScopeKind: ScopeAgent, ScopeID: "refund-bot"},
		{Permission: PermissionPipelineRead, ScopeKind: ScopeOrganization},
		{Permission: PermissionMembershipRead, ScopeKind: ScopeOrganization},
	}
	if got := NarrowGrants(grants, nil); len(got) != 3 {
		t.Fatalf("nil list narrowed to %d grants, want all 3", len(got))
	}
	got := NarrowGrants(grants, []Permission{PermissionPipelineWrite, PermissionAgentWrite})
	if len(got) != 1 || got[0].Permission != PermissionPipelineWrite || got[0].ScopeID != "refund-bot" {
		t.Fatalf("narrowed = %+v, want the team-scoped pipeline.write only", got)
	}
	if got := NarrowGrants(grants, []Permission{}); len(got) != 0 {
		t.Fatalf("empty list narrowed to %d grants, want none", len(got))
	}
}

func TestAPIKeyStatus(t *testing.T) {
	now := time.Unix(10_000, 0)
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	if got := (APIKey{}).Status(now); got != APIKeyActive {
		t.Fatalf("no expiry = %s, want active", got)
	}
	if got := (APIKey{ExpiresAt: &future}).Status(now); got != APIKeyActive {
		t.Fatalf("future expiry = %s, want active", got)
	}
	if got := (APIKey{ExpiresAt: &past}).Status(now); got != APIKeyExpired {
		t.Fatalf("past expiry = %s, want expired", got)
	}
	if got := (APIKey{ExpiresAt: &past, RevokedAt: &past}).Status(now); got != APIKeyRevoked {
		t.Fatalf("revoked and expired = %s, want revoked", got)
	}
	if got := (APIKey{ID: "k1"}).Prefix(); got != "whk_k1" {
		t.Fatalf("prefix = %q", got)
	}
}

// Every human permission can ride on a key except the api_key family,
// so a key can never mint or revoke keys; agent-only
// permissions are not bearable either.
func TestAPIKeyBearableExcludesTheKeyFamily(t *testing.T) {
	for _, permission := range []Permission{PermissionPipelineWrite, PermissionRequestDecide, PermissionRequestRead, PermissionRequestReadUnrouted} {
		if !APIKeyBearable(permission) {
			t.Errorf("%s should be bearable", permission)
		}
	}
	for _, permission := range []Permission{PermissionAPIKeyIssue, PermissionAPIKeyRead, PermissionAPIKeyRevoke, PermissionRequestCreate, "nope.nope"} {
		if APIKeyBearable(permission) {
			t.Errorf("%s should not be bearable", permission)
		}
	}
}
