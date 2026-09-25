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
	// Every scope is born with an empty active revision 1, so authored
	// revisions start at 2. The scopes this test edits are seeded the way
	// agent creation and organization activation seed them.
	for _, scope := range []struct {
		organization, membership domain.ID
		agentSlug                domain.AgentSlug
	}{{firstOrg, firstMembership, "billing"}, {secondOrg, secondMembership, "billing"}, {secondOrg, secondMembership, "cross-tenant"}, {secondOrg, secondMembership, ""}} {
		seedPipelineTestScope(t, ctx, store, scope.organization, scope.membership, scope.agentSlug, now)
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
			OrganizationID: secondOrg, AgentSlug: "billing", Revision: 3,
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-tenant revision lookup returned %v, want not found", err)
		}
		_, err = store.CreateRequestApprovalPipelineRevision(ctx, application.CreateRequestApprovalPipelineRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: firstOrg, AgentSlug: "no-such-agent-type",
			Blocks: firstBlocks, CreatedByActorID: firstMembership, Now: now,
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("revision for an agent type without agents returned %v, want not found", err)
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
			(id, organization_id, agent_slug, revision, block_ids, created_by_actor_id, created_at)
			VALUES ($1, $2, 'billing', 9, '{}', $3, $4)`, "revision must be 4",
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
				OrganizationID: firstOrg, AgentSlug: "billing", Revision: revision,
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
			OrganizationID: firstOrg, AgentSlug: "billing",
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
			OrganizationID: firstOrg, AgentSlug: "billing", Revision: rollbackTarget,
			ExpectedActiveRevision: wrongExpected, ActorID: firstMembership, Now: now.Add(5 * time.Second),
		})
		if !errors.Is(err, domain.ErrPreconditionFailed) {
			t.Fatalf("stale activation returned %v, want precondition failed", err)
		}
		rolledBack, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
			OrganizationID: firstOrg, AgentSlug: "billing", Revision: rollbackTarget,
			ExpectedActiveRevision: active.Revision, ActorID: firstMembership, Now: now.Add(6 * time.Second),
		})
		if err != nil || !rolledBack.IsActive || rolledBack.Revision != rollbackTarget {
			t.Fatalf("rollback activation = %+v, %v", rolledBack, err)
		}
		activeRevisions, err := store.ListActiveRequestApprovalPipelineRevisions(ctx, application.ListActiveRequestApprovalPipelineRevisionsInput{Limit: 200})
		if err != nil {
			t.Fatalf("list active revisions for startup: %v", err)
		}
		// Every seeded scope's empty revision is active too: the first
		// organization's billing scope now holds the rolled-back revision,
		// and the second organization's three scopes hold their seeds.
		byScope := make(map[string]domain.ID)
		for _, revision := range activeRevisions {
			byScope[string(revision.OrganizationID)+"/"+string(revision.AgentSlug)] = revision.ID
		}
		if len(activeRevisions) != 4 || byScope[string(firstOrg)+"/billing"] != rolledBack.ID {
			t.Fatalf("startup revision scan returned unexpected tenants or revisions: %+v", activeRevisions)
		}
		// A scope is never left without an active revision, even by hand.
		assertSQLRejected(t, ctx, store, `UPDATE request_approval_pipelines SET is_active = false WHERE id = $1`,
			"must keep an active revision", rolledBack.ID)
	})

	t.Run("organization scope", func(t *testing.T) {
		orgFirst := createPipelineTestRevision(t, ctx, store, secondOrg, secondMembership, "",
			[]application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("org-small-refund", `{"on_match":"approve"}`)}, now.Add(2*time.Second))
		if orgFirst.Revision != 2 || orgFirst.AgentSlug != "" {
			t.Fatalf("organization revision = %+v, want scope-independent revision 2", orgFirst)
		}
		orgSecond := createPipelineTestRevision(t, ctx, store, secondOrg, secondMembership, "",
			[]application.RequestApprovalBlockSnapshotInput{pipelineTestBlock("org-manual", `{"on_match":"human"}`)}, now.Add(3*time.Second))
		if orgSecond.Revision != 3 {
			t.Fatalf("organization scope did not keep its own revision sequence: %+v", orgSecond)
		}

		assertSQLRejected(t, ctx, store, `INSERT INTO request_approval_pipelines
			(id, organization_id, agent_slug, revision, block_ids, created_by_actor_id, created_at)
			VALUES ($1, $2, NULL, 9, '{}', $3, $4)`, "revision must be 4",
			uuid.NewString(), secondOrg, secondMembership, now)

		activated, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
			OrganizationID: secondOrg, AgentSlug: "", Revision: orgFirst.Revision,
			ExpectedActiveRevision: 1, ActorID: secondMembership, Now: now.Add(4 * time.Second),
		})
		if err != nil || !activated.IsActive {
			t.Fatalf("organization activation = %+v, %v", activated, err)
		}
		assertSQLRejected(t, ctx, store, `UPDATE request_approval_pipelines SET is_active = true WHERE id = $1`,
			"organization_scope_active", orgSecond.ID)

		active, err := store.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
			OrganizationID: secondOrg, AgentSlug: "",
		})
		if err != nil || active.ID != orgFirst.ID || active.AgentSlug != "" {
			t.Fatalf("active organization revision = %+v, %v", active, err)
		}
		if billing, err := store.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
			OrganizationID: secondOrg, AgentSlug: "billing",
		}); err != nil || billing.Revision != 1 || len(billing.BlockIDs) != 0 {
			t.Fatalf("organization activation leaked into an agent-type scope: %+v, %v", billing, err)
		}

		summaries, err := store.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{
			OrganizationID: secondOrg, Scope: domain.ScopeSet{All: true}, Limit: 50,
		})
		if err != nil {
			t.Fatalf("list summaries with organization scope: %v", err)
		}
		if len(summaries) != 3 || summaries[0].AgentSlug != "" || summaries[0].RevisionCount != 3 ||
			summaries[0].ActiveRevision == nil || *summaries[0].ActiveRevision != orgFirst.Revision {
			t.Fatalf("organization summary was not listed first: %+v", summaries)
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
	if len(summaries) != 1 || summaries[0].AgentSlug != "billing" || summaries[0].ActiveRevision == nil ||
		summaries[0].LatestRevision != 5 || summaries[0].RevisionCount != 5 ||
		!summaries[0].LatestCreatedAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("unexpected organization-scoped summaries: %+v", summaries)
	}

	t.Run("history pages by cursor", func(t *testing.T) {
		// The billing scope was seeded with revision 1; six more make a
		// history that does not divide into pages of four.
		seedPipelineTestScope(t, ctx, store, firstOrg, firstMembership, "paged", now)
		for index := 0; index < 6; index++ {
			createPipelineTestRevision(t, ctx, store, firstOrg, firstMembership, "paged", firstBlocks, now.Add(time.Duration(index)*time.Second))
		}
		assertRevisionPaging(t, func(cursor string, limit int) (revisionPageOf, error) {
			page, err := store.ListRequestApprovalPipelineRevisions(ctx, application.ListRequestApprovalPipelineRevisionsInput{
				OrganizationID: firstOrg, AgentSlug: "paged", Limit: limit, Cursor: cursor,
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

// seedPipelineTestScope brings a scope into being the way the product
// does (an organization's activation, an agent type's first agent): an
// empty active revision 1 written in one transaction.
func seedPipelineTestScope(t *testing.T, ctx context.Context, store *Store, organizationID, membershipID domain.ID, agentSlug domain.AgentSlug, now time.Time) {
	t.Helper()
	tx, err := store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := store.ensureRequestApprovalPipelineScope(ctx, tx, organizationID, agentSlug, membershipID, now); err != nil {
		t.Fatalf("seed %q scope: %v", agentSlug, err)
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

func createPipelineTestRevision(t *testing.T, ctx context.Context, store *Store, organizationID, membershipID domain.ID, agentSlug domain.AgentSlug, blocks []application.RequestApprovalBlockSnapshotInput, now time.Time) domain.RequestApprovalPipelineRevision {
	t.Helper()
	revision, err := store.CreateRequestApprovalPipelineRevision(ctx, application.CreateRequestApprovalPipelineRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: organizationID, AgentSlug: agentSlug,
		Blocks: blocks, CreatedByActorID: membershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("create %s pipeline revision: %v", agentSlug, err)
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
		(id, organization_id, agent_slug, revision, block_ids, created_by_actor_id, created_at)
		VALUES ($1, $2, 'billing', 4, $3, $4, $5)`, message,
		uuid.NewString(), organizationID, idStrings(blockIDs), membershipID, now)
}

