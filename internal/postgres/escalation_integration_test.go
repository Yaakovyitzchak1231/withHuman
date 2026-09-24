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
	"github.com/withHumanAI/withHuman/routing"
)

// insertEscalationTestMember admits one more active member the way
// insertPipelineTestTenant seeds the first: straight into the tables.
func insertEscalationTestMember(t *testing.T, ctx context.Context, store *Store, organizationID domain.ID, key string, now time.Time) domain.ID {
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
	return membershipID
}

// escalationTestAgent enrolls an agent instance so requests can be
// created against it.
func escalationTestAgent(t *testing.T, ctx context.Context, store *Store, organizationID, membershipID domain.ID, key string, now time.Time) (domain.Agent, domain.AgentInstance) {
	t.Helper()
	var userID domain.ID
	if err := store.Pool().QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE id = $1`,
		membershipID).Scan(&userID); err != nil {
		t.Fatalf("resolve membership user: %v", err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{
		OrganizationID: organizationID, ActorID: membershipID, Name: key + "-agent",
		AgentSlug: "billing", AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	secret := sha256.Sum256([]byte(key + "-secret"))
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
		OrganizationID: organizationID, AgentID: agent.ID, ActorID: userID, Name: key + "-instance",
		Metadata: json.RawMessage(`{}`),
		Credential: application.AgentCredentialRecord{
			ID: domain.ID(uuid.NewString()), SecretHash: secret[:],
			IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgentInstanceWithCredential: %v", err)
	}
	return agent, instance
}

func escalationTestRequest(organizationID domain.ID, agent domain.Agent, instance domain.AgentInstance, key string, now time.Time) domain.ApprovalRequest {
	return domain.ApprovalRequest{
		Timeout: "1h",
		ID:      domain.ID(uuid.NewString()), OrganizationID: organizationID,
		AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
		Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount":1}`),
		IdempotencyKey: key, SemanticHash: "hash-" + key,
		Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestEscalationAgainstRealPostgres(t *testing.T) {
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
	orgID, owner := insertPipelineTestTenant(t, ctx, store, "escalation", now)
	alice := insertEscalationTestMember(t, ctx, store, orgID, "alice", now)
	bob := insertEscalationTestMember(t, ctx, store, orgID, "bob", now)
	carol := insertEscalationTestMember(t, ctx, store, orgID, "carol", now)
	team, err := store.CreateTeam(ctx, application.CreateTeamInput{
		OrganizationID: orgID, Name: "payments", ActorMembershipID: owner, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	// Alice and Carol are on the team; Bob is named by the policy but is
	// not a member.
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{alice, carol},
		ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatalf("SetTeamManualMembers: %v", err)
	}
	policyDocument := teamPolicyDocument(true, alice, bob)
	policy := createTeamPolicyRevision(t, ctx, store, orgID, team.ID, owner, policyDocument, now)
	activateTeamPolicyRevision(t, ctx, store, orgID, team.ID, owner, policy.Revision, nil, now)

	// The path names only the owner; the team enters through the pin so
	// the pin's own archive guard is what the test exercises later.
	pathDocument := userTargetPathDocument(owner)
	path := createRoutingTestPathRevision(t, ctx, store, orgID, owner, "payments", pathDocument, now)
	if _, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
		OrganizationID: orgID, PathKey: "payments", Revision: path.Revision, ActorID: owner, Now: now,
	}); err != nil {
		t.Fatalf("activate path: %v", err)
	}
	agent, instance := escalationTestAgent(t, ctx, store, orgID, owner, "escalation", now)

	request := escalationTestRequest(orgID, agent, instance, "escalation-req-1", now)
	initialization := pendingApprovalInitialization(request)
	initialization.Routing = &domain.ApprovalRequestRouting{
		RequestID: request.ID, OrganizationID: orgID,
		Outcome: domain.RoutingEscalated, Reason: domain.RoutingReasonPipelineDefault,
		EscalationPathKey: "payments", EscalationPathRevisionID: path.ID, Urgency: "standard",
		TeamPolicies: []domain.ApprovalRoutingTeamPolicy{{TeamID: team.ID, PolicyRevisionID: policy.ID, Revision: policy.Revision}},
		CreatedAt:    now,
	}
	if _, err := store.InitializeApproval(ctx, initialization); err != nil {
		t.Fatalf("InitializeApproval with team pins: %v", err)
	}

	t.Run("pins persist with the request", func(t *testing.T) {
		var pinnedRevisionID string
		var createdAt time.Time
		if err := store.Pool().QueryRow(ctx, `SELECT policy_revision_id::text, created_at FROM approval_request_routing_team_policies
			WHERE organization_id = $1 AND request_id = $2 AND team_id = $3`, orgID, request.ID, team.ID).Scan(&pinnedRevisionID, &createdAt); err != nil {
			t.Fatalf("read pin row: %v", err)
		}
		if pinnedRevisionID != string(policy.ID) || !createdAt.Equal(now) {
			t.Fatalf("pin row = %s at %s", pinnedRevisionID, createdAt)
		}
		assertSQLRejected(t, ctx, store, `DELETE FROM approval_request_routing_team_policies WHERE request_id = $1`,
			"append-only", request.ID)
		// The three-column foreign key refuses pinning a revision of a
		// different team.
		otherTeam, err := store.CreateTeam(ctx, application.CreateTeamInput{
			OrganizationID: orgID, Name: "security", ActorMembershipID: owner, Now: now,
		})
		if err != nil {
			t.Fatalf("CreateTeam other: %v", err)
		}
		other := escalationTestRequest(orgID, agent, instance, "escalation-req-mismatch", now)
		mismatched := pendingApprovalInitialization(other)
		mismatched.Routing = &domain.ApprovalRequestRouting{
			RequestID: other.ID, OrganizationID: orgID,
			Outcome: domain.RoutingEscalated, Reason: domain.RoutingReasonPipelineDefault,
			EscalationPathKey: "payments", EscalationPathRevisionID: path.ID, Urgency: "standard",
			TeamPolicies: []domain.ApprovalRoutingTeamPolicy{{TeamID: otherTeam.ID, PolicyRevisionID: policy.ID, Revision: policy.Revision}},
			CreatedAt:    now,
		}
		if _, err := store.InitializeApproval(ctx, mismatched); err == nil {
			t.Fatal("pinning another team's policy revision was accepted")
		}
	})

	t.Run("program loads both halves", func(t *testing.T) {
		program, err := store.LoadEscalationProgram(ctx, application.LoadEscalationProgramInput{
			OrganizationID: orgID, RequestID: request.ID,
		})
		if err != nil || !program.Escalated || program.Urgency != "standard" || program.PathDocumentVersion != 2 {
			t.Fatalf("program = %+v, %v", program, err)
		}
		var pathParsed routing.PathDocument
		if err := routing.Decode(program.PathDocument, &pathParsed); err != nil || len(pathParsed.Nodes) != 1 {
			t.Fatalf("path document = %s, %v", program.PathDocument, err)
		}
		if len(program.TeamPolicies) != 1 || program.TeamPolicies[0].TeamID != team.ID || program.TeamPolicies[0].DocumentVersion != 2 {
			t.Fatalf("team policies = %+v", program.TeamPolicies)
		}
		var policyParsed routing.TeamPolicyDocument
		if err := routing.Decode(program.TeamPolicies[0].Document, &policyParsed); err != nil || !policyParsed.Broadcasts() {
			t.Fatalf("team policy document = %s, %v", program.TeamPolicies[0].Document, err)
		}

		unrouted := escalationTestRequest(orgID, agent, instance, "escalation-unrouted", now)
		if _, err := store.InitializeApproval(ctx, pendingApprovalInitialization(unrouted)); err != nil {
			t.Fatalf("InitializeApproval unrouted: %v", err)
		}
		program, err = store.LoadEscalationProgram(ctx, application.LoadEscalationProgramInput{
			OrganizationID: orgID, RequestID: unrouted.ID,
		})
		if err != nil || program.Escalated || program.PathDocument != nil || len(program.TeamPolicies) != 0 {
			t.Fatalf("unrouted program = %+v, %v", program, err)
		}
	})

	t.Run("level recipients honour the team boundary", func(t *testing.T) {
		resolve := func(entries ...application.LevelEntry) []domain.ID {
			recipients, err := store.ResolveLevelRecipients(ctx, application.ResolveLevelRecipientsInput{
				OrganizationID: orgID, RequestID: request.ID, Urgency: domain.NotificationInterrupt, Entries: entries,
			})
			if err != nil || recipients.Outcome != domain.RoutingEscalated || recipients.Urgency != domain.NotificationInterrupt {
				t.Fatalf("recipients = %+v, %v", recipients, err)
			}
			for _, recipient := range recipients.Recipients {
				if !recipient.Reachable() || len(recipient.Methods) == 0 {
					t.Fatalf("recipient carries no address: %+v", recipient)
				}
			}
			ids := recipients.MembershipIDs()
			slices.Sort(ids)
			return ids
		}
		sorted := func(ids ...domain.ID) []domain.ID {
			slices.Sort(ids)
			return ids
		}
		// An organization level reaches its users and every member of its
		// team targets.
		if got := resolve(application.LevelEntry{Targets: []routing.Target{
			{Type: routing.TargetUser, ID: string(owner)}, {Type: routing.TargetTeam, ID: string(team.ID)},
		}}); !slices.Equal(got, sorted(owner, alice, carol)) {
			t.Fatalf("organization level resolved to %v", got)
		}
		// A team level reaches a named user only while they are on the
		// team: Bob is named but dropped.
		if got := resolve(application.LevelEntry{TeamID: team.ID, Targets: []routing.Target{
			{Type: routing.TargetUser, ID: string(alice)}, {Type: routing.TargetUser, ID: string(bob)},
		}}); !slices.Equal(got, sorted(alice)) {
			t.Fatalf("team level resolved to %v, want only alice", got)
		}
		// Broadcast is every current member, deduplicated with the named
		// ones.
		if got := resolve(application.LevelEntry{TeamID: team.ID, Targets: []routing.Target{
			{Type: routing.TargetUser, ID: string(alice)}, {Type: routing.TargetBroadcast},
		}}); !slices.Equal(got, sorted(alice, carol)) {
			t.Fatalf("broadcast level resolved to %v", got)
		}
		if got := resolve(); len(got) != 0 {
			t.Fatalf("no entries resolved to %v", got)
		}
	})

	tick := func(sequence int64, teams []application.EscalationWalkerProgress, at time.Time, events ...domain.AuditEvent) application.EscalationProgressResult {
		result, err := store.RecordEscalationProgress(ctx, application.RecordEscalationProgressInput{
			OrganizationID: orgID, RequestID: request.ID,
			Progress: application.EscalationProgress{
				Sequence: sequence, Urgency: routing.UrgencyStandard,
				Organization: application.EscalationWalkerProgress{
					Phase: application.EscalationPhaseLevel, CurrentNodeID: uuid.NewString(),
					LevelOrdinal: 1, LevelCount: 1, NextAt: at.Add(15 * time.Minute),
				},
				Teams: teams,
			},
			Events: events, Now: at,
		})
		if err != nil {
			t.Fatalf("RecordEscalationProgress %d: %v", sequence, err)
		}
		return result
	}
	levelEvent := func(sequence int64) domain.AuditEvent {
		data, _ := json.Marshal(map[string]any{"sequence": sequence})
		return domain.AuditEvent{EventType: "escalation.level_entered", ActorType: domain.ActorSystem, Data: data}
	}

	t.Run("progress applies once per sequence", func(t *testing.T) {
		firstTick := now.Add(time.Minute)
		if result := tick(1, []application.EscalationWalkerProgress{{
			TeamID: team.ID, Phase: application.EscalationPhaseLevel, LevelOrdinal: 1, LevelCount: 2,
		}}, firstTick, levelEvent(1)); !result.Applied {
			t.Fatal("first tick was not applied")
		}
		var updatedAt time.Time
		if err := store.Pool().QueryRow(ctx, `SELECT updated_at FROM approval_requests WHERE id = $1`, request.ID).Scan(&updatedAt); err != nil {
			t.Fatal(err)
		}
		if !updatedAt.Equal(firstTick) {
			t.Fatalf("request updated_at = %s, want the tick time %s", updatedAt, firstTick)
		}
		count, data := countAuditEvents(t, ctx, store, orgID, "escalation.level_entered")
		if count != 1 || data["sequence"] != float64(1) {
			t.Fatalf("level events = %d, %v", count, data)
		}
		var subjectType, subjectID string
		if err := store.Pool().QueryRow(ctx, `SELECT subject_type, subject_id::text FROM test_audit_events
			WHERE organization_id = $1 AND event_type = 'escalation.level_entered'`, orgID).Scan(&subjectType, &subjectID); err != nil {
			t.Fatal(err)
		}
		if subjectType != "approval_request" || subjectID != string(request.ID) {
			t.Fatalf("event subject = %s %s, want the request", subjectType, subjectID)
		}

		// The same sequence again, and an older one, are stale retries.
		if result := tick(1, nil, firstTick.Add(time.Second), levelEvent(1)); result.Applied {
			t.Fatal("duplicate tick was applied")
		}
		if result := tick(0, nil, firstTick.Add(time.Second), levelEvent(0)); result.Applied {
			t.Fatal("older tick was applied")
		}
		if count, _ := countAuditEvents(t, ctx, store, orgID, "escalation.level_entered"); count != 1 {
			t.Fatalf("stale ticks appended events: %d", count)
		}
		var teamRows int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM approval_request_escalation_team_state WHERE request_id = $1`, request.ID).Scan(&teamRows); err != nil {
			t.Fatal(err)
		}
		if teamRows != 1 {
			t.Fatalf("team state rows = %d after stale ticks, want the first tick's row", teamRows)
		}

		// A newer sequence rewrites the team rows.
		secondTick := firstTick.Add(15 * time.Minute)
		if result := tick(2, []application.EscalationWalkerProgress{{
			TeamID: team.ID, Phase: application.EscalationPhaseExhausted, LevelOrdinal: 2, LevelCount: 2,
		}}, secondTick, levelEvent(2)); !result.Applied {
			t.Fatal("second tick was not applied")
		}
		view, err := store.GetApprovalRouting(ctx, application.GetApprovalRoutingInput{OrganizationID: orgID, RequestID: request.ID})
		if err != nil {
			t.Fatalf("GetApprovalRouting: %v", err)
		}
		if len(view.PinnedTeamPolicies) != 1 || view.PinnedTeamPolicies[0].TeamID != team.ID ||
			view.PinnedTeamPolicies[0].TeamName != "payments" || view.PinnedTeamPolicies[0].PolicyRevisionID != policy.ID ||
			view.PinnedTeamPolicies[0].Revision != policy.Revision {
			t.Fatalf("pinned team policies = %+v", view.PinnedTeamPolicies)
		}
		if len(view.TeamPolicies) != 1 || view.TeamPolicies[0].PolicyRevisionID != policy.ID {
			t.Fatalf("snapshot team policies = %+v", view.TeamPolicies)
		}
		if view.Escalation == nil || view.Escalation.Sequence != 2 || view.Escalation.Urgency != routing.UrgencyStandard ||
			view.Escalation.Organization.Phase != application.EscalationPhaseLevel ||
			view.Escalation.Organization.CurrentNodeID == "" || view.Escalation.Organization.LevelCount != 1 ||
			!view.Escalation.Organization.NextAt.Equal(secondTick.Add(15*time.Minute)) || !view.Escalation.UpdatedAt.Equal(secondTick) {
			t.Fatalf("escalation progress = %+v", view.Escalation)
		}
		if len(view.Escalation.Teams) != 1 || view.Escalation.Teams[0].TeamID != team.ID ||
			view.Escalation.Teams[0].Phase != application.EscalationPhaseExhausted || view.Escalation.Teams[0].LevelOrdinal != 2 ||
			!view.Escalation.Teams[0].NextAt.IsZero() || view.Escalation.Teams[0].CurrentNodeID != "" {
			t.Fatalf("team progress = %+v", view.Escalation.Teams)
		}
		if count, _ := countAuditEvents(t, ctx, store, orgID, "escalation.level_entered"); count != 2 {
			t.Fatalf("level events after second tick = %d", count)
		}
	})

	t.Run("routing view without progress", func(t *testing.T) {
		other := escalationTestRequest(orgID, agent, instance, "escalation-req-2", now)
		initialization := pendingApprovalInitialization(other)
		initialization.Routing = &domain.ApprovalRequestRouting{
			RequestID: other.ID, OrganizationID: orgID,
			Outcome: domain.RoutingEscalated, Reason: domain.RoutingReasonPipelineDefault,
			EscalationPathKey: "payments", EscalationPathRevisionID: path.ID, Urgency: "standard", CreatedAt: now,
		}
		if _, err := store.InitializeApproval(ctx, initialization); err != nil {
			t.Fatalf("InitializeApproval: %v", err)
		}
		view, err := store.GetApprovalRouting(ctx, application.GetApprovalRoutingInput{OrganizationID: orgID, RequestID: other.ID})
		if err != nil || view.Escalation != nil || len(view.PinnedTeamPolicies) != 0 {
			t.Fatalf("view = %+v, %v", view, err)
		}
		if _, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{
			OrganizationID: orgID, RequestID: other.ID, Now: now.Add(2 * time.Hour),
		}); err != nil {
			t.Fatalf("expire: %v", err)
		}
	})

	t.Run("pinned policy guards the team, then the terminal request releases it", func(t *testing.T) {
		if err := store.ArchiveTeam(ctx, application.ArchiveTeamInput{
			OrganizationID: orgID, TeamID: team.ID, ActorMembershipID: owner, Now: now,
		}); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("archive with a pending pinned policy = %v, want ErrConflict", err)
		}
		if _, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{
			OrganizationID: orgID, RequestID: request.ID, Now: now.Add(2 * time.Hour),
		}); err != nil {
			t.Fatalf("expire request: %v", err)
		}
		if result := tick(3, nil, now.Add(2*time.Hour), levelEvent(3)); result.Applied {
			t.Fatal("tick on a terminal request was applied")
		}
		if count, _ := countAuditEvents(t, ctx, store, orgID, "escalation.level_entered"); count != 2 {
			t.Fatalf("terminal tick appended an event: %d", count)
		}
		if err := store.ArchiveTeam(ctx, application.ArchiveTeamInput{
			OrganizationID: orgID, TeamID: team.ID, ActorMembershipID: owner, Now: now,
		}); err != nil {
			t.Fatalf("archive after expiry: %v", err)
		}
		// Pins and the team remain readable as history; the policy is archived.
		var isActive bool
		if err := store.Pool().QueryRow(ctx, `SELECT is_active FROM team_escalation_policies WHERE id = $1`, policy.ID).Scan(&isActive); err != nil {
			t.Fatal(err)
		}
		if isActive {
			t.Fatal("archived team's policy is still active")
		}
		view, err := store.GetApprovalRouting(ctx, application.GetApprovalRoutingInput{OrganizationID: orgID, RequestID: request.ID})
		if err != nil || len(view.PinnedTeamPolicies) != 1 || view.PinnedTeamPolicies[0].TeamName != team.Name {
			t.Fatalf("view after team archive = %+v, %v", view, err)
		}
	})
}
