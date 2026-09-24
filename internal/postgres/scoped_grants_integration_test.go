package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// assignmentRows counts the assignment rows matching one principal, role,
// and scope, whatever their source.
func assignmentRows(t *testing.T, ctx context.Context, store *Store, kind domain.PrincipalKind, principalID, roleID domain.ID, scope domain.Scope) int {
	t.Helper()
	return countRows(t, ctx, store, `SELECT count(*) FROM role_assignments
        WHERE principal_kind = $1 AND principal_id = $2 AND role_id = $3 AND scope_kind = $4 AND scope_id = $5`,
		string(kind), principalID, roleID, string(scope.Kind), scope.ID)
}

// Scoped grants against real PostgreSQL (design doc 10, phase 3): agents
// are principals holding the built-in agent role, a team's admin grant
// at an agent is an ordinary assignment row, list queries narrow by
// the caller's scope set, teams carry their assignments, and the path
// editor's warning count sees inheritance.
func TestScopedGrantsAgainstRealPostgres(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	reconcileBuiltInRoles(t, ctx, store, now)

	org, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: "scoped", OrganizationName: "Scoped",
		UserID: domain.ID(uuid.NewString()), MembershipID: domain.ID(uuid.NewString()), DisplayName: "Founder",
		Email: "founder@scoped.example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	})
	if err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	orgID, ownerMembershipID := org.Organization.ID, org.Principal.MembershipID

	invite := func(email string, grants []domain.AssignmentSpec) domain.Principal {
		t.Helper()
		invitationID := domain.ID(uuid.NewString())
		hash := tokenHashOf("invite-" + email)
		if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
			OrganizationID: orgID, InvitationID: invitationID, Email: email, Grants: grants,
			InvitedByMembershipID: ownerMembershipID, TokenHash: hash, ExpiresAt: now.Add(time.Hour),
			SeatQuota: application.UnlimitedQuota(), Now: now,
		}); err != nil {
			t.Fatalf("CreateInvitation(%s): %v", email, err)
		}
		principal, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{
			InvitationID: invitationID, TokenHash: hash, AssertedEmail: email,
			NewUserID: domain.ID(uuid.NewString()), DisplayName: email, PasswordHash: "argon2id$test",
			ProviderKey: "local", Issuer: "withhuman:self-hosted", MembershipID: domain.ID(uuid.NewString()),
			SeatQuota: application.UnlimitedQuota(), Now: now,
		})
		if err != nil {
			t.Fatalf("AcceptInvitation(%s): %v", email, err)
		}
		return principal
	}
	createTeam := func(name string) domain.Team {
		t.Helper()
		team, err := store.CreateTeam(ctx, application.CreateTeamInput{OrganizationID: orgID, Name: name, ActorMembershipID: ownerMembershipID, Now: now})
		if err != nil {
			t.Fatalf("CreateTeam(%s): %v", name, err)
		}
		return team
	}
	refunds, releases := createTeam("Refunds"), createTeam("Releases")

	// Creating an agent writes its agent-role assignment and audits it.
	// Nothing about the agent hands a team a role: the team that manages
	// the type gets its admin row through the assignment API like any
	// other grant.
	refundBot, err := store.CreateAgent(ctx, application.CreateAgentInput{
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "refund bot", AgentSlug: "refund-bot",
		AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent(refund bot): %v", err)
	}
	organizationScope := domain.Scope{Kind: domain.ScopeOrganization}
	refundScope := domain.Scope{Kind: domain.ScopeAgent, ID: "refund-bot"}
	if count := assignmentRows(t, ctx, store, domain.PrincipalAgent, refundBot.ID, domain.RoleAgentID, organizationScope); count != 1 {
		t.Fatalf("agent role rows for refund bot = %d, want 1", count)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events
        WHERE organization_id = $1 AND event_type = 'role_assignment.granted'
          AND subject_id = $2 AND actor_id = $3`, orgID, refundBot.ID, org.Principal.ActorID); count != 1 {
		t.Fatalf("granted audit rows = %d, want 1", count)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM role_assignments
        WHERE organization_id = $1 AND principal_kind = 'team'`, orgID); count != 0 {
		t.Fatalf("team assignment rows after creating an agent = %d, want 0", count)
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: refunds.ID,
		Assignments:       []domain.AssignmentSpec{{RoleID: domain.RoleAdminID, ScopeKind: domain.ScopeAgent, ScopeID: "refund-bot"}},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("ReplaceAssignments(refunds): %v", err)
	}
	if count := assignmentRows(t, ctx, store, domain.PrincipalTeam, refunds.ID, domain.RoleAdminID, refundScope); count != 1 {
		t.Fatalf("refunds admin rows = %d, want 1", count)
	}
	var grantedBy string
	if err := store.Pool().QueryRow(ctx, `SELECT granted_by_actor_id::text FROM role_assignments
        WHERE principal_kind = 'agent' AND principal_id = $1`, refundBot.ID).Scan(&grantedBy); err != nil || grantedBy != string(ownerMembershipID) {
		t.Fatalf("agent assignment granted_by = %q, %v; want the creating membership", grantedBy, err)
	}
	// A slug names exactly one agent: a second agent cannot take
	// refund-bot, and the refusal leaves the team's row at that scope
	// untouched. Another refund agent gets a slug of its own and is a
	// principal outside the refund-bot scope.
	if _, err := store.CreateAgent(ctx, application.CreateAgentInput{
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "refund bot two", AgentSlug: "refund-bot",
		AgentQuota: application.UnlimitedQuota(), Now: now,
	}); !errors.Is(err, domain.ErrAgentSlugTaken) {
		t.Fatalf("CreateAgent(refund bot two) on a taken slug = %v, want slug taken", err)
	}
	if count := assignmentRows(t, ctx, store, domain.PrincipalTeam, refunds.ID, domain.RoleAdminID, refundScope); count != 1 {
		t.Fatalf("refunds admin rows after the refused agent = %d, want 1", count)
	}
	if _, err := store.CreateAgent(ctx, application.CreateAgentInput{
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "refund bot two", AgentSlug: "refund-bot-two",
		AgentQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("CreateAgent(refund bot two): %v", err)
	}
	if count := assignmentRows(t, ctx, store, domain.PrincipalTeam, refunds.ID, domain.RoleAdminID, refundScope); count != 1 {
		t.Fatalf("refunds admin rows after second agent = %d, want 1", count)
	}
	if _, err := store.CreateAgent(ctx, application.CreateAgentInput{
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "release bot", AgentSlug: "release-bot",
		AgentQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("CreateAgent(release bot): %v", err)
	}
	releaseScope := domain.Scope{Kind: domain.ScopeAgent, ID: "release-bot"}
	if count := assignmentRows(t, ctx, store, domain.PrincipalTeam, releases.ID, domain.RoleAdminID, releaseScope); count != 0 {
		t.Fatalf("release team admin rows = %d, want 0", count)
	}

	// The credential resolves the agent's grants from its assignment.
	secret := sha256.Sum256([]byte("refund-credential"))
	credentialID := domain.ID(uuid.NewString())
	if _, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
		OrganizationID: orgID, AgentID: refundBot.ID, ActorID: org.Principal.ActorID, Name: "runner",
		Metadata: json.RawMessage(`{}`),
		Credential: application.AgentCredentialRecord{
			ID: credentialID, SecretHash: secret[:], IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		},
		Now: now,
	}); err != nil {
		t.Fatalf("CreateAgentInstanceWithCredential: %v", err)
	}
	agentPrincipal, err := store.ResolveAgentCredential(ctx, application.ResolveAgentCredentialInput{
		CredentialID: credentialID, SecretHash: secret[:], Now: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("ResolveAgentCredential: %v", err)
	}
	if !hasGrant(agentPrincipal.Grants, domain.PermissionRequestCreate, domain.ScopeOrganization, "") ||
		!hasGrant(agentPrincipal.Grants, domain.PermissionRequestRead, domain.ScopeOrganization, "") ||
		hasGrant(agentPrincipal.Grants, domain.PermissionRequestDecide, domain.ScopeOrganization, "") {
		t.Fatalf("agent principal grants = %#v", agentPrincipal.Grants)
	}
	if agentPrincipal.AgentSlug != "refund-bot" || agentPrincipal.ActorID != refundBot.ID {
		t.Fatalf("agent principal = %#v", agentPrincipal)
	}

	// Enrollment attaches multiple instances to the existing principal.
	enrolledAgent, err := store.CreateAgent(ctx, application.CreateAgentInput{OrganizationID: orgID, ActorID: ownerMembershipID, Name: "enrolled bot", AgentSlug: "claude-code", AgentQuota: application.UnlimitedQuota(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(code, instanceName string) domain.AgentInstance {
		t.Helper()
		if err := store.CreateAgentEnrollment(ctx, application.CreateAgentEnrollmentInput{Enrollment: domain.AgentEnrollment{
			ID: domain.ID(uuid.NewString()), EnrollmentCodeHash: tokenHashOf(code), UserCode: code,
			Runtime: "claude-code", RequestedAgentSlug: "claude-code", InstanceName: instanceName, InstanceMetadata: json.RawMessage(`{}`),
			Status: "pending", ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		}}); err != nil {
			t.Fatalf("CreateAgentEnrollment(%s): %v", code, err)
		}
		instance, err := store.AuthorizeAgentEnrollment(ctx, application.AuthorizeAgentEnrollmentInput{
			OrganizationID: orgID, UserCode: code, MembershipID: ownerMembershipID, AgentID: enrolledAgent.ID, Now: now,
		})
		if err != nil {
			t.Fatalf("AuthorizeAgentEnrollment(%s): %v", code, err)
		}
		return instance
	}
	first := enroll("ENROL-1", "laptop")
	second := enroll("ENROL-2", "desktop")
	if first.AgentID != second.AgentID {
		t.Fatalf("enrollments under one name created two agents: %s, %s", first.AgentID, second.AgentID)
	}
	if count := assignmentRows(t, ctx, store, domain.PrincipalAgent, first.AgentID, domain.RoleAgentID, organizationScope); count != 1 {
		t.Fatalf("enrolled agent role rows = %d, want 1", count)
	}

	// A member who is admin only at refund-bot (through the Refunds team)
	// lists only that agent, and never the organization pipeline.
	editor := invite("editor@scoped.example.com", []domain.AssignmentSpec{organizationGrant(domain.RoleAuditorID)})
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: refunds.ID, MembershipIDs: []domain.ID{editor.MembershipID},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("SetTeamManualMembers(refunds): %v", err)
	}
	editorGrants, err := store.LoadGrants(ctx, application.LoadGrantsInput{OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: editor.MembershipID})
	if err != nil || !hasGrant(editorGrants, domain.PermissionPipelineRead, domain.ScopeAgent, "refund-bot") ||
		hasGrant(editorGrants, domain.PermissionPipelineRead, domain.ScopeOrganization, "") {
		t.Fatalf("editor grants = %#v, %v", editorGrants, err)
	}
	refundOnly := domain.ScopeSet{Scopes: []domain.Scope{refundScope}}
	everywhere := domain.ScopeSet{All: true}

	summaries, err := store.ListAgentSummaries(ctx, application.ListAgentSummariesInput{OrganizationID: orgID, Scope: refundOnly, Limit: 50, Now: now})
	if err != nil || len(summaries) != 1 || summaries[0].AgentSlug != "refund-bot" {
		t.Fatalf("scoped agent summaries = %#v, %v; want refund-bot alone, not refund-bot-two", summaries, err)
	}
	if all, err := store.ListAgentSummaries(ctx, application.ListAgentSummariesInput{OrganizationID: orgID, Scope: everywhere, Limit: 50, Now: now}); err != nil || len(all) != 4 {
		t.Fatalf("unscoped agent summaries = %d, %v; want 4", len(all), err)
	}

	blocks := []application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("manual-review", `{"on_match":"human"}`)}
	for _, agentSlug := range []domain.AgentSlug{"", "refund-bot", "release-bot"} {
		createPipelineTestRevision(t, ctx, store, orgID, ownerMembershipID, agentSlug, blocks, now)
	}
	pipelines, err := store.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{
		OrganizationID: orgID, Scope: refundOnly, Limit: 50,
	})
	if err != nil || len(pipelines) != 1 || pipelines[0].AgentSlug != "refund-bot" {
		t.Fatalf("scoped pipeline summaries = %#v, %v; want refund-bot alone", pipelines, err)
	}
	pipelines, err = store.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{
		OrganizationID: orgID, Scope: everywhere, Limit: 50,
	})
	// Every agent seeds its own pipeline, so the unscoped list holds the
	// organization row first plus one row per agent.
	if err != nil || len(pipelines) < 3 || pipelines[0].AgentSlug != "" {
		t.Fatalf("unscoped pipeline summaries = %#v, %v; want the organization row first", pipelines, err)
	}
	for _, want := range []domain.AgentSlug{"refund-bot", "release-bot"} {
		if !slices.ContainsFunc(pipelines, func(summary domain.RequestApprovalPipelineSummary) bool { return summary.AgentSlug == want }) {
			t.Fatalf("unscoped pipeline summaries %#v lack %s", pipelines, want)
		}
	}

	// Teams list by scope and carry their assignments.
	teams, err := store.ListTeams(ctx, application.ListTeamsInput{
		OrganizationID: orgID, Scope: domain.ScopeSet{Scopes: []domain.Scope{{Kind: domain.ScopeTeam, ID: string(refunds.ID)}}},
	})
	if err != nil || len(teams) != 1 || teams[0].ID != refunds.ID {
		t.Fatalf("scoped teams = %#v, %v; want Refunds alone", teams, err)
	}
	if len(teams[0].Assignments) != 1 || teams[0].Assignments[0].RoleKey != domain.RoleAdmin ||
		teams[0].Assignments[0].ScopeKind != domain.ScopeAgent || teams[0].Assignments[0].ScopeID != "refund-bot" {
		t.Fatalf("Refunds assignments = %#v", teams[0].Assignments)
	}
	teams, err = store.ListTeams(ctx, application.ListTeamsInput{OrganizationID: orgID, Scope: everywhere})
	if err != nil || len(teams) != 2 {
		t.Fatalf("unscoped teams = %#v, %v; want both", teams, err)
	}
	for _, team := range teams {
		if team.Assignments == nil {
			t.Fatalf("team %s assignments are nil, want an empty list at least", team.Name)
		}
	}

	// Team assignments round-trip through ReplaceAssignments and show on
	// the detail; the agent-type row and an organization row coexist.
	replaced, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: refunds.ID,
		Assignments: []domain.AssignmentSpec{
			{RoleID: domain.RoleAdminID, ScopeKind: domain.ScopeAgent, ScopeID: "refund-bot"},
			organizationGrant(domain.RoleReviewerID),
		},
		ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil || len(replaced) != 2 {
		t.Fatalf("ReplaceAssignments(team) = %#v, %v", replaced, err)
	}
	detail, _, err := store.GetTeam(ctx, application.GetTeamInput{OrganizationID: orgID, TeamID: refunds.ID})
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	specs := make([]domain.AssignmentSpec, 0, len(detail.Assignments))
	for _, assignment := range detail.Assignments {
		specs = append(specs, assignment.Spec())
	}
	if !slices.Contains(specs, organizationGrant(domain.RoleReviewerID)) ||
		!slices.Contains(specs, domain.AssignmentSpec{RoleID: domain.RoleAdminID, ScopeKind: domain.ScopeAgent, ScopeID: "refund-bot"}) || len(specs) != 2 {
		t.Fatalf("GetTeam assignments = %#v", detail.Assignments)
	}

	// The path editor's warning: members of Releases lacking
	// request.decide at organization scope. The owner holds it, an
	// auditor does not, a suspended member is not counted, and a grant
	// pinned to a team or agent does not satisfy it.
	auditor := invite("auditor@scoped.example.com", []domain.AssignmentSpec{organizationGrant(domain.RoleAuditorID)})
	suspended := invite("suspended@scoped.example.com", []domain.AssignmentSpec{organizationGrant(domain.RoleAuditorID)})
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: suspended.MembershipID, Status: domain.MembershipSuspended,
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("SetMembershipStatus: %v", err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: releases.ID,
		MembershipIDs:     []domain.ID{ownerMembershipID, auditor.MembershipID, suspended.MembershipID},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("SetTeamManualMembers(releases): %v", err)
	}
	lacking, err := store.CountTeamMembersLacking(ctx, application.CountTeamMembersLackingInput{
		OrganizationID: orgID, TeamID: releases.ID, Permission: domain.PermissionRequestDecide,
	})
	if err != nil || lacking.MemberCount != 2 || lacking.LackingCount != 1 {
		t.Fatalf("CountTeamMembersLacking = %#v, %v; want 2 members, 1 lacking", lacking, err)
	}
	// Releases holds admin at release-bot: agent scope does not
	// count, an organization-scope reviewer grant does.
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: releases.ID,
		Assignments:       []domain.AssignmentSpec{{RoleID: domain.RoleAdminID, ScopeKind: domain.ScopeAgent, ScopeID: "release-bot"}},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("ReplaceAssignments(releases, agent): %v", err)
	}
	if lacking, err = store.CountTeamMembersLacking(ctx, application.CountTeamMembersLackingInput{
		OrganizationID: orgID, TeamID: releases.ID, Permission: domain.PermissionRequestDecide,
	}); err != nil || lacking.LackingCount != 1 {
		t.Fatalf("CountTeamMembersLacking with agent grant = %#v, %v; want 1 lacking", lacking, err)
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: releases.ID,
		Assignments:       []domain.AssignmentSpec{organizationGrant(domain.RoleReviewerID)},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("ReplaceAssignments(releases, reviewer): %v", err)
	}
	if lacking, err = store.CountTeamMembersLacking(ctx, application.CountTeamMembersLackingInput{
		OrganizationID: orgID, TeamID: releases.ID, Permission: domain.PermissionRequestDecide,
	}); err != nil || lacking.MemberCount != 2 || lacking.LackingCount != 0 {
		t.Fatalf("CountTeamMembersLacking with inherited grant = %#v, %v; want nobody lacking", lacking, err)
	}
	if lacking, err = store.CountTeamMembersLacking(ctx, application.CountTeamMembersLackingInput{
		OrganizationID: orgID, TeamID: domain.ID(uuid.NewString()), Permission: domain.PermissionRequestDecide,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("CountTeamMembersLacking(unknown team) = %#v, %v; want not found", lacking, err)
	}
	if _, err := store.CountTeamMembersLacking(ctx, application.CountTeamMembersLackingInput{
		OrganizationID: orgID, TeamID: releases.ID, Permission: "made.up",
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CountTeamMembersLacking(unknown permission) = %v; want invalid", err)
	}
}