func TestRequestApprovalPipelineInputRejectsDuplicateLogicalBlocks(t *testing.T) {
	now := time.Now().UTC()
	first := pipelineTestBlock("same-key", `{"v":1}`)
	second := pipelineTestBlock("same-key", `{"v":2}`)
	err := (application.CreateRequestApprovalPipelineRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: domain.ID(uuid.NewString()), AgentSlug: "billing",
		Blocks:           []application.RequestApprovalBlockSnapshotInput{first, second},
		CreatedByActorID: domain.ID(uuid.NewString()), Now: now,
	}).Validate()
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("duplicate logical block validation returned %v, want invalid", err)
	}
}

// A pipeline scope comes into being with the organization or with the
// agent that owns it, as an empty active revision, so that there is
// always exactly one pipeline per agent and none can be authored for a
// slug no agent holds.
func TestPipelineScopesFollowAgentsAgainstRealPostgres(t *testing.T) {
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
		OrganizationID: orgID, OrganizationSlug: "scopes", OrganizationName: "Scopes",
		UserID: userID, MembershipID: membershipID, DisplayName: "Owner",
		Email: "owner@scopes.example", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	activeScope := func(agentSlug domain.AgentSlug) domain.RequestApprovalPipelineRevision {
		t.Helper()
		revision, err := store.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
			OrganizationID: orgID, AgentSlug: agentSlug,
		})
		if err != nil {
			t.Fatalf("active revision for %q: %v", agentSlug, err)
		}
		return revision
	}

	organization := activeScope("")
	if organization.Revision != 1 || len(organization.BlockIDs) != 0 || organization.CreatedByActorID != membershipID {
		t.Fatalf("organization activation did not seed an empty active revision: %+v", organization)
	}

	if _, err := store.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
		OrganizationID: orgID, AgentSlug: "release",
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a slug no agent holds has a pipeline: %v", err)
	}
	createAgent := func(name string, agentSlug domain.AgentSlug) {
		t.Helper()
		if _, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
			OrganizationID: orgID, ActorID: membershipID, Name: name, AgentSlug: agentSlug,
			AgentQuota: application.UnlimitedQuota(), Now: now,
		}); err != nil {
			t.Fatalf("CreateAgent %s: %v", name, err)
		}
	}
	createAgent("release-one", "release")
	release := activeScope("release")
	if release.Revision != 1 || len(release.BlockIDs) != 0 || release.AgentSlug != "release" {
		t.Fatalf("the agent did not seed its pipeline: %+v", release)
	}
	// The slug is the scope key and is taken for good: a second agent
	// cannot share it, so no agent can ever take over another's pipeline.
	if _, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
		OrganizationID: orgID, ActorID: membershipID, Name: "release-two", AgentSlug: "release",
		AgentQuota: application.UnlimitedQuota(), Now: now,
	}); !errors.Is(err, domain.ErrAgentSlugTaken) || !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second agent on the same slug must be refused as a conflict, got %v", err)
	}
	if again := activeScope("release"); again.ID != release.ID {
		t.Fatalf("the refused agent touched the pipeline: %+v", again)
	}
	revisions, err := store.ListRequestApprovalPipelineRevisions(ctx, application.ListRequestApprovalPipelineRevisionsInput{
		OrganizationID: orgID, AgentSlug: "release", Limit: 10,
	})
	if err != nil || len(revisions.Revisions) != 1 || revisions.TotalCount != 1 || revisions.NextCursor != "" {
		t.Fatalf("release revisions = %+v, %v; want exactly the seed", revisions, err)
	}

	// Enrollment into an existing agent preserves that agent's pipeline.
	createAgent("support-bot", "support")
	supportAgent, err := store.GetAgentBySlug(ctx, application.GetAgentBySlugInput{OrganizationID: orgID, AgentSlug: "support"})
	if err != nil {
		t.Fatal(err)
	}
	codeHash := make([]byte, 32)
	if err := store.CreateAgentEnrollment(ctx, application.CreateAgentEnrollmentInput{Enrollment: domain.AgentEnrollment{
		ID: domain.ID(uuid.NewString()), EnrollmentCodeHash: codeHash, UserCode: "WXYZ-1234",
		Runtime: "claude-code", RequestedAgentSlug: "support", InstanceName: "laptop", InstanceMetadata: json.RawMessage(`{}`),
		Status:    "pending",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}}); err != nil {
		t.Fatalf("CreateAgentEnrollment: %v", err)
	}
	if _, err := store.AuthorizeAgentEnrollment(ctx, application.AuthorizeAgentEnrollmentInput{
		OrganizationID: orgID, UserCode: "WXYZ-1234", MembershipID: membershipID, AgentID: supportAgent.ID, Now: now,
	}); err != nil {
		t.Fatalf("AuthorizeAgentEnrollment: %v", err)
	}
	if support := activeScope("support"); support.Revision != 1 || len(support.BlockIDs) != 0 || support.ID == release.ID {
		t.Fatalf("enrollment did not keep the agent's own seeded pipeline: %+v", support)
	}

	summaries, err := store.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{OrganizationID: orgID, Scope: domain.ScopeSet{All: true}, Limit: 50})
	if err != nil || len(summaries) != 3 {
		t.Fatalf("summaries = %+v, %v; want organization, release, support", summaries, err)
	}
	for _, summary := range summaries {
		if summary.ActiveRevision == nil || *summary.ActiveRevision != 1 || summary.RevisionCount != 1 {
			t.Fatalf("seeded scope summary = %+v, want one active revision", summary)
		}
	}

	counts := make(map[string]int)
	for _, event := range auditEvents(t, ctx, store, `organization_id = $1`, orgID) {
		counts[event.EventType]++
		if strings.HasPrefix(event.EventType, "request_approval_pipeline.") && (event.ActorID != userID || event.SubjectType != "request_approval_pipeline") {
			t.Fatalf("seed audit event lacks the creating person: %+v", event)
		}
	}
	if counts["request_approval_pipeline.revision_created"] != 3 || counts["request_approval_pipeline.activated"] != 3 {
		t.Fatalf("seed audit counts: %+v", counts)
	}
}
