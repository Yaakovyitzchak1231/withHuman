package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// The reference guards in both directions: a pipeline revision cannot be
// activated while a path it names is inactive or an endpoint it posts to
// is gone, and a path cannot be archived while an active revision names it.
// Every scope is born with an empty active revision 1, so "no revision
// names the path" means that seed is active again.
func TestPipelineReferenceGuardsAgainstRealPostgres(t *testing.T) {
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
	orgID, membershipID := insertPipelineTestTenant(t, ctx, store, "references", now)
	activatePath := func(t *testing.T, key string) domain.EscalationPathRevision {
		t.Helper()
		created := createRoutingTestPathRevision(t, ctx, store, orgID, membershipID, key, routingTestPathDocument(), now)
		activated, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
			OrganizationID: orgID, PathKey: key, Revision: created.Revision, ActorID: membershipID, Now: now,
		})
		if err != nil {
			t.Fatalf("activate %s: %v", key, err)
		}
		return activated
	}
	activatePipeline := func(agentSlug domain.AgentSlug, revision, expected int64) error {
		_, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
			OrganizationID: orgID, AgentSlug: agentSlug, Revision: revision,
			ExpectedActiveRevision: expected, ActorID: membershipID, Now: now,
		})
		return err
	}
	archivePath := func(key string) error {
		_, err := store.ArchiveEscalationPath(ctx, application.ArchiveEscalationPathInput{
			OrganizationID: orgID, PathKey: key, ActorID: membershipID, Now: now,
		})
		return err
	}
	activeRevision := func(t *testing.T, agentSlug domain.AgentSlug) domain.RequestApprovalPipelineRevision {
		t.Helper()
		active, _, err := activeRequestApprovalPipelineRevision(ctx, mustBegin(t, ctx, store), orgID, agentSlug)
		if err != nil {
			t.Fatal(err)
		}
		return active
	}

	t.Run("escalation paths", func(t *testing.T) {
		seedPipelineTestScope(t, ctx, store, orgID, membershipID, "billing", now)
		activatePath(t, "fallback")
		payments := createRoutingTestPathRevision(t, ctx, store, orgID, membershipID, "payments", routingTestPathDocument(), now)
		review := pipelineTestBlock("manual-review", `{"outcome":"human","escalation_path":"payments"}`)
		review.BlockType = "always"
		revision, err := store.CreateRequestApprovalPipelineRevision(ctx, application.CreateRequestApprovalPipelineRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, AgentSlug: "billing",
			Blocks: []application.RequestApprovalBlockSnapshotInput{
				pipelineTestBlock("auto-approve", `{"on_match":"approve"}`),
				review,
			},
			DefaultEscalationPath: "fallback", CreatedByActorID: membershipID, Now: now,
		})
		if err != nil {
			t.Fatalf("create revision: %v", err)
		}

		// The path exists but has no active revision: activation is refused
		// and names the path, and the seed stays active.
		var inactive domain.EscalationPathsInactiveError
		if err := activatePipeline("billing", revision.Revision, 1); !errors.As(err, &inactive) || !errors.Is(err, domain.ErrEscalationPathInactive) || len(inactive.PathKeys) != 1 || inactive.PathKeys[0] != "payments" {
			t.Fatalf("activation with an inactive path = %v, want EscalationPathsInactiveError{payments}", err)
		}
		if active := activeRevision(t, "billing"); active.Revision != 1 {
			t.Fatalf("refused activation changed the active revision: %+v", active)
		}

		if _, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
			OrganizationID: orgID, PathKey: "payments", Revision: payments.Revision, ActorID: membershipID, Now: now,
		}); err != nil {
			t.Fatalf("activate payments: %v", err)
		}
		if err := activatePipeline("billing", revision.Revision, 1); err != nil {
			t.Fatalf("activation with active paths: %v", err)
		}

		uses, err := store.ListEscalationPathUses(ctx, application.ListEscalationPathUsesInput{OrganizationID: orgID, PathKey: "payments"})
		if err != nil || len(uses) != 1 || uses[0].PipelineID != revision.ID || uses[0].AgentSlug != "billing" || uses[0].Revision != revision.Revision || uses[0].BlockKey != "manual-review" {
			t.Fatalf("block use = %+v %v", uses, err)
		}
		uses, err = store.ListEscalationPathUses(ctx, application.ListEscalationPathUsesInput{OrganizationID: orgID, PathKey: "fallback"})
		if err != nil || len(uses) != 1 || uses[0].PipelineID != revision.ID || uses[0].BlockKey != "" {
			t.Fatalf("default use = %+v %v", uses, err)
		}
		if uses, err := store.ListEscalationPathUses(ctx, application.ListEscalationPathUsesInput{OrganizationID: domain.ID(uuid.NewString()), PathKey: "payments"}); err != nil || len(uses) != 0 {
			t.Fatalf("cross-organization uses = %+v %v", uses, err)
		}

		var inUse domain.EscalationPathInUseError
		if err := archivePath("payments"); !errors.As(err, &inUse) || !errors.Is(err, domain.ErrEscalationPathInUse) || inUse.PathKey != "payments" || len(inUse.Uses) != 1 {
			t.Fatalf("archive while used by a block = %v", err)
		}
		if err := archivePath("fallback"); !errors.Is(err, domain.ErrEscalationPathInUse) {
			t.Fatalf("archive while used as the default = %v", err)
		}
		if still, err := store.GetActiveEscalationPathRevision(ctx, application.GetActiveEscalationPathRevisionInput{OrganizationID: orgID, PathKey: "payments"}); err != nil || !still.IsActive || still.ArchivedAt != nil {
			t.Fatalf("refused archive changed the path: %+v %v", still, err)
		}

		// Once the empty seed is active again no revision names the path,
		// and it archives.
		if err := activatePipeline("billing", 1, revision.Revision); err != nil {
			t.Fatalf("roll back to the seed: %v", err)
		}
		if uses, err := store.ListEscalationPathUses(ctx, application.ListEscalationPathUsesInput{OrganizationID: orgID, PathKey: "payments"}); err != nil || len(uses) != 0 {
			t.Fatalf("inactive revision counted as a use: %+v %v", uses, err)
		}
		if err := archivePath("payments"); err != nil {
			t.Fatalf("archive unused path: %v", err)
		}
		// And the rollback is refused again, because the path is archived.
		if err := activatePipeline("billing", revision.Revision, 1); !errors.Is(err, domain.ErrEscalationPathInactive) {
			t.Fatalf("rollback onto an archived path = %v", err)
		}
	})

	t.Run("webhook endpoints", func(t *testing.T) {
		seedPipelineTestScope(t, ctx, store, orgID, membershipID, "ops", now)
		created, err := store.CreateWebhookEndpointRevision(ctx, application.CreateWebhookEndpointRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, EndpointKey: "fraud", Name: "fraud", URL: "https://fraud.example.com/hooks",
			SealedSigningSecret: []byte("sealed"), CreatedByActorID: membershipID, Now: now,
		})
		if err != nil {
			t.Fatalf("create endpoint revision: %v", err)
		}
		webhookBlock := func(key, endpointKey string) application.RequestApprovalBlockSnapshotInput {
			config, _ := json.Marshal(map[string]any{"endpoint_key": endpointKey, "allowed_outcomes": []string{"next"}, "reason": "Checked."})
			block := pipelineTestBlock(key, string(config))
			block.BlockType = "webhook"
			return block
		}
		revision := createPipelineTestRevision(t, ctx, store, orgID, membershipID, "ops", []application.RequestApprovalBlockSnapshotInput{
			webhookBlock("fraud-check", "fraud"), webhookBlock("missing-check", "gone"),
		}, now)
		// Neither an endpoint with no revision nor one whose revision is
		// not yet active may be activated against.
		var inactive domain.WebhookEndpointsInactiveError
		if err := activatePipeline("ops", revision.Revision, 1); !errors.As(err, &inactive) || !errors.Is(err, domain.ErrWebhookEndpointInactive) || len(inactive.EndpointKeys) != 2 || inactive.EndpointKeys[0] != "fraud" || inactive.EndpointKeys[1] != "gone" {
			t.Fatalf("activation with inactive endpoints = %v, want WebhookEndpointsInactiveError{fraud, gone}", err)
		}
		if _, err := store.ActivateWebhookEndpointRevision(ctx, application.ActivateWebhookEndpointRevisionInput{
			OrganizationID: orgID, EndpointKey: "fraud", Revision: created.Revision.Revision, ActorID: membershipID, Now: now,
		}); err != nil {
			t.Fatalf("activate endpoint: %v", err)
		}
		healthy := createPipelineTestRevision(t, ctx, store, orgID, membershipID, "ops", []application.RequestApprovalBlockSnapshotInput{
			webhookBlock("fraud-check", "fraud"),
		}, now)
		if err := activatePipeline("ops", healthy.Revision, 1); err != nil {
			t.Fatalf("activation with an active endpoint: %v", err)
		}
		var inUse domain.WebhookEndpointInUseError
		if _, err := store.DeactivateWebhookEndpoint(ctx, application.DeactivateWebhookEndpointInput{
			OrganizationID: orgID, EndpointKey: "fraud", ExpectedActiveRevision: created.Revision.Revision, ActorID: membershipID, Now: now,
		}); !errors.As(err, &inUse) || !errors.Is(err, domain.ErrWebhookEndpointInUse) || inUse.EndpointKey != "fraud" || len(inUse.Uses) != 1 || inUse.Uses[0].BlockKey != "fraud-check" {
			t.Fatalf("archive while used = %v", err)
		}
	})

	// A branch is stored flat: it and every block inside it are rows of
	// their own in pre-order, so the guards and the uses see a branch's
	// own path and everything nested exactly as they see top-level blocks.
	t.Run("branches", func(t *testing.T) {
		seedPipelineTestScope(t, ctx, store, orgID, membershipID, "support", now)
		branchEnd := createRoutingTestPathRevision(t, ctx, store, orgID, membershipID, "branch-end", routingTestPathDocument(), now)
		inside := createRoutingTestPathRevision(t, ctx, store, orgID, membershipID, "inside-path", routingTestPathDocument(), now)
		endpoint, err := store.CreateWebhookEndpointRevision(ctx, application.CreateWebhookEndpointRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, EndpointKey: "branch-fraud", Name: "branch-fraud", URL: "https://fraud.example.com/branch",
			SealedSigningSecret: []byte("sealed"), CreatedByActorID: membershipID, Now: now,
		})
		if err != nil {
			t.Fatalf("create endpoint revision: %v", err)
		}
		conditionID := uuid.NewString()
		branchBlock := func(reason string) application.RequestApprovalBlockSnapshotInput {
			config, _ := json.Marshal(map[string]any{
				"when":       map[string]any{"id": conditionID, "field": "/request/tool", "operator": "equals", "value": "stripe.refund"},
				"block_keys": []string{"inside-review", "fraud-hook"}, "reason": reason, "escalation_path": "branch-end",
			})
			block := pipelineTestBlock("refunds", string(config))
			block.BlockType = "branch"
			return block
		}
		review := pipelineTestBlock("inside-review", `{"on_match":"human","escalation_path":"inside-path"}`)
		hook := func(reason string) application.RequestApprovalBlockSnapshotInput {
			config, _ := json.Marshal(map[string]any{"endpoint_key": "branch-fraud", "allowed_outcomes": []string{"next"}, "reason": reason})
			block := pipelineTestBlock("fraud-hook", string(config))
			block.BlockType = "webhook"
			return block
		}
		revision := createPipelineTestRevision(t, ctx, store, orgID, membershipID, "support", []application.RequestApprovalBlockSnapshotInput{
			branchBlock("Refunds nobody decided."), review, hook("Checked."),
		}, now)
		stored, err := store.GetRequestApprovalBlocks(ctx, application.GetRequestApprovalBlocksInput{OrganizationID: orgID, BlockIDs: revision.BlockIDs})
		if err != nil || len(stored) != 3 || stored[0].BlockKey != "refunds" || stored[1].BlockKey != "inside-review" || stored[2].BlockKey != "fraud-hook" {
			t.Fatalf("blocks did not read back in pre-order: %+v %v", stored, err)
		}

		var inactivePaths domain.EscalationPathsInactiveError
		if err := activatePipeline("support", revision.Revision, 1); !errors.As(err, &inactivePaths) || len(inactivePaths.PathKeys) != 2 {
			t.Fatalf("activation with the branch's and a nested block's inactive paths = %v", err)
		}
		for _, path := range []domain.EscalationPathRevision{branchEnd, inside} {
			if _, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
				OrganizationID: orgID, PathKey: path.PathKey, Revision: path.Revision, ActorID: membershipID, Now: now,
			}); err != nil {
				t.Fatalf("activate %s: %v", path.PathKey, err)
			}
		}
		if err := activatePipeline("support", revision.Revision, 1); !errors.Is(err, domain.ErrWebhookEndpointInactive) {
			t.Fatalf("activation with a nested webhook's inactive endpoint = %v", err)
		}
		if _, err := store.ActivateWebhookEndpointRevision(ctx, application.ActivateWebhookEndpointRevisionInput{
			OrganizationID: orgID, EndpointKey: "branch-fraud", Revision: endpoint.Revision.Revision, ActorID: membershipID, Now: now,
		}); err != nil {
			t.Fatalf("activate endpoint: %v", err)
		}
		if err := activatePipeline("support", revision.Revision, 1); err != nil {
			t.Fatalf("activation with every nested reference active: %v", err)
		}

		for path, blockKey := range map[string]string{"branch-end": "refunds", "inside-path": "inside-review"} {
			uses, err := store.ListEscalationPathUses(ctx, application.ListEscalationPathUsesInput{OrganizationID: orgID, PathKey: path})
			if err != nil || len(uses) != 1 || uses[0].BlockKey != blockKey {
				t.Fatalf("%s uses = %+v %v", path, uses, err)
			}
			if err := archivePath(path); !errors.Is(err, domain.ErrEscalationPathInUse) {
				t.Fatalf("archive %s while a branch uses it = %v", path, err)
			}
		}
		endpointUses, err := store.ListWebhookEndpointUses(ctx, application.ListWebhookEndpointUsesInput{OrganizationID: orgID, EndpointKeys: []string{"branch-fraud"}})
		if uses := endpointUses["branch-fraud"]; err != nil || len(uses) != 1 || uses[0].BlockKey != "fraud-hook" {
			t.Fatalf("nested webhook uses = %+v %v", endpointUses, err)
		}

		// A revision that changes one nested block reuses the unchanged
		// branch and sibling snapshots.
		next := createPipelineTestRevision(t, ctx, store, orgID, membershipID, "support", []application.RequestApprovalBlockSnapshotInput{
			branchBlock("Refunds nobody decided."), pipelineTestBlock("inside-review", `{"on_match":"human","escalation_path":"inside-path"}`), hook("Checked again."),
		}, now)
		if next.BlockIDs[0] != revision.BlockIDs[0] || next.BlockIDs[1] != revision.BlockIDs[1] || next.BlockIDs[2] == revision.BlockIDs[2] {
			t.Fatalf("snapshot reuse across revisions: before %v after %v", revision.BlockIDs, next.BlockIDs)
		}
	})

	// An archive and an activation that names the path race: exactly one
	// commits, and the end state is consistent either way.
	t.Run("archive races activation", func(t *testing.T) {
		for round := 0; round < 4; round++ {
			key := "race-" + uuid.NewString()[:8]
			agentSlug := domain.AgentSlug("agent-" + uuid.NewString()[:8])
			seedPipelineTestScope(t, ctx, store, orgID, membershipID, agentSlug, now)
			activatePath(t, key)
			revision := createPipelineTestRevision(t, ctx, store, orgID, membershipID, agentSlug, []application.RequestApprovalBlockSnapshotInput{
				pipelineTestBlock("review", `{"on_match":"human","escalation_path":"`+key+`"}`),
			}, now)
			var wait sync.WaitGroup
			var activateErr, archiveErr error
			wait.Add(2)
			go func() { defer wait.Done(); activateErr = activatePipeline(agentSlug, revision.Revision, 1) }()
			go func() { defer wait.Done(); archiveErr = archivePath(key) }()
			wait.Wait()
			active := activeRevision(t, agentSlug)
			_, pathErr := store.GetActiveEscalationPathRevision(ctx, application.GetActiveEscalationPathRevisionInput{OrganizationID: orgID, PathKey: key})
			pathActive := pathErr == nil
			switch {
			case activateErr == nil && errors.Is(archiveErr, domain.ErrEscalationPathInUse):
				if active.ID != revision.ID || !pathActive {
					t.Fatalf("round %d: activation won but state is revision=%d path_active=%v", round, active.Revision, pathActive)
				}
			case archiveErr == nil && errors.Is(activateErr, domain.ErrEscalationPathInactive):
				if active.Revision != 1 || pathActive {
					t.Fatalf("round %d: archive won but state is revision=%d path_active=%v", round, active.Revision, pathActive)
				}
			default:
				t.Fatalf("round %d: activate=%v archive=%v, want exactly one to win", round, activateErr, archiveErr)
			}
		}
	})
}

func mustBegin(t *testing.T, ctx context.Context, store *Store) pgx.Tx {
	t.Helper()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	return tx
}
