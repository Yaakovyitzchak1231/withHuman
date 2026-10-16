package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// teamPolicyDocument is a policy whose single level names the given
// memberships and, when broadcast is set, the whole team.
func teamPolicyDocument(broadcast bool, membershipIDs ...domain.ID) json.RawMessage {
	targets := make([]map[string]string, 0, len(membershipIDs)+1)
	for _, membershipID := range membershipIDs {
		targets = append(targets, map[string]string{"type": "user", "id": string(membershipID)})
	}
	if broadcast {
		targets = append(targets, map[string]string{"type": "broadcast"})
	}
	document, _ := json.Marshal(map[string]any{
		"working_hours": []any{},
		"nodes": []map[string]any{{
			"id": uuid.NewString(), "type": "level", "targets": targets, "escalate_after": "15m",
		}},
	})
	return document
}

func createTeamPolicyRevision(t *testing.T, ctx context.Context, store *Store, organizationID, teamID, membershipID domain.ID, document json.RawMessage, now time.Time) domain.TeamEscalationPolicyRevision {
	t.Helper()
	revision, err := store.CreateTeamEscalationPolicyRevision(ctx, application.CreateTeamEscalationPolicyRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: organizationID, TeamID: teamID,
		Document: document, DocumentVersion: 2, CreatedByActorID: membershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("create team policy revision: %v", err)
	}
	return revision
}

func activateTeamPolicyRevision(t *testing.T, ctx context.Context, store *Store, organizationID, teamID, membershipID domain.ID, revision int64, expected *int64, now time.Time) domain.TeamEscalationPolicyRevision {
	t.Helper()
	activated, err := store.ActivateTeamEscalationPolicyRevision(ctx, application.ActivateTeamEscalationPolicyRevisionInput{
		OrganizationID: organizationID, TeamID: teamID, Revision: revision,
		ExpectedActiveRevision: expected, ActorID: membershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("activate team policy revision %d: %v", revision, err)
	}
	return activated
}

// countAuditEvents counts the organization's events of one type and
// returns the data of the newest one.
func countAuditEvents(t *testing.T, ctx context.Context, store *Store, organizationID domain.ID, eventType string) (int, map[string]any) {
	t.Helper()
	var count int
	var data []byte
	if err := store.Pool().QueryRow(ctx, `SELECT count(*), (array_agg(data ORDER BY sequence DESC))[1]
		FROM test_audit_events WHERE organization_id = $1 AND event_type = $2`, organizationID, eventType).Scan(&count, &data); err != nil {
		t.Fatalf("count %s audit events: %v", eventType, err)
	}
	decoded := map[string]any{}
	if data != nil {
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("decode %s audit data: %v", eventType, err)
		}
	}
	return count, decoded
}

