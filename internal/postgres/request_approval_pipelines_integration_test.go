package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

func TestRequestApprovalPipelinesAgainstRealPostgres(t *testing.T) {
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
	firstOrg, firstMembership := insertPipelineTestTenant(t, ctx, store, "first", now)
	secondOrg, secondMembership := insertPipelineTestTenant(t, ctx, store, "second", now)
	// Every pipeline is born with an empty active revision 1, so authored
	// revisions start at 2. The pipelines this test edits are seeded the
	// way a create and the organization's activation seed them.
	for _, scope := range []struct {
		organization, membership domain.ID
		pipelineKey              string
	}{{firstOrg, firstMembership, "billing"}, {secondOrg, secondMembership, "billing"}, {secondOrg, secondMembership, "cross-tenant"}, {secondOrg, secondMembership, domain.EntryPipelineKey}} {
		seedPipelineTestScope(t, ctx, store, scope.organization, scope.membership, scope.pipelineKey, now)
	}
	firstBlocks := []application.RequestApprovalBlockSnapshotInput{
		pipelineTestBlock("small-refund", `{"on_match":"approve","threshold":1000}`),
		pipelineTestBlock("manual-review", `{"on_match":"human"}`),
	}

	firstRevision := createPipelineTestRevision(t, ctx, store, firstOrg, firstMembership, "billing", firstBlocks, now)
	if firstRevision.Revision != 2 || firstRevision.IsActive {
		t.Fatalf("first authored revision = %+v, want inactive revision 2", firstRevision)
	}
	reusedInputs := []application.RequestApprovalBlockSnapshotInput{
		pipelineTestBlock("small-refund", `{"threshold":1000,"on_match":"approve"}`),
		pipelineTestBlock("manual-review", `{"on_match":"human"}`),
	}
	secondRevision := createPipelineTestRevision(t, ctx, store, firstOrg, firstMembership, "billing", reusedInputs, now.Add(time.Second))
	if secondRevision.Revision != 3 {
		t.Fatalf("second revision number = %d, want 3", secondRevision.Revision)
	}
	for index := range firstRevision.BlockIDs {
		if secondRevision.BlockIDs[index] != firstRevision.BlockIDs[index] {
			t.Fatalf("block %d was not reused by content hash: %s != %s", index,
				secondRevision.BlockIDs[index], firstRevision.BlockIDs[index])
		}
	}
	blocks, err := store.GetRequestApprovalBlocks(ctx, application.GetRequestApprovalBlocksInput{
		OrganizationID: firstOrg,
		BlockIDs:       firstRevision.BlockIDs,
	})
	if err != nil {
		t.Fatalf("get ordered blocks: %v", err)
	}
	if len(blocks) != 2 || blocks[0].BlockKey != "small-refund" || blocks[1].BlockKey != "manual-review" {
		t.Fatalf("blocks lost pipeline order: %+v", blocks)
	}
	for _, block := range blocks {
		if len(block.ContentHash) != 64 {
			t.Fatalf("block %s has invalid content hash %q", block.ID, block.ContentHash)
		}
	}

	t.Run("tenant isolation", func(t *testing.T) {
		otherRevision := createPipelineTestRevision(t, ctx, store, secondOrg, secondMembership, "billing",
			[]application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("small-refund", `{"on_match":"approve","threshold":1000}`)}, now)
		if otherRevision.BlockIDs[0] == firstRevision.BlockIDs[0] {
			t.Fatal("content hash deduplication crossed organization boundary")
		}
		_, err := store.GetRequestApprovalPipelineRevision(ctx, application.GetRequestApprovalPipelineRevisionInput{
			OrganizationID: secondOrg, PipelineKey: "billing", Revision: 3,
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-tenant revision lookup returned %v, want not found", err)
		}
		_, err = store.CreateRequestApprovalPipelineRevision(ctx, application.CreateRequestApprovalPipelineRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: firstOrg, PipelineKey: "no-such-pipeline", Name: "Nowhere",
			Blocks: firstBlocks, CreatedByActorID: firstMembership, Now: now,
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("revision for a pipeline never created returned %v, want not found", err)
		}
		_, err = store.GetRequestApprovalBlocks(ctx, application.GetRequestApprovalBlocksInput{
			OrganizationID: secondOrg, BlockIDs: firstRevision.BlockIDs,
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-tenant block lookup returned %v, want not found", err)
		}
	})

	t.Run("append only records", func(t *testing.T) {
		assertSQLRejected(t, ctx, store, `UPDATE request_approval_blocks SET name = 'changed' WHERE id = $1`,
			"append-only", firstRevision.BlockIDs[0])
		assertSQLRejected(t, ctx, store, `DELETE FROM request_approval_blocks WHERE id = $1`,
			"append-only", firstRevision.BlockIDs[0])
		assertSQLRejected(t, ctx, store, `UPDATE request_approval_pipelines SET block_ids = '{}' WHERE id = $1`,
			"immutable", firstRevision.ID)
		assertSQLRejected(t, ctx, store, `DELETE FROM request_approval_pipelines WHERE id = $1`,
			"append-only", firstRevision.ID)
	})

	t.Run("monotonic revisions and invalid references", func(t *testing.T) {
		assertSQLRejected(t, ctx, store, `INSERT INTO request_approval_pipelines
			(id, organization_id, pipeline_key, name, revision, block_ids, created_by_actor_id, created_at)
			VALUES ($1, $2, 'billing', 'Billing', 9, '{}', $3, $4)`, "revision must be 4",
			uuid.NewString(), firstOrg, firstMembership, now)

		assertInvalidPipelineBlockArray(t, ctx, store, firstOrg, firstMembership,
			[]domain.ID{domain.ID(uuid.NewString())}, "missing or cross-organization", now)
		otherBlocks, err := store.GetRequestApprovalBlocks(ctx, application.GetRequestApprovalBlocksInput{
			OrganizationID: secondOrg,
			BlockIDs: []domain.ID{createPipelineTestRevision(t, ctx, store, secondOrg, secondMembership,
				"cross-tenant", []application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("foreign", `{"v":1}`)}, now).BlockIDs[0]},
		})
		if err != nil {
			t.Fatalf("load other tenant block: %v", err)
		}
		assertInvalidPipelineBlockArray(t, ctx, store, firstOrg, firstMembership,
			[]domain.ID{otherBlocks[0].ID}, "missing or cross-organization", now)
		assertInvalidPipelineBlockArray(t, ctx, store, firstOrg, firstMembership,
			[]domain.ID{firstRevision.BlockIDs[0], firstRevision.BlockIDs[0]}, "duplicate block IDs", now)

		duplicateKeyBlockID := domain.ID(uuid.NewString())
		if _, err := store.Pool().Exec(ctx, `INSERT INTO request_approval_blocks
			(id, organization_id, block_key, name, block_type, config_version, config, timeout_ms,
			 max_attempts, created_by_actor_id, created_at)
			VALUES ($1, $2, 'small-refund', 'Changed snapshot', 'cel', 1, '{"on_match":"deny"}', 50, 1, $3, $4)`,
			duplicateKeyBlockID, firstOrg, firstMembership, now); err != nil {
			t.Fatalf("insert duplicate logical key snapshot: %v", err)
		}
		assertInvalidPipelineBlockArray(t, ctx, store, firstOrg, firstMembership,
			[]domain.ID{firstRevision.BlockIDs[0], duplicateKeyBlockID}, "duplicate block keys", now)
	})

	thirdRevision := createPipelineTestRevision(t, ctx, store, firstOrg, firstMembership, "billing",
		[]application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("deny-large", `{"on_match":"deny"}`)}, now.Add(2*time.Second))
	fourthRevision := createPipelineTestRevision(t, ctx, store, firstOrg, firstMembership, "billing",
		[]application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("human-default", `{"on_match":"human"}`)}, now.Add(3*time.Second))
	if thirdRevision.Revision != 4 || fourthRevision.Revision != 5 {
		t.Fatalf("repository did not resume monotonic revisions after rejected inserts: %d, %d", thirdRevision.Revision, fourthRevision.Revision)
	}

	t.Run("concurrent activation and stale revisions", func(t *testing.T) {
		start := make(chan struct{})
		results := make(chan error, 2)
		activate := func(revision int64) {
			<-start
			_, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
				OrganizationID: firstOrg, PipelineKey: "billing", Revision: revision,
				ExpectedActiveRevision: 1, ActorID: firstMembership, Now: now.Add(4 * time.Second),
			})
			results <- err
		}
		go activate(thirdRevision.Revision)
		go activate(fourthRevision.Revision)
		close(start)
		succeeded, stale := 0, 0
		for range 2 {
			err := <-results
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, domain.ErrPreconditionFailed):
				stale++
			default:
				t.Fatalf("concurrent activation returned unexpected error: %v", err)
			}
		}
		if succeeded != 1 || stale != 1 {
			t.Fatalf("concurrent activation results: %d succeeded, %d stale", succeeded, stale)
		}
		active, err := store.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
			OrganizationID: firstOrg, PipelineKey: "billing",
		})
		if err != nil {
			t.Fatalf("get active revision: %v", err)
		}
		var rollbackTarget int64
		if active.Revision == thirdRevision.Revision {
			rollbackTarget = fourthRevision.Revision
		} else {
			rollbackTarget = thirdRevision.Revision
		}
		wrongExpected := active.Revision + 100
		_, err = store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
			OrganizationID: firstOrg, PipelineKey: "billing", Revision: rollbackTarget,
			ExpectedActiveRevision: wrongExpected, ActorID: firstMembership, Now: now.Add(5 * time.Second),
		})
		if !errors.Is(err, domain.ErrPreconditionFailed) {
			t.Fatalf("stale activation returned %v, want precondition failed", err)
		}
		rolledBack, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
			OrganizationID: firstOrg, PipelineKey: "billing", Revision: rollbackTarget,
			ExpectedActiveRevision: active.Revision, ActorID: firstMembership, Now: now.Add(6 * time.Second),
		})
		if err != nil || !rolledBack.IsActive || rolledBack.Revision != rollbackTarget {
			t.Fatalf("rollback activation = %+v, %v", rolledBack, err)
		}
		activeRevisions, err := store.ListActiveRequestApprovalPipelineRevisions(ctx, application.ListActiveRequestApprovalPipelineRevisionsInput{Limit: 200})
		if err != nil {
			t.Fatalf("list active revisions for startup: %v", err)
		}
		// Every seeded pipeline's empty revision is active too: the first
		// organization's billing pipeline now holds the rolled-back
		// revision, and the second organization's three hold their seeds.
		byScope := make(map[string]domain.ID)
		for _, revision := range activeRevisions {
			byScope[string(revision.OrganizationID)+"/"+revision.PipelineKey] = revision.ID
		}
		if len(activeRevisions) != 4 || byScope[string(firstOrg)+"/billing"] != rolledBack.ID {
			t.Fatalf("startup revision scan returned unexpected tenants or revisions: %+v", activeRevisions)
		}
		// A scope is never left without an active revision, even by hand.
		assertSQLRejected(t, ctx, store, `UPDATE request_approval_pipelines SET is_active = false WHERE id = $1`,
			"must keep an active revision", rolledBack.ID)
	})

	t.Run("entry pipeline", func(t *testing.T) {
		entryFirst := createPipelineTestRevision(t, ctx, store, secondOrg, secondMembership, domain.EntryPipelineKey,
			[]application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("entry-small-refund", `{"on_match":"approve"}`)}, now.Add(2*time.Second))
		if entryFirst.Revision != 2 || entryFirst.PipelineKey != domain.EntryPipelineKey || entryFirst.Name != entryPipelineName {
			t.Fatalf("entry revision = %+v, want revision 2 of the entry pipeline", entryFirst)
		}
		entrySecond := createPipelineTestRevision(t, ctx, store, secondOrg, secondMembership, domain.EntryPipelineKey,
			[]application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("entry-manual", `{"on_match":"human"}`)}, now.Add(3*time.Second))
		if entrySecond.Revision != 3 {
			t.Fatalf("the entry pipeline did not keep its own revision sequence: %+v", entrySecond)
		}

		assertSQLRejected(t, ctx, store, `INSERT INTO request_approval_pipelines
			(id, organization_id, pipeline_key, name, revision, block_ids, created_by_actor_id, created_at)
			VALUES ($1, $2, 'entry', 'Entry pipeline', 9, '{}', $3, $4)`, "revision must be 4",
			uuid.NewString(), secondOrg, secondMembership, now)

		activated, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
			OrganizationID: secondOrg, PipelineKey: domain.EntryPipelineKey, Revision: entryFirst.Revision,
			ExpectedActiveRevision: 1, ActorID: secondMembership, Now: now.Add(4 * time.Second),
		})
		if err != nil || !activated.IsActive {
			t.Fatalf("entry activation = %+v, %v", activated, err)
		}
		assertSQLRejected(t, ctx, store, `UPDATE request_approval_pipelines SET is_active = true WHERE id = $1`,
			"request_approval_pipelines_active_idx", entrySecond.ID)

		active, err := store.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
			OrganizationID: secondOrg, PipelineKey: domain.EntryPipelineKey,
		})
		if err != nil || active.ID != entryFirst.ID {
			t.Fatalf("active entry revision = %+v, %v", active, err)
		}
		if billing, err := store.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
			OrganizationID: secondOrg, PipelineKey: "billing",
		}); err != nil || billing.Revision != 1 || len(billing.BlockIDs) != 0 {
			t.Fatalf("entry activation leaked into another pipeline: %+v, %v", billing, err)
		}

		summaries, err := store.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{
			OrganizationID: secondOrg, Scope: domain.ScopeSet{All: true}, Limit: 50,
		})
		if err != nil {
			t.Fatalf("list summaries: %v", err)
		}
		if len(summaries) != 3 || summaries[0].PipelineKey != domain.EntryPipelineKey || summaries[0].Name != entryPipelineName || summaries[0].RevisionCount != 3 ||
			summaries[0].ActiveRevision == nil || *summaries[0].ActiveRevision != entryFirst.Revision {
			t.Fatalf("entry summary was not listed first: %+v", summaries)
		}
		scoped, err := store.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{
			OrganizationID: secondOrg, Scope: domain.ScopeSet{Scopes: []domain.Scope{{Kind: domain.ScopePipeline, ID: "billing"}}}, Limit: 50,
		})
		if err != nil || len(scoped) != 1 || scoped[0].PipelineKey != "billing" {
			t.Fatalf("a pipeline-scoped reader must see only their pipelines: %+v, %v", scoped, err)
		}
	})

	eventCounts := make(map[string]int)
	for _, event := range auditEvents(t, ctx, store, `organization_id = $1`, firstOrg) {
		eventCounts[event.EventType]++
		if strings.HasPrefix(event.EventType, "request_approval_pipeline.") &&
			(event.ActorType != domain.ActorHuman || event.ActorID == "" || event.SubjectType != "request_approval_pipeline") {
			t.Fatalf("pipeline audit event lacks administrative provenance: %+v", event)
		}
	}
	// The seed's own revision_created and activated events count too.
	if eventCounts["request_approval_pipeline.revision_created"] != 5 ||
		eventCounts["request_approval_pipeline.activated"] != 3 {
		t.Fatalf("unexpected administrative audit counts: %+v", eventCounts)
	}

	summaries, err := store.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{
		OrganizationID: firstOrg,
		Scope:          domain.ScopeSet{All: true},
		Limit:          50,
	})
	if err != nil {
		t.Fatalf("list pipeline summaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0].PipelineKey != "billing" || summaries[0].ActiveRevision == nil ||
		summaries[0].LatestRevision != 5 || summaries[0].RevisionCount != 5 ||
		!summaries[0].LatestCreatedAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("unexpected organization-scoped summaries: %+v", summaries)
	}

	t.Run("history pages by cursor", func(t *testing.T) {
		// The paged pipeline is seeded with revision 1; six more make a
		// history that does not divide into pages of four.
		seedPipelineTestScope(t, ctx, store, firstOrg, firstMembership, "paged", now)
		for index := 0; index < 6; index++ {
			createPipelineTestRevision(t, ctx, store, firstOrg, firstMembership, "paged", firstBlocks, now.Add(time.Duration(index)*time.Second))
		}
		assertRevisionPaging(t, func(cursor string, limit int) (revisionPageOf, error) {
			page, err := store.ListRequestApprovalPipelineRevisions(ctx, application.ListRequestApprovalPipelineRevisionsInput{
				OrganizationID: firstOrg, PipelineKey: "paged", Limit: limit, Cursor: cursor,
			})
			result := revisionPageOf{nextCursor: page.NextCursor, totalCount: page.TotalCount}
			for _, revision := range page.Revisions {
				result.numbers = append(result.numbers, revision.Revision)
			}
			return result, err
		}, descending(7), 4)
	})
}

