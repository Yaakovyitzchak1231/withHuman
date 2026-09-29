package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

func TestPermissionPoliciesAgainstRealPostgres(t *testing.T) {
	server := embeddedpgtest.Start(t)
	ctx := context.Background()
	store, err := Open(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatal(err)
	}
	recordAudit(t, store)
	now := time.Now().UTC()
	reconcileBuiltInRoles(t, ctx, store, now)
	result, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: "policies", OrganizationName: "Policies", UserID: domain.ID(uuid.NewString()), MembershipID: domain.ID(uuid.NewString()), DisplayName: "Owner", Email: "owner@policies.test", PasswordHash: "argon2id$test", AdmissionSource: domain.AdmissionManual, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	actor := result.Principal
	org := actor.OrganizationID
	team := func(name string) domain.Team {
		t.Helper()
		team, err := store.CreateTeam(ctx, application.CreateTeamInput{OrganizationID: org, Name: name, ActorMembershipID: actor.MembershipID, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		return team
	}
	recipient, a, b := team("Recipients"), team("A"), team("B")
	member := admitTeamTestMember(t, ctx, store, org, actor.MembershipID, "member@policies.test", "secret", now)
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{OrganizationID: org, TeamID: recipient.ID, MembershipIDs: []domain.ID{member}, ActorMembershipID: actor.MembershipID, Now: now}); err != nil {
		t.Fatal(err)
	}
	target := application.ReadPermissionPoliciesInput{Actor: actor, PrincipalKind: domain.PrincipalTeam, PrincipalID: recipient.ID}
	spec := domain.PermissionPolicySpec{ScopeKind: domain.ScopeTeam, ResourceIDs: []string{string(a.ID), string(b.ID)}, Permissions: []domain.Permission{domain.PermissionTeamRead}}
	input := application.MutatePermissionPolicyInput{ReadPermissionPoliciesInput: target, Spec: spec, IdempotencyKey: "create", Now: now}
	created, err := store.MutatePermissionPolicy(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Resources) != 2 || created.Revision != 1 {
		t.Fatalf("created=%+v", created)
	}
	replay, err := store.MutatePermissionPolicy(ctx, input)
	if err != nil || replay.ID != created.ID {
		t.Fatalf("retry=%+v %v", replay, err)
	}
	changed := input
	changed.Spec.Permissions = []domain.Permission{domain.PermissionTeamMemberWrite}
	if _, err := store.MutatePermissionPolicy(ctx, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("key reuse=%v", err)
	}
	page, err := store.ReadPermissionPolicies(ctx, target)
	if err != nil || len(page.Policies) != 1 {
		t.Fatalf("page=%+v %v", page, err)
	}
	inherited, err := store.ReadPermissionPolicies(ctx, application.ReadPermissionPoliciesInput{Actor: actor, PrincipalKind: domain.PrincipalMembership, PrincipalID: member})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range inherited.Policies {
		if p.ID == created.ID {
			found = true
			if p.CanEdit || p.ViaTeamID != recipient.ID {
				t.Fatalf("inherited writable: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("missing inherited policy")
	}
	// Same permission set on another recipient must not share a mutable bundle.
	other := team("Other")
	second := input
	second.PrincipalID = other.ID
	second.IdempotencyKey = "other"
	unrelated, err := store.MutatePermissionPolicy(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	edit := input
	edit.PolicyID = created.ID
	edit.Revision = 1
	edit.IdempotencyKey = "edit"
	edit.Spec.Permissions = []domain.Permission{domain.PermissionTeamMemberWrite}
	updated, err := store.MutatePermissionPolicy(ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID || updated.Revision != 2 {
		t.Fatalf("update=%+v", updated)
	}
	otherPage, err := store.ReadPermissionPolicies(ctx, second.ReadPermissionPoliciesInput)
	if err != nil || otherPage.Policies[0].ID != unrelated.ID || otherPage.Policies[0].Permissions[0] != domain.PermissionTeamRead {
		t.Fatalf("unrelated changed: %+v %v", otherPage, err)
	}
	stale := edit
	stale.IdempotencyKey = "stale"
	if _, err := store.MutatePermissionPolicy(ctx, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale edit=%v", err)
	}
	// Invalid resource selection is atomic and leaves the saved policy intact.
	bad := edit
	bad.Revision = 2
	bad.IdempotencyKey = "bad-resource"
	bad.Spec.ResourceIDs = []string{string(a.ID), uuid.NewString()}
	if _, err := store.MutatePermissionPolicy(ctx, bad); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("missing resource=%v", err)
	}
	// A scoped delegator cannot delete a policy that also covers another scope,
	// even when the caller provides forged organization-wide grants.
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{OrganizationID: org, PrincipalKind: domain.PrincipalMembership, PrincipalID: member, Assignments: []domain.AssignmentSpec{{RoleID: domain.RoleAdminID, ScopeKind: domain.ScopeTeam, ScopeID: string(a.ID)}}, ActorMembershipID: actor.MembershipID, Now: now}); err != nil {
		t.Fatal(err)
	}
	forged := actor
	forged.MembershipID = member
	remove := input
	remove.Actor = forged
	remove.PolicyID = created.ID
	remove.Revision = 2
	remove.Delete = true
	remove.IdempotencyKey = "forbidden-delete"
	if _, err := store.MutatePermissionPolicy(ctx, remove); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("out-of-scope delete=%v", err)
	}
	escalate := input
	escalate.Actor = forged
	escalate.IdempotencyKey = "forbidden-add"
	escalate.Spec = domain.PermissionPolicySpec{ScopeKind: domain.ScopeOrganization, Permissions: []domain.Permission{domain.PermissionRequestReadUnrouted}}
	if _, err := store.MutatePermissionPolicy(ctx, escalate); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("escalation=%v", err)
	}
	self := input
	self.PrincipalKind = domain.PrincipalMembership
	self.PrincipalID = actor.MembershipID
	self.IdempotencyKey = "self"
	if _, err := store.MutatePermissionPolicy(ctx, self); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("self edit=%v", err)
	}
	remove.Actor = actor
	remove.IdempotencyKey = "delete"
	if _, err := store.MutatePermissionPolicy(ctx, remove); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MutatePermissionPolicy(ctx, remove); err != nil {
		t.Fatalf("delete retry=%v", err)
	}
	page, err = store.ReadPermissionPolicies(ctx, target)
	if err != nil || len(page.Policies) != 0 {
		t.Fatalf("deleted page=%+v %v", page, err)
	}
	ownerGrants, err := store.LoadGrants(ctx, application.LoadGrantsInput{OrganizationID: org, PrincipalKind: domain.PrincipalMembership, PrincipalID: actor.MembershipID})
	if err != nil || !hasGrant(ownerGrants, domain.PermissionOrganizationDelete, domain.ScopeOrganization, "") {
		t.Fatalf("owner changed: %v", err)
	}
	t.Run("admission validates every policy before creating bundles", func(t *testing.T) {
		count := func() int {
			var n int
			if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM roles WHERE organization_id=$1`, org).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		before := count()
		invalid := spec
		invalid.ResourceIDs = []string{"missing-resource"}
		if _, err := store.CompilePermissionPolicies(ctx, application.CompilePermissionPoliciesInput{Actor: actor, Policies: []domain.PermissionPolicySpec{spec, invalid}, Now: now}); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("invalid admission: %v", err)
		}
		if count() != before {
			t.Fatal("failed admission created a bundle")
		}
		dangerous := domain.PermissionPolicySpec{ScopeKind: domain.ScopeOrganization, Permissions: []domain.Permission{domain.PermissionPolicyWrite}}
		if _, err := store.CompilePermissionPolicies(ctx, application.CompilePermissionPoliciesInput{Actor: actor, Policies: []domain.PermissionPolicySpec{dangerous}, Unattended: true, Now: now}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("unattended admission: %v", err)
		}
		compiled, err := store.CompilePermissionPolicies(ctx, application.CompilePermissionPoliciesInput{Actor: actor, Policies: []domain.PermissionPolicySpec{spec}, Now: now})
		if err != nil || len(compiled) != 2 || compiled[0].RoleID != compiled[1].RoleID {
			t.Fatalf("compiled admission: %+v %v", compiled, err)
		}
		if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{OrganizationID: org, PrincipalKind: domain.PrincipalTeam, PrincipalID: other.ID, Assignments: compiled, ActorMembershipID: actor.MembershipID, Now: now}); err != nil {
			t.Fatal(err)
		}
		page, err := store.ReadPermissionPolicies(ctx, second.ReadPermissionPoliciesInput)
		if err != nil || len(page.Policies) != 1 || len(page.Policies[0].ResourceIDs) != 2 {
			t.Fatalf("admission grouping: %+v %v", page, err)
		}
	})
	t.Run("sensitive changes require fresh authentication", func(t *testing.T) {
		stale := input
		stale.IdempotencyKey = "stale-auth"
		stale.Actor.Auth.AuthenticatedAt = now.Add(-365 * 24 * time.Hour)
		stale.Spec = domain.PermissionPolicySpec{ScopeKind: domain.ScopeOrganization, Permissions: []domain.Permission{domain.PermissionPolicyWrite}}
		if _, err := store.MutatePermissionPolicy(ctx, stale); !errors.Is(err, domain.ErrAuthenticationStale) {
			t.Fatalf("stale authentication: %v", err)
		}
	})
	t.Run("directory member cannot be manually granted permissions", func(t *testing.T) {
		if _, err := store.pool.Exec(ctx, `UPDATE memberships SET directory_managed=true WHERE id=$1`, member); err != nil {
			t.Fatal(err)
		}
		locked := input
		locked.PrincipalKind = domain.PrincipalMembership
		locked.PrincipalID = member
		locked.IdempotencyKey = "directory-member"
		if _, err := store.MutatePermissionPolicy(ctx, locked); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("directory write: %v", err)
		}
	})
	t.Run("policy views contain no role identities", func(t *testing.T) {
		raw, err := json.Marshal(created)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "role_id") || strings.Contains(string(raw), "role_name") || strings.Contains(string(raw), "role_key") {
			t.Fatalf("role identity leaked: %s", raw)
		}
	})

	t.Run("concurrent duplicate commands return one policy", func(t *testing.T) {
		command := input
		command.IdempotencyKey = "concurrent-retry"
		start := make(chan struct{})
		results := make([]domain.PermissionPolicy, 2)
		errs := make([]error, 2)
		var workers sync.WaitGroup
		for i := range results {
			workers.Add(1)
			go func(i int) {
				defer workers.Done()
				<-start
				results[i], errs[i] = store.MutatePermissionPolicy(ctx, command)
			}(i)
		}
		close(start)
		workers.Wait()
		if errs[0] != nil || errs[1] != nil || results[0].ID != results[1].ID {
			t.Fatalf("concurrent command: %+v %v", results, errs)
		}
	})
	t.Run("cross organization resources are refused", func(t *testing.T) {
		foreign, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: "foreign", OrganizationName: "Foreign", UserID: domain.ID(uuid.NewString()), MembershipID: domain.ID(uuid.NewString()), DisplayName: "Foreign Owner", Email: "owner@foreign.test", PasswordHash: "argon2id$test", AdmissionSource: domain.AdmissionManual, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		foreignTeam, err := store.CreateTeam(ctx, application.CreateTeamInput{OrganizationID: foreign.Principal.OrganizationID, Name: "Foreign team", ActorMembershipID: foreign.Principal.MembershipID, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		command := input
		command.Spec.ResourceIDs = []string{string(foreignTeam.ID)}
		command.IdempotencyKey = "cross-org"
		if _, err := store.MutatePermissionPolicy(ctx, command); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("foreign resource: %v", err)
		}
	})

	t.Run("pipeline scoped policies bind to live pipelines", func(t *testing.T) {
		if _, err := store.CreateRequestApprovalPipeline(ctx, application.CreateRequestApprovalPipelineInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: org, PipelineKey: "payments", Name: "Payments",
			CreatedByActorID: actor.MembershipID, Now: now,
		}); err != nil {
			t.Fatal(err)
		}
		command := input
		command.IdempotencyKey = "pipeline"
		command.Spec = domain.PermissionPolicySpec{ScopeKind: domain.ScopePipeline, ResourceIDs: []string{"payments"}, Permissions: []domain.Permission{domain.PermissionPipelineRead, domain.PermissionPipelineWrite}}
		policy, err := store.MutatePermissionPolicy(ctx, command)
		if err != nil {
			t.Fatalf("pipeline policy: %v", err)
		}
		if len(policy.Resources) != 1 || policy.Resources[0].Kind != domain.ScopePipeline || policy.Resources[0].ID != "payments" || policy.Resources[0].Name != "Payments" {
			t.Fatalf("pipeline policy resources=%+v", policy.Resources)
		}
		missing := command
		missing.IdempotencyKey = "pipeline-missing"
		missing.Spec.ResourceIDs = []string{"nobody"}
		if _, err := store.MutatePermissionPolicy(ctx, missing); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("unknown pipeline: %v", err)
		}
		options, err := store.ReadPermissionPolicies(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, resource := range options.Resources {
			if resource.Kind == domain.ScopePipeline && resource.ID == "payments" {
				found = true
			}
		}
		if !found {
			t.Fatalf("pipeline missing from options: %+v", options.Resources)
		}
	})

}
