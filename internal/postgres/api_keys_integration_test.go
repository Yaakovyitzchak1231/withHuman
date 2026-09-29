package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// apiKeyTestMember inserts a second active member holding one built-in
// role at organization scope.
func apiKeyTestMember(t *testing.T, ctx context.Context, store *Store, organizationID, ownerMembershipID domain.ID, role domain.ID, key string, now time.Time) (domain.ID, domain.ID) {
	t.Helper()
	userID := domain.ID(uuid.NewString())
	membershipID := domain.ID(uuid.NewString())
	if _, err := store.Pool().Exec(ctx, `INSERT INTO users
		(id, display_name, primary_email, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', $4, $4)`, userID, key, key+"@example.com", now); err != nil {
		t.Fatalf("insert %s user: %v", key, err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO memberships
		(id, user_id, organization_id, status, admission_source, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', 'manual', $4, $4)`, membershipID, userID, organizationID, now); err != nil {
		t.Fatalf("insert %s membership: %v", key, err)
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: organizationID, PrincipalKind: domain.PrincipalMembership, PrincipalID: membershipID,
		Assignments:       []domain.AssignmentSpec{{RoleID: role, ScopeKind: domain.ScopeOrganization}},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("assign %s: %v", key, err)
	}
	return userID, membershipID
}

// Exercises the personal API key repository against real PostgreSQL:
// minting, live resolution with narrowing, the organization's policy
// switch, revocation, expiry, membership status, and the deprovision
// cascade, with organization scoping on every read.
func TestAPIKeysAgainstRealPostgres(t *testing.T) {
	server := embeddedpgtest.Start(t)
	ctx := context.Background()
	store, err := Open(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	recordAudit(t, store)
	// Built-in roles are reconciled from code at boot; the migration seed
	// alone predates the auditor's request.read.unrouted.
	if _, err := store.ReconcileBuiltInRoles(ctx, application.ReconcileBuiltInRolesInput{Roles: domain.BuiltInRoles(), Permissions: domain.PermissionKeys(), Now: time.Now().UTC()}); err != nil {
		t.Fatalf("reconcile built-in roles: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID, ownerMembershipID := domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "acme", OrganizationName: "Acme",
		UserID: domain.ID(uuid.NewString()), MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test", AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	otherOrgID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: otherOrgID, OrganizationSlug: "other", OrganizationName: "Other",
		UserID: domain.ID(uuid.NewString()), MembershipID: domain.ID(uuid.NewString()), DisplayName: "Other",
		Email: "other@example.com", PasswordHash: "argon2id$test", AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization other: %v", err)
	}
	reviewerUserID, reviewerMembershipID := apiKeyTestMember(t, ctx, store, orgID, ownerMembershipID, domain.RoleReviewerID, "reviewer", now)

	// The policy switch defaults on.
	policy, err := store.GetOrganizationPolicy(ctx, application.GetOrganizationPolicyInput{OrganizationID: orgID})
	if err != nil || !policy.PersonalAPIKeysAllowed {
		t.Fatalf("policy = %+v err %v, want personal keys allowed by default", policy, err)
	}

	mint := func(membershipID domain.ID, name string, permissions []domain.Permission, expiresAt *time.Time) (domain.APIKey, []byte) {
		t.Helper()
		keyID := domain.ID(uuid.NewString())
		_, secretHash, err := authn.MintOpaqueToken(domain.APIKeyPrefix, keyID)
		if err != nil {
			t.Fatal(err)
		}
		key, err := store.CreateAPIKey(ctx, application.CreateAPIKeyInput{
			KeyID: keyID, OrganizationID: orgID, MembershipID: membershipID, Name: name, Permissions: permissions,
			SecretHash: secretHash, AssuranceAtIssue: "strong", Now: now, ExpiresAt: expiresAt,
		})
		if err != nil {
			t.Fatalf("CreateAPIKey(%s): %v", name, err)
		}
		return key, secretHash
	}
	resolve := func(key domain.APIKey, hash []byte, at time.Time) (domain.Principal, error) {
		return store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{KeyID: key.ID, SecretHash: hash, Now: at})
	}

	// An inherit-all key resolves to the member's own principal with the
	// key named in the auth context and last_used_at stamped.
	inherit, inheritHash := mint(reviewerMembershipID, "inherit", nil, nil)
	if inherit.Kind != domain.APIKeyPersonal || inherit.Permissions != nil || inherit.Prefix() != "whk_"+string(inherit.ID) {
		t.Fatalf("created key = %+v", inherit)
	}
	principal, err := resolve(inherit, inheritHash, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("resolve inherit: %v", err)
	}
	if principal.ActorType != domain.ActorHuman || principal.ActorID != reviewerUserID || principal.MembershipID != reviewerMembershipID ||
		principal.OrganizationID != orgID || principal.Auth.Method != domain.AuthMethodAPIKey || principal.Auth.CredentialID != inherit.ID ||
		principal.Auth.Assurance != "strong" {
		t.Fatalf("principal = %+v", principal)
	}
	if !slices.Contains(principal.Permissions, domain.PermissionRequestDecide) || slices.Contains(principal.Permissions, domain.PermissionPipelineWrite) {
		t.Fatalf("inherit-all permissions = %v, want the reviewer role", principal.Permissions)
	}
	if got, err := store.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: orgID, KeyID: inherit.ID}); err != nil || got.LastUsedAt == nil {
		t.Fatalf("last_used_at after resolve = %+v err %v", got, err)
	}
	wrongHash := authn.HashToken("not the secret")
	if _, err := resolve(inherit, wrongHash, now); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("wrong secret: err = %v", err)
	}

	// A narrowed key is the intersection: request.read stays, a listed
	// permission the reviewer lacks contributes nothing, and the rest of
	// the role is dropped.
	narrowed, narrowedHash := mint(reviewerMembershipID, "narrowed", []domain.Permission{domain.PermissionRequestRead, domain.PermissionPipelineWrite}, nil)
	principal, err = resolve(narrowed, narrowedHash, now)
	if err != nil {
		t.Fatalf("resolve narrowed: %v", err)
	}
	if len(principal.Permissions) != 1 || principal.Permissions[0] != domain.PermissionRequestRead {
		t.Fatalf("narrowed permissions = %v, want [request.read]", principal.Permissions)
	}

	// Listing scopes to the organization and optionally to one member.
	if keys, err := store.ListAPIKeys(ctx, application.ListAPIKeysInput{OrganizationID: orgID}); err != nil || len(keys) != 2 {
		t.Fatalf("org list = %d keys err %v, want 2", len(keys), err)
	}
	if keys, err := store.ListAPIKeys(ctx, application.ListAPIKeysInput{OrganizationID: orgID, MembershipID: ownerMembershipID}); err != nil || len(keys) != 0 {
		t.Fatalf("owner list = %d keys err %v, want 0", len(keys), err)
	}
	if keys, err := store.ListAPIKeys(ctx, application.ListAPIKeysInput{OrganizationID: otherOrgID}); err != nil || len(keys) != 0 {
		t.Fatalf("other org list = %d keys err %v, want 0", len(keys), err)
	}
	if _, err := store.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: otherOrgID, KeyID: inherit.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-organization get: err = %v", err)
	}

	// Role changes reach the key on the next request.
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: reviewerMembershipID,
		Assignments:       []domain.AssignmentSpec{{RoleID: domain.RoleAuditorID, ScopeKind: domain.ScopeOrganization}},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("reassign to auditor: %v", err)
	}
	principal, err = resolve(inherit, inheritHash, now)
	if err != nil {
		t.Fatalf("resolve after role change: %v", err)
	}
	if slices.Contains(principal.Permissions, domain.PermissionRequestDecide) || !slices.Contains(principal.Permissions, domain.PermissionRequestReadUnrouted) {
		t.Fatalf("permissions after role change = %v, want the auditor role", principal.Permissions)
	}

	// The organization switch refuses every personal key without
	// deleting any, and admits them again when turned back on.
	policy.PersonalAPIKeysAllowed = false
	update := application.UpdateOrganizationPolicyInput{
		OrganizationID: orgID, MFAMode: policy.MFAMode,
		ApprovalReauthWindow: policy.ApprovalReauthWindow, ClaimRetriggerWindow: policy.ClaimRetriggerWindow,
		UnroutedRequests: policy.UnroutedRequests, RequestVisibility: policy.RequestVisibility,
		PersonalAPIKeysAllowed: false, ActorMembershipID: ownerMembershipID, Now: now,
	}
	if _, err := store.UpdateOrganizationPolicy(ctx, update); err != nil {
		t.Fatalf("turn keys off: %v", err)
	}
	if _, err := resolve(inherit, inheritHash, now); !errors.Is(err, domain.ErrAPIKeysDisabled) {
		t.Fatalf("keys off: err = %v, want ErrAPIKeysDisabled", err)
	}
	update.PersonalAPIKeysAllowed = true
	if _, err := store.UpdateOrganizationPolicy(ctx, update); err != nil {
		t.Fatalf("turn keys on: %v", err)
	}
	if _, err := resolve(inherit, inheritHash, now); err != nil {
		t.Fatalf("keys on again: %v", err)
	}

	// Expiry is checked live; a key with no expiry is fine years later.
	soon := now.Add(time.Hour)
	expiring, expiringHash := mint(reviewerMembershipID, "expiring", nil, &soon)
	if _, err := resolve(expiring, expiringHash, now.Add(30*time.Minute)); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	if _, err := resolve(expiring, expiringHash, now.Add(2*time.Hour)); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("after expiry: err = %v", err)
	}
	if _, err := resolve(inherit, inheritHash, now.Add(5*365*24*time.Hour)); err != nil {
		t.Fatalf("no expiry years later: %v", err)
	}
	if got, _ := store.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: orgID, KeyID: expiring.ID}); got.Status(now.Add(2*time.Hour)) != domain.APIKeyExpired {
		t.Fatalf("status after expiry = %s", got.Status(now.Add(2*time.Hour)))
	}

	// Revocation is immediate, idempotent, scoped to the member when
	// asked, and audited.
	if err := store.RevokeAPIKey(ctx, application.RevokeAPIKeyInput{
		OrganizationID: orgID, KeyID: narrowed.ID, MembershipID: ownerMembershipID, ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoking another member's key as own: err = %v, want ErrNotFound", err)
	}
	if err := store.RevokeAPIKey(ctx, application.RevokeAPIKeyInput{
		OrganizationID: orgID, KeyID: narrowed.ID, ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("org-wide revoke: %v", err)
	}
	if err := store.RevokeAPIKey(ctx, application.RevokeAPIKeyInput{
		OrganizationID: orgID, KeyID: narrowed.ID, ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if _, err := resolve(narrowed, narrowedHash, now); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked key: err = %v", err)
	}
	got, err := store.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: orgID, KeyID: narrowed.ID})
	if err != nil || got.RevokedAt == nil || got.RevokedByMembershipID != ownerMembershipID || got.RevocationReason != domain.APIKeyRevokedManually {
		t.Fatalf("revoked row = %+v err %v", got, err)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events WHERE organization_id = $1 AND event_type = 'api_key.revoked' AND subject_id = $2`, orgID, narrowed.ID); count != 1 {
		t.Fatalf("api_key.revoked events = %d, want 1", count)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events WHERE organization_id = $1 AND event_type = 'api_key.created'`, orgID); count != 3 {
		t.Fatalf("api_key.created events = %d, want 3", count)
	}

	// Suspension refuses the key and reactivation brings it back;
	// deprovisioning revokes every live key of the member in the same
	// transaction.
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: reviewerMembershipID, Status: domain.MembershipSuspended,
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := resolve(inherit, inheritHash, now); !errors.Is(err, domain.ErrMembershipInactive) {
		t.Fatalf("suspended member: err = %v, want ErrMembershipInactive", err)
	}
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: reviewerMembershipID, Status: domain.MembershipActive,
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if _, err := resolve(inherit, inheritHash, now); err != nil {
		t.Fatalf("reactivated member: %v", err)
	}
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: reviewerMembershipID, Status: domain.MembershipDeprovisioned,
		RevokeSessions: true, ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("deprovision: %v", err)
	}
	if _, err := resolve(inherit, inheritHash, now); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("deprovisioned member's key: err = %v, want ErrUnauthenticated", err)
	}
	got, _ = store.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: orgID, KeyID: inherit.ID})
	if got.RevokedAt == nil || got.RevocationReason != domain.APIKeyRevokedMembershipDeprovisioned || got.RevokedByMembershipID != "" {
		t.Fatalf("cascaded row = %+v", got)
	}
	got, _ = store.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: orgID, KeyID: expiring.ID})
	if got.RevokedAt == nil || got.RevocationReason != domain.APIKeyRevokedMembershipDeprovisioned {
		t.Fatalf("cascade skipped the expiring key: %+v", got)
	}
	got, _ = store.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: orgID, KeyID: narrowed.ID})
	if got.RevocationReason != domain.APIKeyRevokedManually {
		t.Fatalf("cascade rewrote an already revoked key: %+v", got)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events WHERE organization_id = $1 AND event_type = 'api_key.revoked'`, orgID); count != 3 {
		t.Fatalf("api_key.revoked events after cascade = %d, want 3", count)
	}
}