func insertPipelineTestTenant(t *testing.T, ctx context.Context, store *Store, key string, now time.Time) (domain.ID, domain.ID) {
	t.Helper()
	organizationID := domain.ID(uuid.NewString())
	userID := domain.ID(uuid.NewString())
	membershipID := domain.ID(uuid.NewString())
	if _, err := store.Pool().Exec(ctx, `INSERT INTO users
		(id, display_name, primary_email, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', $4, $4)`, userID, key, key+"@example.com", now); err != nil {
		t.Fatalf("insert %s user: %v", key, err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO organizations
		(id, slug, name, status, created_at, updated_at) VALUES ($1, $2, $3, 'active', $4, $4)`,
		organizationID, "pipeline-"+key, key, now); err != nil {
		t.Fatalf("insert %s organization: %v", key, err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO memberships
		(id, user_id, organization_id, status, admission_source, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', 'manual', $4, $4)`, membershipID, userID, organizationID, now); err != nil {
		t.Fatalf("insert %s membership: %v", key, err)
	}
	return organizationID, membershipID
}

// seedPipelineTestScope brings a pipeline into being the way the product
// does: the entry pipeline through the organization's activation, any
// other through its create. Both write an empty active revision 1.
func seedPipelineTestScope(t *testing.T, ctx context.Context, store *Store, organizationID, membershipID domain.ID, pipelineKey string, now time.Time) {
	t.Helper()
	if pipelineKey != domain.EntryPipelineKey {
		if _, err := store.CreateRequestApprovalPipeline(ctx, application.CreateRequestApprovalPipelineInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: organizationID, PipelineKey: pipelineKey, Name: "Pipeline " + pipelineKey,
			CreatedByActorID: membershipID, Now: now,
		}); err != nil {
			t.Fatalf("create %q pipeline: %v", pipelineKey, err)
		}
		return
	}
	tx, err := store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := store.ensureEntryPipeline(ctx, tx, organizationID, membershipID, now); err != nil {
		t.Fatalf("seed the entry pipeline: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

func pipelineTestBlock(key, config string) application.RequestApprovalBlockSnapshotInput {
	return application.RequestApprovalBlockSnapshotInput{
		ID: domain.ID(uuid.NewString()), BlockKey: key, Name: key, BlockType: "cel",
		ConfigVersion: 1, Config: json.RawMessage(config), TimeoutMS: 50, MaxAttempts: 1,
	}
}

func createPipelineTestRevision(t *testing.T, ctx context.Context, store *Store, organizationID, membershipID domain.ID, pipelineKey string, blocks []application.RequestApprovalBlockSnapshotInput, now time.Time) domain.RequestApprovalPipelineRevision {
	t.Helper()
	name := "Pipeline " + pipelineKey
	if pipelineKey == domain.EntryPipelineKey {
		name = entryPipelineName
	}
	revision, err := store.CreateRequestApprovalPipelineRevision(ctx, application.CreateRequestApprovalPipelineRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: organizationID, PipelineKey: pipelineKey, Name: name,
		Blocks: blocks, CreatedByActorID: membershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("create %s pipeline revision: %v", pipelineKey, err)
	}
	return revision
}

func assertSQLRejected(t *testing.T, ctx context.Context, store *Store, query, message string, arguments ...any) {
	t.Helper()
	if _, err := store.Pool().Exec(ctx, query, arguments...); err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("SQL mutation returned %v, want error containing %q", err, message)
	}
}

func assertInvalidPipelineBlockArray(t *testing.T, ctx context.Context, store *Store, organizationID, membershipID domain.ID, blockIDs []domain.ID, message string, now time.Time) {
	t.Helper()
	assertSQLRejected(t, ctx, store, `INSERT INTO request_approval_pipelines
		(id, organization_id, pipeline_key, name, revision, block_ids, created_by_actor_id, created_at)
		VALUES ($1, $2, 'billing', 'Billing', 4, $3, $4, $5)`, message,
		uuid.NewString(), organizationID, idStrings(blockIDs), membershipID, now)
}

func TestRequestApprovalPipelineInputRejectsDuplicateLogicalBlocks(t *testing.T) {
	now := time.Now().UTC()
	first := pipelineTestBlock("same-key", `{"v":1}`)
	second := pipelineTestBlock("same-key", `{"v":2}`)
	err := (application.CreateRequestApprovalPipelineRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: domain.ID(uuid.NewString()), PipelineKey: "billing", Name: "Billing",
		Blocks:           []application.RequestApprovalBlockSnapshotInput{first, second},
		CreatedByActorID: domain.ID(uuid.NewString()), Now: now,
	}).Validate()
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("duplicate logical block validation returned %v, want invalid", err)
	}
}

