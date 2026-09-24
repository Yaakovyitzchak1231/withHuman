package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// The hosted edition registers the issuance permission (admin by
// BuiltInRoles); the store's issuance and delegation checks are exercised
// here the way that edition holds it.
func init() {
	if err := domain.RegisterPermissions(domain.PermissionDefinition{
		Key: domain.PermissionAPIKeyOrganizationIssue, Description: "Issue organization API keys",
		Holders: []domain.PermissionHolder{domain.HolderHuman}, Resource: domain.ResourceOrganization, Dangerous: true,
		BuiltInRoles: []domain.Role{domain.RoleAdmin},
	}); err != nil && !strings.Contains(err.Error(), "already registered") {
		panic(err)
	}
}

func TestOrganizationAPIKeyLifecycle(t *testing.T) {
	pg := embeddedpgtest.Start(t)
	ctx := context.Background()
	store, err := Open(ctx, pg.URL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatal(err)
	}
	recordAudit(t, store)
	now := time.Now().UTC().Truncate(time.Microsecond)
	org, user, member := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	_, err = store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: org, OrganizationSlug: "orgkeys", OrganizationName: "Organization keys", UserID: user, MembershipID: member, DisplayName: "Owner", Email: "owner@orgkeys.test", PasswordHash: "test", AdmissionSource: domain.AdmissionManual, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReconcileBuiltInRoles(ctx, application.ReconcileBuiltInRolesInput{Roles: domain.BuiltInRoles(), Permissions: domain.PermissionKeys(), Now: now}); err != nil {
		t.Fatal(err)
	}
	adminUser, admin := apiKeyTestMember(t, ctx, store, org, member, domain.RoleAdminID, "integration-admin", now)
	hash := sha256.Sum256([]byte("session"))
	actor, err := store.CreateSession(ctx, application.CreateSessionInput{UserID: adminUser, OrganizationID: org, SecretHash: hash[:], IssuedAt: now, ExpiresAt: now.Add(time.Hour), Auth: domain.AuthContext{Method: "password", Assurance: "strong", AuthenticatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	keyID := domain.ID(uuid.NewString())
	_, secretHash, err := authn.MintOpaqueToken(domain.OrganizationAPIKeyPrefix, keyID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256([]byte("intent"))
	input := application.CreateAPIKeyInput{Kind: domain.APIKeyOrganization, Actor: actor, KeyID: keyID, OrganizationID: org, MembershipID: admin, Name: "terraform", Policies: []domain.PermissionPolicySpec{{ScopeKind: domain.ScopeOrganization, Permissions: []domain.Permission{domain.PermissionAgentRead, domain.PermissionAgentWrite, domain.PermissionPipelineRead}}}, SecretHash: secretHash, AssuranceAtIssue: "strong", Now: now, IdempotencyKey: "create-terraform", Fingerprint: fingerprint[:]}
	key, err := store.CreateAPIKey(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if key.Kind != domain.APIKeyOrganization || key.MembershipID != "" || key.CreatedByMembershipID != admin || len(key.Grants) != 3 {
		t.Fatalf("key: %+v", key)
	}
	replay := input
	replay.KeyID = domain.ID(uuid.NewString())
	same, err := store.CreateAPIKey(ctx, replay)
	if err != nil || same.ID != key.ID {
		t.Fatalf("replay: %+v %v", same, err)
	}
	changed := sha256.Sum256([]byte("different"))
	replay.Fingerprint = changed[:]
	if _, err = store.CreateAPIKey(ctx, replay); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("conflict: %v", err)
	}
	resolve := func() domain.Principal {
		t.Helper()
		p, err := store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{KeyID: key.ID, SecretHash: secretHash, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	principal := resolve()
	if principal.ActorType != domain.ActorAPIKey || principal.ActorID != key.ID || principal.MembershipID != "" {
		t.Fatalf("principal: %+v", principal)
	}
	if authorization.Check(principal, domain.PermissionAgentWrite, domain.AgentResource("ci")) != nil {
		t.Fatal("missing explicit grant")
	}
	if authorization.Check(principal.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestDecide})), domain.PermissionRequestDecide, domain.RequestResource("request")) == nil {
		t.Fatal("injected decision grant passed")
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{OrganizationID: org, ActorID: principal.ManagementActorID(), Name: "CI", AgentSlug: "ci", AgentQuota: application.UnlimitedQuota(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	var auditType domain.ActorType
	var auditID domain.ID
	if err = store.Pool().QueryRow(ctx, `SELECT actor_type,actor_id FROM test_audit_events WHERE organization_id=$1 AND subject_id=$2 AND event_type='agent.created'`, org, agent.ID).Scan(&auditType, &auditID); err != nil || auditType != domain.ActorAPIKey || auditID != key.ID {
		t.Fatalf("actor: %s %s %v", auditType, auditID, err)
	}

	// Concurrent retries serialize on one intent and commit exactly one key.
	t.Run("concurrent creation", func(t *testing.T) {
		type result struct {
			key domain.APIKey
			err error
		}
		results := make(chan result, 4)
		for range 4 {
			go func() {
				attempt := input
				attempt.KeyID = domain.ID(uuid.NewString())
				attempt.IdempotencyKey = "concurrent"
				key, err := store.CreateAPIKey(ctx, attempt)
				results <- result{key, err}
			}()
		}
		var id domain.ID
		for range 4 {
			r := <-results
			if r.err != nil {
				t.Fatal(r.err)
			}
			if id != "" && r.key.ID != id {
				t.Fatal("retry created another key")
			}
			id = r.key.ID
		}
	})
	if err = store.RecordAPIKeyUse(ctx, application.RecordAPIKeyUseInput{Principal: principal, Method: "GET", Route: "/api/v1/agents", Now: now}); err != nil {
		t.Fatal(err)
	}
	if err = store.Pool().QueryRow(ctx, `SELECT actor_type,actor_id FROM test_audit_events WHERE organization_id=$1 AND subject_id=$2 AND event_type='api_key.used'`, org, key.ID).Scan(&auditType, &auditID); err != nil || auditType != domain.ActorAPIKey || auditID != key.ID {
		t.Fatalf("use audit: %s %s %v", auditType, auditID, err)
	}

	t.Run("scope deletion and expiry", func(t *testing.T) {
		attempt := input
		attempt.KeyID = domain.ID(uuid.NewString())
		attempt.IdempotencyKey = "scoped"
		attempt.Policies = []domain.PermissionPolicySpec{{Permissions: []domain.Permission{domain.PermissionAgentRead}, ScopeKind: domain.ScopeAgent, ResourceIDs: []string{"ci"}}}
		scoped, err := store.CreateAPIKey(ctx, attempt)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{KeyID: scoped.ID, SecretHash: secretHash, Now: now})
		if err != nil || authorization.Check(resolved, domain.PermissionAgentRead, domain.AgentResource("other")) == nil {
			t.Fatalf("scope escaped: %v", err)
		}
		if _, err = store.ArchiveAgent(ctx, application.ArchiveAgentInput{OrganizationID: org, AgentID: agent.ID, ActorID: principal.ManagementActorID(), Now: now}); err != nil {
			t.Fatal(err)
		}
		resolved, err = store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{KeyID: scoped.ID, SecretHash: secretHash, Now: now})
		if err != nil || len(resolved.Grants) != 0 {
			t.Fatalf("deleted type grants: %+v %v", resolved.Grants, err)
		}
		// The slug stays taken, so no new agent can inherit the scope, and
		// restoring the agent does not bring the deleted grants back.
		if _, err = store.CreateAgent(ctx, application.CreateAgentInput{OrganizationID: org, ActorID: member, Name: "New CI", AgentSlug: "ci", AgentQuota: application.UnlimitedQuota(), Now: now}); !errors.Is(err, domain.ErrAgentSlugTaken) {
			t.Fatalf("archived slug reused: %v", err)
		}
		if _, err = store.RestoreAgent(ctx, application.RestoreAgentInput{OrganizationID: org, AgentID: agent.ID, ActorID: principal.ManagementActorID(), Now: now}); err != nil {
			t.Fatal(err)
		}
		resolved, err = store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{KeyID: scoped.ID, SecretHash: secretHash, Now: now})
		if err != nil || len(resolved.Grants) != 0 {
			t.Fatal("restore revived grants")
		}
		if _, err = store.Pool().Exec(ctx, `UPDATE api_keys SET expires_at=created_at + interval '1 second' WHERE id=$1`, scoped.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{KeyID: scoped.ID, SecretHash: secretHash, Now: now.Add(time.Hour)}); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("expired: %v", err)
		}
	})
	t.Run("team archiving ends delegation", func(t *testing.T) {
		team, err := store.CreateTeam(ctx, application.CreateTeamInput{OrganizationID: org, Name: "Scoped team", ActorMembershipID: member, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		attempt := input
		attempt.KeyID = domain.ID(uuid.NewString())
		attempt.IdempotencyKey = "team-key"
		attempt.Policies = []domain.PermissionPolicySpec{{Permissions: []domain.Permission{domain.PermissionTeamRead}, ScopeKind: domain.ScopeTeam, ResourceIDs: []string{string(team.ID)}}}
		key, err := store.CreateAPIKey(ctx, attempt)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.ArchiveTeam(ctx, application.ArchiveTeamInput{OrganizationID: org, TeamID: team.ID, ActorMembershipID: member, Now: now}); err != nil {
			t.Fatal(err)
		}
		resolved, err := store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{KeyID: key.ID, SecretHash: secretHash, Now: now})
		if err != nil || len(resolved.Grants) != 0 {
			t.Fatalf("archived team grants: %+v %v", resolved.Grants, err)
		}
		scopes, err := store.ListAPIKeyScopes(ctx, application.ListAPIKeyScopesInput{OrganizationID: org})
		if err != nil {
			t.Fatal(err)
		}
		for _, scope := range scopes {
			if scope.Kind == domain.ScopeTeam && scope.ID == string(team.ID) {
				t.Fatal("archived team offered for delegation")
			}
		}
		attempt.KeyID = domain.ID(uuid.NewString())
		attempt.IdempotencyKey = "archived-team-key"
		if _, err := store.CreateAPIKey(ctx, attempt); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("archived scope accepted: %v", err)
		}
	})

	t.Run("shared RBAC policy lifecycle", func(t *testing.T) {
		attempt := input
		attempt.KeyID = domain.ID(uuid.NewString())
		attempt.IdempotencyKey = "editable-policy-key"
		editable, err := store.CreateAPIKey(ctx, attempt)
		if err != nil {
			t.Fatal(err)
		}
		target := application.ReadPermissionPoliciesInput{Actor: actor, PrincipalKind: domain.PrincipalAPIKey, PrincipalID: editable.ID}
		page, err := store.ReadPermissionPolicies(ctx, target)
		if err != nil || len(page.Policies) != 1 {
			t.Fatalf("policies: %+v %v", page, err)
		}
		original := page.Policies[0]
		if !original.CanEdit {
			t.Fatal("creator with delegation authority cannot edit")
		}
		var bundleCount int
		if err := store.pool.QueryRow(ctx, `SELECT count(DISTINCT role_id) FROM role_assignments WHERE principal_kind='api_key' AND principal_id=$1 AND permission_policy_id=$2`, editable.ID, original.ID).Scan(&bundleCount); err != nil || bundleCount != 1 {
			t.Fatalf("shared RBAC storage: %d %v", bundleCount, err)
		}
		change := application.MutatePermissionPolicyInput{ReadPermissionPoliciesInput: target, PolicyID: original.ID, Revision: original.Revision, Spec: domain.PermissionPolicySpec{ScopeKind: domain.ScopeOrganization, Permissions: []domain.Permission{domain.PermissionAgentRead}}, IdempotencyKey: "narrow-key-policy", Now: now}
		updated, err := store.MutatePermissionPolicy(ctx, change)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{Kind: domain.APIKeyOrganization, KeyID: editable.ID, SecretHash: secretHash, Now: now})
		if err != nil || len(resolved.Grants) != 1 {
			t.Fatalf("live policy: %+v %v", resolved.Grants, err)
		}
		if len(resolve().Grants) != 3 {
			t.Fatal("editing one key affected another key")
		}
		change.IdempotencyKey = "stale-edit"
		if _, err := store.MutatePermissionPolicy(ctx, change); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("stale edit accepted: %v", err)
		}
		change.Revision = updated.Revision
		change.IdempotencyKey = "forbidden-human-policy"
		change.Spec.Permissions = []domain.Permission{domain.PermissionRequestDecide}
		if _, err := store.MutatePermissionPolicy(ctx, change); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("human permission accepted: %v", err)
		}
		change.IdempotencyKey = "remove-key-policy"
		change.Delete = true
		if _, err := store.MutatePermissionPolicy(ctx, change); err != nil {
			t.Fatal(err)
		}
		resolved, err = store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{Kind: domain.APIKeyOrganization, KeyID: editable.ID, SecretHash: secretHash, Now: now})
		if err != nil || len(resolved.Grants) != 0 {
			t.Fatalf("removed policy: %+v %v", resolved.Grants, err)
		}
		if _, err := store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{Kind: domain.APIKeyPersonal, KeyID: editable.ID, SecretHash: secretHash, Now: now}); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("mismatched key kind accepted: %v", err)
		}
		add := application.MutatePermissionPolicyInput{ReadPermissionPoliciesInput: target, Spec: input.Policies[0], IdempotencyKey: "restore-key-policy", Now: now}
		if _, err := store.MutatePermissionPolicy(ctx, add); err != nil {
			t.Fatal(err)
		}
		// Fresh authentication is required even when changing read-only access.
		if _, err := store.pool.Exec(ctx, `UPDATE sessions SET authenticated_at=$2 WHERE id=$1`, actor.Auth.SessionID, now.Add(-24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		add.IdempotencyKey = "stale-session-policy"
		if _, err := store.MutatePermissionPolicy(ctx, add); !errors.Is(err, domain.ErrAuthenticationStale) {
			t.Fatalf("stale session accepted: %v", err)
		}
		if _, err := store.pool.Exec(ctx, `UPDATE sessions SET authenticated_at=$2 WHERE id=$1`, actor.Auth.SessionID, now); err != nil {
			t.Fatal(err)
		}
		// Personal credentials cannot administer even organization keys they created.
		bearerTarget := target
		bearerTarget.Actor.Auth.Method = domain.AuthMethodAPIKey
		if _, err := store.ReadPermissionPolicies(ctx, bearerTarget); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("personal key policy access: %v", err)
		}
		if err := store.RevokeAPIKey(ctx, application.RevokeAPIKeyInput{OrganizationID: org, KeyID: editable.ID, ActorMembershipID: admin, Now: now}); err != nil {
			t.Fatal(err)
		}
		add.IdempotencyKey = "revoked-key-policy"
		if _, err := store.MutatePermissionPolicy(ctx, add); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("revoked key editable: %v", err)
		}
	})
	// Remove every creator grant and deactivate the creator. Runtime authority is independent.
	if _, err = store.Pool().Exec(ctx, `DELETE FROM role_assignments WHERE organization_id=$1 AND principal_id=$2`, org, admin); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool().Exec(ctx, `UPDATE memberships SET status='deprovisioned' WHERE organization_id=$1 AND id=$2`, org, admin); err != nil {
		t.Fatal(err)
	}
	if len(resolve().Grants) != 3 {
		t.Fatal("creator changes narrowed organization key")
	}
	// A stale issuance principal cannot grant again after the creator is removed.
	replay = input
	replay.IdempotencyKey = "after-removal"
	replay.KeyID = domain.ID(uuid.NewString())
	if _, err = store.CreateAPIKey(ctx, replay); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("stale issuer: %v", err)
	}
	if _, err = store.Pool().Exec(ctx, `UPDATE organization_policies SET personal_api_keys_allowed=false WHERE organization_id=$1`, org); err != nil {
		t.Fatal(err)
	}
	resolve()
	if _, err = store.Pool().Exec(ctx, `UPDATE organization_policies SET organization_api_keys_allowed=false WHERE organization_id=$1`, org); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{KeyID: key.ID, SecretHash: secretHash, Now: now}); !errors.Is(err, domain.ErrAPIKeysDisabled) {
		t.Fatalf("policy: %v", err)
	}
	listed, err := store.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: org, KeyID: key.ID})
	if err != nil || listed.BlockedReason != "api_keys_disabled" {
		t.Fatalf("blocked metadata: %+v %v", listed, err)
	}
	if _, err = store.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: domain.ID(uuid.NewString()), KeyID: key.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross organization: %v", err)
	}
	if err = store.RevokeAPIKey(ctx, application.RevokeAPIKeyInput{OrganizationID: org, KeyID: key.ID, ActorMembershipID: member, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err = store.RevokeAPIKey(ctx, application.RevokeAPIKeyInput{OrganizationID: org, KeyID: key.ID, ActorMembershipID: member, Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveAPIKey(ctx, application.ResolveAPIKeyInput{KeyID: key.ID, SecretHash: secretHash, Now: now}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked: %v", err)
	}
}