func TestTeamEscalationPoliciesAgainstRealPostgres(t *testing.T) {
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
	firstOrg, firstMembership := insertPipelineTestTenant(t, ctx, store, "team-policy-first", now)
	secondOrg, secondMembership := insertPipelineTestTenant(t, ctx, store, "team-policy-second", now)
	team, err := store.CreateTeam(ctx, application.CreateTeamInput{
		OrganizationID: firstOrg, Name: "payments", ActorMembershipID: firstMembership, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	otherTeam, err := store.CreateTeam(ctx, application.CreateTeamInput{
		OrganizationID: firstOrg, Name: "security", ActorMembershipID: firstMembership, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateTeam other: %v", err)
	}

	t.Run("create refuses an unknown team", func(t *testing.T) {
		if _, err := store.CreateTeamEscalationPolicyRevision(ctx, application.CreateTeamEscalationPolicyRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: firstOrg, TeamID: domain.ID(uuid.NewString()),
			Document: teamPolicyDocument(true), DocumentVersion: 2, CreatedByActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown team = %v, want ErrNotFound", err)
		}
		// A team of another organization is just as unknown.
		if _, err := store.CreateTeamEscalationPolicyRevision(ctx, application.CreateTeamEscalationPolicyRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: secondOrg, TeamID: team.ID,
			Document: teamPolicyDocument(true), DocumentVersion: 2, CreatedByActorID: secondMembership, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-tenant team = %v, want ErrNotFound", err)
		}
	})

	first := createTeamPolicyRevision(t, ctx, store, firstOrg, team.ID, firstMembership, teamPolicyDocument(true, firstMembership), now)
	if first.Revision != 1 || first.IsActive || first.TeamID != team.ID || first.OrganizationID != firstOrg {
		t.Fatalf("first revision = %+v, want inactive revision 1", first)
	}
	second := createTeamPolicyRevision(t, ctx, store, firstOrg, team.ID, firstMembership, teamPolicyDocument(false, firstMembership), now.Add(time.Second))
	if second.Revision != 2 {
		t.Fatalf("second revision number = %d, want 2", second.Revision)
	}
	if count, data := countAuditEvents(t, ctx, store, firstOrg, "team_escalation_policy.revision_created"); count != 2 ||
		data["team_id"] != string(team.ID) || data["revision"] != float64(2) {
		t.Fatalf("revision_created events = %d, %v", count, data)
	}

	t.Run("tenant isolation", func(t *testing.T) {
		if _, err := store.GetTeamEscalationPolicyRevision(ctx, application.GetTeamEscalationPolicyRevisionInput{
			OrganizationID: secondOrg, TeamID: team.ID, Revision: 1,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-tenant read = %v, want ErrNotFound", err)
		}
		revisions, err := store.ListTeamEscalationPolicyRevisions(ctx, application.ListTeamEscalationPolicyRevisionsInput{
			OrganizationID: secondOrg, TeamID: team.ID,
		})
		if err != nil || len(revisions) != 0 {
			t.Fatalf("cross-tenant list = %+v, %v", revisions, err)
		}
	})

	t.Run("append only records", func(t *testing.T) {
		assertSQLRejected(t, ctx, store, `DELETE FROM team_escalation_policies WHERE id = $1`,
			"append-only", first.ID)
		// A draft's document may change; a revision that was active may not.
		// The frozen team lives in the second organization, whose events no
		// later count reads.
		frozenTeam, err := store.CreateTeam(ctx, application.CreateTeamInput{
			OrganizationID: secondOrg, Name: "frozen", ActorMembershipID: secondMembership, Now: now,
		})
		if err != nil {
			t.Fatalf("CreateTeam frozen: %v", err)
		}
		frozen := createTeamPolicyRevision(t, ctx, store, secondOrg, frozenTeam.ID, secondMembership, teamPolicyDocument(true), now)
		if _, err := store.ActivateTeamEscalationPolicyRevision(ctx, application.ActivateTeamEscalationPolicyRevisionInput{
			OrganizationID: secondOrg, TeamID: frozenTeam.ID, Revision: frozen.Revision, ActorID: secondMembership, Now: now,
		}); err != nil {
			t.Fatalf("activate the frozen policy: %v", err)
		}
		assertSQLRejected(t, ctx, store, `UPDATE team_escalation_policies SET document = '{"nodes":[]}' WHERE id = $1`,
			"only a draft team escalation policy revision may change its content", frozen.ID)
		assertSQLRejected(t, ctx, store, `UPDATE team_escalation_policies SET revision = 99 WHERE id = $1`,
			"identity is immutable", first.ID)
		assertSQLRejected(t, ctx, store, `UPDATE team_escalation_policies SET is_active = is_active WHERE id = $1`,
			"must change is_active", first.ID)
	})

	t.Run("monotonic revisions", func(t *testing.T) {
		assertSQLRejected(t, ctx, store, `INSERT INTO team_escalation_policies
			(id, organization_id, team_id, revision, document, document_version, created_by_actor_id, created_at)
			VALUES ($1, $2, $3, 9, '{}', 2, $4, $5)`,
			"revision must be", uuid.NewString(), firstOrg, team.ID, firstMembership, now)
	})

	t.Run("activation preconditions", func(t *testing.T) {
		activated := activateTeamPolicyRevision(t, ctx, store, firstOrg, team.ID, firstMembership, 1, nil, now)
		if !activated.IsActive || activated.Revision != 1 {
			t.Fatalf("activated = %+v", activated)
		}
		stale := int64(2)
		if _, err := store.ActivateTeamEscalationPolicyRevision(ctx, application.ActivateTeamEscalationPolicyRevisionInput{
			OrganizationID: firstOrg, TeamID: team.ID, Revision: 2,
			ExpectedActiveRevision: &stale, ActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrPreconditionFailed) {
			t.Fatalf("stale activation = %v, want ErrPreconditionFailed", err)
		}
		if _, err := store.ActivateTeamEscalationPolicyRevision(ctx, application.ActivateTeamEscalationPolicyRevisionInput{
			OrganizationID: firstOrg, TeamID: team.ID, Revision: 2,
			ExpectedActiveRevision: nil, ActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrPreconditionFailed) {
			t.Fatalf("activation expecting none = %v, want ErrPreconditionFailed", err)
		}
		if _, err := store.ActivateTeamEscalationPolicyRevision(ctx, application.ActivateTeamEscalationPolicyRevisionInput{
			OrganizationID: firstOrg, TeamID: team.ID, Revision: 7,
			ActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrPreconditionFailed) {
			t.Fatalf("activation of a missing revision = %v", err)
		}
		// The partial unique index is the last line of defence for one
		// active revision per team.
		assertSQLRejected(t, ctx, store, `UPDATE team_escalation_policies SET is_active = true WHERE id = $1`,
			"team_escalation_policies_active_revision_idx", second.ID)
		expected := int64(1)
		switched := activateTeamPolicyRevision(t, ctx, store, firstOrg, team.ID, firstMembership, 2, &expected, now)
		if !switched.IsActive || switched.Revision != 2 {
			t.Fatalf("switched = %+v", switched)
		}
		active, err := store.GetActiveTeamEscalationPolicyRevision(ctx, application.GetActiveTeamEscalationPolicyRevisionInput{
			OrganizationID: firstOrg, TeamID: team.ID,
		})
		if err != nil || active.Revision != 2 || active.ID != second.ID {
			t.Fatalf("active = %+v, %v, want revision 2", active, err)
		}
		listed, err := store.ListTeamEscalationPolicyRevisions(ctx, application.ListTeamEscalationPolicyRevisionsInput{
			OrganizationID: firstOrg, TeamID: team.ID,
		})
		if err != nil || len(listed) != 2 || listed[0].Revision != 2 || !listed[0].IsActive || listed[1].IsActive {
			t.Fatalf("listed = %+v, %v", listed, err)
		}
		if count, data := countAuditEvents(t, ctx, store, firstOrg, "team_escalation_policy.activated"); count != 2 ||
			data["team_id"] != string(team.ID) || data["revision"] != float64(2) || data["previous_active_revision"] != float64(1) {
			t.Fatalf("activated events = %d, %v", count, data)
		}
	})

	t.Run("active policies of many teams", func(t *testing.T) {
		revisions, err := store.GetActiveTeamEscalationPolicies(ctx, application.GetActiveTeamEscalationPoliciesInput{
			OrganizationID: firstOrg, TeamIDs: []domain.ID{team.ID, otherTeam.ID, domain.ID(uuid.NewString())},
		})
		if err != nil || len(revisions) != 1 || revisions[0].ID != second.ID || revisions[0].TeamID != team.ID {
			t.Fatalf("active policies = %+v, %v, want only the payments policy", revisions, err)
		}
		revisions, err = store.GetActiveTeamEscalationPolicies(ctx, application.GetActiveTeamEscalationPoliciesInput{
			OrganizationID: secondOrg, TeamIDs: []domain.ID{team.ID},
		})
		if err != nil || len(revisions) != 0 {
			t.Fatalf("cross-tenant active policies = %+v, %v", revisions, err)
		}
		revisions, err = store.GetActiveTeamEscalationPolicies(ctx, application.GetActiveTeamEscalationPoliciesInput{
			OrganizationID: firstOrg,
		})
		if err != nil || len(revisions) != 0 {
			t.Fatalf("active policies of no teams = %+v, %v", revisions, err)
		}
	})

	t.Run("deactivation", func(t *testing.T) {
		if _, err := store.DeactivateTeamEscalationPolicy(ctx, application.DeactivateTeamEscalationPolicyInput{
			OrganizationID: firstOrg, TeamID: team.ID, ExpectedActiveRevision: 1,
			ActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrPreconditionFailed) {
			t.Fatalf("stale deactivation = %v, want ErrPreconditionFailed", err)
		}
		archived, err := store.DeactivateTeamEscalationPolicy(ctx, application.DeactivateTeamEscalationPolicyInput{
			OrganizationID: firstOrg, TeamID: team.ID, ExpectedActiveRevision: 2,
			ActorID: firstMembership, Now: now,
		})
		if err != nil || archived.IsActive || archived.Revision != 2 {
			t.Fatalf("archived = %+v, %v", archived, err)
		}
		if _, err := store.GetActiveTeamEscalationPolicyRevision(ctx, application.GetActiveTeamEscalationPolicyRevisionInput{
			OrganizationID: firstOrg, TeamID: team.ID,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("active after archiving = %v, want ErrNotFound", err)
		}
		count, data := countAuditEvents(t, ctx, store, firstOrg, "team_escalation_policy.deactivated")
		if count != 1 || data["team_id"] != string(team.ID) || data["revision"] != float64(2) {
			t.Fatalf("deactivated events = %d, %v", count, data)
		}
		var subjectType, subjectID, actorID string
		if err := store.Pool().QueryRow(ctx, `SELECT subject_type, subject_id::text, actor_id::text FROM test_audit_events
			WHERE organization_id = $1 AND event_type = 'team_escalation_policy.deactivated'`, firstOrg).Scan(&subjectType, &subjectID, &actorID); err != nil {
			t.Fatalf("read deactivated event: %v", err)
		}
		var userID string
		if err := store.Pool().QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE id = $1`, firstMembership).Scan(&userID); err != nil {
			t.Fatalf("resolve actor user: %v", err)
		}
		if subjectType != "team_escalation_policy" || subjectID != string(second.ID) || actorID != userID {
			t.Fatalf("deactivated event subject = %s %s actor %s", subjectType, subjectID, actorID)
		}
	})
}