// The entry pipeline comes into being with the organization, as an empty
// active revision; every other pipeline comes into being through its
// create. Agents own no pipeline, and a key is taken for good.
func TestPipelinesAreKeyedAgainstRealPostgres(t *testing.T) {
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
	orgID, userID, membershipID := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "keyed", OrganizationName: "Keyed",
		UserID: userID, MembershipID: membershipID, DisplayName: "Owner",
		Email: "owner@keyed.example", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	active := func(pipelineKey string) (domain.RequestApprovalPipelineRevision, error) {
		return store.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
			OrganizationID: orgID, PipelineKey: pipelineKey,
		})
	}

	entry, err := active(domain.EntryPipelineKey)
	if err != nil || entry.Revision != 1 || len(entry.BlockIDs) != 0 || entry.CreatedByActorID != membershipID || entry.Name != entryPipelineName {
		t.Fatalf("organization activation did not seed an empty active entry pipeline: %+v, %v", entry, err)
	}
	if _, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
		OrganizationID: orgID, ActorID: membershipID, Name: "release-one", AgentSlug: "release",
		AgentQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := active("release"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an agent must not seed a pipeline: %v", err)
	}

	created, err := store.CreateRequestApprovalPipeline(ctx, application.CreateRequestApprovalPipelineInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PipelineKey: "payments", Name: "Payments",
		Blocks:           []application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("small", `{"on_match":"approve"}`)},
		CreatedByActorID: membershipID, Now: now,
	})
	if err != nil || created.Revision != 1 || !created.IsActive || created.Name != "Payments" || len(created.BlockIDs) != 1 {
		t.Fatalf("CreateRequestApprovalPipeline = %+v, %v", created, err)
	}
	if again, err := active("payments"); err != nil || again.ID != created.ID {
		t.Fatalf("the created pipeline must be active at once: %+v, %v", again, err)
	}
	if _, err := store.CreateRequestApprovalPipeline(ctx, application.CreateRequestApprovalPipelineInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PipelineKey: "payments", Name: "Payments again", CreatedByActorID: membershipID, Now: now,
	}); !errors.Is(err, domain.ErrRequestApprovalPipelineExists) {
		t.Fatalf("a second pipeline on the same key = %v", err)
	}
	if _, err := store.CreateRequestApprovalPipeline(ctx, application.CreateRequestApprovalPipelineInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Name: "Entry", CreatedByActorID: membershipID, Now: now,
	}); !errors.Is(err, domain.ErrInvalid) && !errors.Is(err, domain.ErrRequestApprovalPipelineEntry) {
		t.Fatalf("creating the entry pipeline = %v", err)
	}
	renamed, err := store.CreateRequestApprovalPipelineRevision(ctx, application.CreateRequestApprovalPipelineRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PipelineKey: "payments", Name: "Payments team",
		CreatedByActorID: membershipID, Now: now,
	})
	if err != nil || renamed.Revision != 2 || renamed.Name != "Payments team" || renamed.IsActive {
		t.Fatalf("a renaming revision = %+v, %v", renamed, err)
	}
	summaries, err := store.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{OrganizationID: orgID, Scope: domain.ScopeSet{All: true}, Limit: 50})
	if err != nil || len(summaries) != 2 || summaries[0].PipelineKey != domain.EntryPipelineKey || summaries[1].Name != "Payments" || summaries[1].RevisionCount != 2 {
		t.Fatalf("summaries = %+v, %v; the name is the active revision's", summaries, err)
	}
	if _, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
		OrganizationID: orgID, PipelineKey: "payments", Revision: 2, ExpectedActiveRevision: 1, ActorID: membershipID, Now: now,
	}); err != nil {
		t.Fatalf("activate the rename: %v", err)
	}
	summaries, _ = store.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{OrganizationID: orgID, Scope: domain.ScopeSet{All: true}, Limit: 50})
	if len(summaries) != 2 || summaries[1].Name != "Payments team" {
		t.Fatalf("summaries after rename = %+v", summaries)
	}

	counts := make(map[string]int)
	for _, event := range auditEvents(t, ctx, store, `organization_id = $1`, orgID) {
		counts[event.EventType]++
		if strings.HasPrefix(event.EventType, "request_approval_pipeline.") && (event.ActorID != userID || event.SubjectType != "request_approval_pipeline") {
			t.Fatalf("pipeline audit event lacks the creating person: %+v", event)
		}
	}
	// The entry seed, the create, and the rename each create; the seed,
	// the create, and the activation each activate.
	if counts["request_approval_pipeline.revision_created"] != 3 || counts["request_approval_pipeline.activated"] != 3 {
		t.Fatalf("audit counts: %+v", counts)
	}
}
