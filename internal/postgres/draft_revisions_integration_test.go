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
	"github.com/withHumanAI/withHuman/internal/secrets"
)

// A draft revision of every revisioned resource changes in place and
// keeps its number until it is first activated; from then on it is frozen
// by the store and by the table's trigger, even after another revision
// replaces it.
func TestDraftRevisionsAgainstRealPostgres(t *testing.T) {
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
	sealer, err := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	store.WithSealer(sealer)
	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID, owner := insertPipelineTestTenant(t, ctx, store, "drafts", now)
	updatedEvents := func(eventType string, subjectID domain.ID) int {
		t.Helper()
		return len(auditEvents(t, ctx, store, `event_type = $1 AND subject_id = $2`, eventType, subjectID))
	}

	t.Run("pipeline", func(t *testing.T) {
		seedPipelineTestScope(t, ctx, store, orgID, owner, domain.EntryPipelineKey, now)
		active, err := store.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
			OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey,
		})
		if err != nil || active.ActivatedAt == nil {
			t.Fatalf("the seeded entry revision is active from birth: %+v %v", active, err)
		}
		draft := createPipelineTestRevision(t, ctx, store, orgID, owner, domain.EntryPipelineKey,
			[]application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("approve-all", `{"outcome":"approve"}`)}, now)
		if draft.ActivatedAt != nil {
			t.Fatalf("a new revision is a draft: %+v", draft)
		}
		update := application.UpdateRequestApprovalPipelineRevisionInput{
			OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Revision: draft.Revision, Name: "Renamed",
			Blocks: []application.RequestApprovalBlockSnapshotInput{
				pipelineTestBlock("deny-all", `{"outcome":"deny"}`),
				pipelineTestBlock("approve-all", `{"outcome":"approve"}`),
			},
			DefaultEscalationPath: "", ActorID: owner, Now: now.Add(time.Minute),
		}
		updated, err := store.UpdateRequestApprovalPipelineRevision(ctx, update)
		if err != nil || updated.Revision != draft.Revision || updated.ID != draft.ID || updated.Name != "Renamed" || len(updated.BlockIDs) != 2 {
			t.Fatalf("UpdateRequestApprovalPipelineRevision: %+v %v", updated, err)
		}
		read, err := store.GetRequestApprovalPipelineRevision(ctx, application.GetRequestApprovalPipelineRevisionInput{
			OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Revision: draft.Revision,
		})
		if err != nil || read.Name != "Renamed" || len(read.BlockIDs) != 2 || read.BlockIDs[0] != updated.BlockIDs[0] || !read.CreatedAt.Equal(draft.CreatedAt) {
			t.Fatalf("the draft reads back changed in place: %+v %v", read, err)
		}
		if count := updatedEvents("request_approval_pipeline.revision_updated", draft.ID); count != 1 {
			t.Fatalf("revision_updated events = %d, want 1", count)
		}
		// The same document again writes nothing.
		update.Blocks = []application.RequestApprovalBlockSnapshotInput{
			pipelineTestBlock("deny-all", `{"outcome":"deny"}`),
			pipelineTestBlock("approve-all", `{"outcome":"approve"}`),
		}
		if _, err := store.UpdateRequestApprovalPipelineRevision(ctx, update); err != nil {
			t.Fatalf("unchanged update: %v", err)
		}
		if count := updatedEvents("request_approval_pipeline.revision_updated", draft.ID); count != 1 {
			t.Fatalf("an unchanged update was audited: %d events", count)
		}

		activated, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
			OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Revision: draft.Revision,
			ExpectedActiveRevision: active.Revision, ActorID: owner, Now: now.Add(2 * time.Minute),
		})
		if err != nil || activated.ActivatedAt == nil {
			t.Fatalf("activation stamps the draft: %+v %v", activated, err)
		}
		if _, err := store.UpdateRequestApprovalPipelineRevision(ctx, update); !errors.Is(err, domain.ErrRevisionNotDraft) {
			t.Fatalf("update of the active revision = %v, want ErrRevisionNotDraft", err)
		}
		// Rolling back leaves the once-active revision frozen.
		if _, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
			OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Revision: active.Revision,
			ExpectedActiveRevision: draft.Revision, ActorID: owner, Now: now.Add(3 * time.Minute),
		}); err != nil {
			t.Fatalf("roll back: %v", err)
		}
		if _, err := store.UpdateRequestApprovalPipelineRevision(ctx, update); !errors.Is(err, domain.ErrRevisionNotDraft) {
			t.Fatalf("update of a once-active revision = %v, want ErrRevisionNotDraft", err)
		}
		assertSQLRejected(t, ctx, store, `UPDATE request_approval_pipelines SET name = 'Sneaky' WHERE id = $1`,
			"only a draft request approval pipeline revision may change its content", draft.ID)
		assertSQLRejected(t, ctx, store, `UPDATE request_approval_pipelines SET activated_at = NULL WHERE id = $1`,
			"must change active state or archive state", draft.ID)
		if _, err := store.UpdateRequestApprovalPipelineRevision(ctx, application.UpdateRequestApprovalPipelineRevisionInput{
			OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Revision: 99, Name: "Missing", ActorID: owner, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("update of a missing revision = %v, want ErrNotFound", err)
		}
	})

	t.Run("escalation path", func(t *testing.T) {
		first := createRoutingTestPathRevision(t, ctx, store, orgID, owner, "on-call", routingTestPathDocument(), now)
		if first.ActivatedAt != nil {
			t.Fatalf("a new path's first revision is a draft: %+v", first)
		}
		update := application.UpdateEscalationPathRevisionInput{
			OrganizationID: orgID, PathKey: "on-call", Revision: first.Revision, Name: "On call",
			Document: routingTestPathDocument(), DocumentVersion: 2, ActorID: owner, Now: now.Add(time.Minute),
		}
		updated, err := store.UpdateEscalationPathRevision(ctx, update)
		if err != nil || updated.Revision != 1 || updated.Name != "On call" {
			t.Fatalf("UpdateEscalationPathRevision: %+v %v", updated, err)
		}
		if _, err := store.UpdateEscalationPathRevision(ctx, update); err != nil {
			t.Fatalf("unchanged update: %v", err)
		}
		if count := updatedEvents("escalation_path.revision_updated", first.ID); count != 1 {
			t.Fatalf("revision_updated events = %d, want 1", count)
		}
		activated, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
			OrganizationID: orgID, PathKey: "on-call", Revision: 1, ActorID: owner, Now: now.Add(2 * time.Minute),
		})
		if err != nil || activated.ActivatedAt == nil || activated.Name != "On call" {
			t.Fatalf("activation stamps the draft: %+v %v", activated, err)
		}
		if _, err := store.UpdateEscalationPathRevision(ctx, update); !errors.Is(err, domain.ErrRevisionNotDraft) {
			t.Fatalf("update of the active revision = %v, want ErrRevisionNotDraft", err)
		}
		assertSQLRejected(t, ctx, store, `UPDATE escalation_paths SET name = 'Sneaky' WHERE id = $1`,
			"only a draft escalation path revision may change its content", first.ID)
		// A newer revision is a draft again; archiving the path freezes it.
		second := createRoutingTestPathRevision(t, ctx, store, orgID, owner, "on-call", routingTestPathDocument(), now)
		update.Revision = second.Revision
		if _, err := store.UpdateEscalationPathRevision(ctx, update); err != nil {
			t.Fatalf("update of the newer draft: %v", err)
		}
		if _, err := store.ArchiveEscalationPath(ctx, application.ArchiveEscalationPathInput{
			OrganizationID: orgID, PathKey: "on-call", ActorID: owner, Now: now.Add(3 * time.Minute),
		}); err != nil {
			t.Fatalf("ArchiveEscalationPath: %v", err)
		}
		if _, err := store.UpdateEscalationPathRevision(ctx, update); !errors.Is(err, domain.ErrEscalationPathArchived) {
			t.Fatalf("update of an archived draft = %v, want ErrEscalationPathArchived", err)
		}
	})

	t.Run("webhook endpoint", func(t *testing.T) {
		const key = "fraud"
		sealed, err := sealer.Seal(application.WebhookSigningSecretLabel(orgID, key), "whsec_first")
		if err != nil {
			t.Fatal(err)
		}
		created, err := store.CreateWebhookEndpointRevision(ctx, application.CreateWebhookEndpointRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, EndpointKey: key, Name: "Fraud",
			URL: "https://fraud.example.com/a", SealedSigningSecret: sealed, CreatedByActorID: owner, Now: now,
		})
		if err != nil || created.Revision.ActivatedAt != nil {
			t.Fatalf("CreateWebhookEndpointRevision: %+v %v", created, err)
		}
		update := application.UpdateWebhookEndpointRevisionInput{
			OrganizationID: orgID, EndpointKey: key, Revision: 1, Name: "Fraud check",
			URL: "https://fraud.example.com/b", ActorID: owner, Now: now.Add(time.Minute),
		}
		updated, err := store.UpdateWebhookEndpointRevision(ctx, update)
		if err != nil || updated.Revision != 1 || updated.URL != "https://fraud.example.com/b" || updated.Name != "Fraud check" {
			t.Fatalf("UpdateWebhookEndpointRevision: %+v %v", updated, err)
		}
		secret, err := store.OpenWebhookSigningSecret(ctx, application.OpenWebhookSigningSecretInput{OrganizationID: orgID, EndpointKey: key})
		if err != nil || secret != "whsec_first" {
			t.Fatalf("the key keeps its secret: %q %v", secret, err)
		}
		activated, err := store.ActivateWebhookEndpointRevision(ctx, application.ActivateWebhookEndpointRevisionInput{
			OrganizationID: orgID, EndpointKey: key, Revision: 1, ActorID: owner, Now: now.Add(2 * time.Minute),
		})
		if err != nil || activated.ActivatedAt == nil {
			t.Fatalf("activation stamps the draft: %+v %v", activated, err)
		}
		if _, err := store.UpdateWebhookEndpointRevision(ctx, update); !errors.Is(err, domain.ErrRevisionNotDraft) {
			t.Fatalf("update of the active revision = %v, want ErrRevisionNotDraft", err)
		}
		assertSQLRejected(t, ctx, store, `UPDATE webhook_endpoint_revisions SET url = 'https://evil.example.com' WHERE id = $1`,
			"only a draft webhook endpoint revision may change its content", created.Revision.ID)
	})

	t.Run("team escalation policy", func(t *testing.T) {
		alice := insertEscalationTestMember(t, ctx, store, orgID, "draft-alice", now)
		team, err := store.CreateTeam(ctx, application.CreateTeamInput{
			OrganizationID: orgID, Name: "drafts", ActorMembershipID: owner, Now: now,
		})
		if err != nil {
			t.Fatalf("CreateTeam: %v", err)
		}
		draft, err := store.CreateTeamEscalationPolicyRevision(ctx, application.CreateTeamEscalationPolicyRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, TeamID: team.ID,
			Document: teamPolicyDocument(true), DocumentVersion: 2, CreatedByActorID: owner, Now: now,
		})
		if err != nil || draft.ActivatedAt != nil {
			t.Fatalf("CreateTeamEscalationPolicyRevision: %+v %v", draft, err)
		}
		update := application.UpdateTeamEscalationPolicyRevisionInput{
			OrganizationID: orgID, TeamID: team.ID, Revision: draft.Revision,
			Document: teamPolicyDocument(false, alice), DocumentVersion: 2, ActorID: owner, Now: now.Add(time.Minute),
		}
		updated, err := store.UpdateTeamEscalationPolicyRevision(ctx, update)
		if err != nil || updated.Revision != draft.Revision {
			t.Fatalf("UpdateTeamEscalationPolicyRevision: %+v %v", updated, err)
		}
		read, err := store.GetTeamEscalationPolicyRevision(ctx, application.GetTeamEscalationPolicyRevisionInput{
			OrganizationID: orgID, TeamID: team.ID, Revision: draft.Revision,
		})
		if err != nil || !jsonEqual(read.Document, update.Document) {
			t.Fatalf("the draft reads back changed in place: %s %v", read.Document, err)
		}
		activated, err := store.ActivateTeamEscalationPolicyRevision(ctx, application.ActivateTeamEscalationPolicyRevisionInput{
			OrganizationID: orgID, TeamID: team.ID, Revision: draft.Revision, ActorID: owner, Now: now.Add(2 * time.Minute),
		})
		if err != nil || activated.ActivatedAt == nil {
			t.Fatalf("activation stamps the draft: %+v %v", activated, err)
		}
		if _, err := store.UpdateTeamEscalationPolicyRevision(ctx, update); !errors.Is(err, domain.ErrRevisionNotDraft) {
			t.Fatalf("update of the active revision = %v, want ErrRevisionNotDraft", err)
		}
		assertSQLRejected(t, ctx, store, `UPDATE team_escalation_policies SET document = '{"nodes":[]}' WHERE id = $1`,
			"only a draft team escalation policy revision may change its content", draft.ID)
	})
}

func jsonEqual(left, right json.RawMessage) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	leftCanonical, _ := json.Marshal(a)
	rightCanonical, _ := json.Marshal(b)
	return string(leftCanonical) == string(rightCanonical)
}
