package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
	"github.com/withHumanAI/withHuman/routing"
)

func TestRoutingAgainstRealPostgres(t *testing.T) {
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
	firstOrg, firstMembership := insertPipelineTestTenant(t, ctx, store, "routing-first", now)
	secondOrg, secondMembership := insertPipelineTestTenant(t, ctx, store, "routing-second", now)

	firstRevision := createRoutingTestPathRevision(t, ctx, store, firstOrg, firstMembership, "payments", routingTestPathDocument(), now)
	if firstRevision.Revision != 1 || firstRevision.IsActive {
		t.Fatalf("first revision = %+v, want inactive revision 1", firstRevision)
	}
	secondRevision := createRoutingTestPathRevision(t, ctx, store, firstOrg, firstMembership, "payments", routingTestPathDocument(), now.Add(time.Second))
	if secondRevision.Revision != 2 {
		t.Fatalf("second revision number = %d, want 2", secondRevision.Revision)
	}

	t.Run("tenant isolation", func(t *testing.T) {
		if _, err := store.GetEscalationPathRevision(ctx, application.GetEscalationPathRevisionInput{
			OrganizationID: secondOrg, PathKey: "payments", Revision: 1,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-tenant read = %v, want ErrNotFound", err)
		}
		if _, err := store.CreateEscalationPath(ctx, application.CreateEscalationPathInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: secondOrg, PathKey: "isolated",
			Name: "Isolated", Document: routingTestPathDocument(), DocumentVersion: 2,
			CreatedByActorID: firstMembership, Now: now,
		}); err == nil {
			t.Fatal("cross-tenant membership accepted as creator")
		}
		// The second organization's own keys are its own.
		if _, err := store.CreateEscalationPath(ctx, application.CreateEscalationPathInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: secondOrg, PathKey: "isolated",
			Name: "Isolated", Document: routingTestPathDocument(), DocumentVersion: 2,
			CreatedByActorID: secondMembership, Now: now,
		}); err != nil {
			t.Fatalf("create a path in the second organization: %v", err)
		}
	})

	t.Run("explicit create and revision append", func(t *testing.T) {
		if _, err := store.CreateEscalationPath(ctx, application.CreateEscalationPathInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: firstOrg, PathKey: "payments",
			Name: "Duplicate", Document: routingTestPathDocument(), DocumentVersion: 2,
			CreatedByActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrEscalationPathExists) {
			t.Fatalf("create on a live key = %v, want ErrEscalationPathExists", err)
		}
		if _, err := store.CreateEscalationPathRevision(ctx, application.CreateEscalationPathRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: firstOrg, PathKey: "never-created",
			Name: "Implicit", Document: routingTestPathDocument(), DocumentVersion: 2,
			CreatedByActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("revision on an unknown key = %v, want ErrNotFound", err)
		}
		summaries, err := store.ListEscalationPathSummaries(ctx, application.ListEscalationPathSummariesInput{OrganizationID: firstOrg})
		if err != nil || len(summaries) != 1 || summaries[0].PathKey != "payments" || summaries[0].RevisionCount != 2 {
			t.Fatalf("summaries after refused writes = %+v, %v", summaries, err)
		}
	})

	t.Run("append only records", func(t *testing.T) {
		assertSQLRejected(t, ctx, store, `DELETE FROM escalation_paths WHERE id = $1`,
			"append-only", firstRevision.ID)
		assertSQLRejected(t, ctx, store, `UPDATE escalation_paths SET name = 'renamed' WHERE id = $1`,
			"only is_active and archived_at may change", firstRevision.ID)
		assertSQLRejected(t, ctx, store, `UPDATE escalation_paths SET is_active = is_active WHERE id = $1`,
			"must change is_active", firstRevision.ID)
	})

	t.Run("monotonic revisions", func(t *testing.T) {
		assertSQLRejected(t, ctx, store, `INSERT INTO escalation_paths
			(id, organization_id, path_key, revision, name, document, document_version, created_by_actor_id, created_at)
			VALUES ($1, $2, 'payments', 9, 'Skipped', '{}', 2, $3, $4)`,
			"revision must be", uuid.NewString(), firstOrg, firstMembership, now)
	})

	t.Run("resolution reports missing and inactive keys", func(t *testing.T) {
		resolved, err := store.ResolveEscalationPaths(ctx, application.ResolveEscalationPathsInput{
			OrganizationID: firstOrg, PathKeys: []string{"payments", "missing"},
		})
		if err != nil || !slices.Equal(resolved.Missing, []string{"missing"}) || !slices.Equal(resolved.Inactive, []string{"payments"}) {
			t.Fatalf("resolved = %+v, %v", resolved, err)
		}
		// The key exists only in the first organization.
		resolved, err = store.ResolveEscalationPaths(ctx, application.ResolveEscalationPathsInput{
			OrganizationID: secondOrg, PathKeys: []string{"payments"},
		})
		if err != nil || !slices.Equal(resolved.Missing, []string{"payments"}) {
			t.Fatalf("cross-tenant resolution = %+v, %v", resolved, err)
		}
	})

	t.Run("activation preconditions", func(t *testing.T) {
		expectNone := (*int64)(nil)
		activated, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
			OrganizationID: firstOrg, PathKey: "payments", Revision: 1,
			ExpectedActiveRevision: expectNone, ActorID: firstMembership, Now: now,
		})
		if err != nil || !activated.IsActive {
			t.Fatalf("activate revision 1 = %+v, %v", activated, err)
		}
		stale := int64(2)
		if _, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
			OrganizationID: firstOrg, PathKey: "payments", Revision: 2,
			ExpectedActiveRevision: &stale, ActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrPreconditionFailed) {
			t.Fatalf("stale activation = %v, want ErrPreconditionFailed", err)
		}
		expected := int64(1)
		var successes int
		var mu sync.Mutex
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
					OrganizationID: firstOrg, PathKey: "payments", Revision: 2,
					ExpectedActiveRevision: &expected, ActorID: firstMembership, Now: now,
				})
				if err == nil {
					mu.Lock()
					successes++
					mu.Unlock()
				} else if !errors.Is(err, domain.ErrPreconditionFailed) {
					t.Errorf("concurrent activation = %v", err)
				}
			}()
		}
		wg.Wait()
		if successes != 1 {
			t.Fatalf("concurrent activations succeeded %d times, want exactly 1", successes)
		}
		active, err := store.GetActiveEscalationPathRevision(ctx, application.GetActiveEscalationPathRevisionInput{
			OrganizationID: firstOrg, PathKey: "payments",
		})
		if err != nil || active.Revision != 2 {
			t.Fatalf("active revision = %+v, %v, want revision 2", active, err)
		}
		resolved, err := store.ResolveEscalationPaths(ctx, application.ResolveEscalationPathsInput{
			OrganizationID: firstOrg, PathKeys: []string{"payments"},
		})
		if err != nil || len(resolved.Missing) != 0 || len(resolved.Inactive) != 0 {
			t.Fatalf("active key resolution = %+v, %v", resolved, err)
		}
	})

	t.Run("archive ends the path and keeps it readable", func(t *testing.T) {
		first := createRoutingTestPathRevision(t, ctx, store, firstOrg, firstMembership, "archive-me", routingTestPathDocument(), now)
		if _, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
			OrganizationID: firstOrg, PathKey: "archive-me", Revision: first.Revision, ActorID: firstMembership, Now: now,
		}); err != nil {
			t.Fatalf("activate archive-me: %v", err)
		}
		if _, err := store.ArchiveEscalationPath(ctx, application.ArchiveEscalationPathInput{
			OrganizationID: secondOrg, PathKey: "archive-me", ActorID: secondMembership, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-tenant archive = %v, want ErrNotFound", err)
		}
		archivedAt := now.Add(time.Minute)
		archived, err := store.ArchiveEscalationPath(ctx, application.ArchiveEscalationPathInput{
			OrganizationID: firstOrg, PathKey: "archive-me", ActorID: firstMembership, Now: archivedAt,
		})
		if err != nil || len(archived) != 1 || archived[0].ID != first.ID || archived[0].IsActive || archived[0].ArchivedAt == nil || !archived[0].ArchivedAt.Equal(archivedAt) {
			t.Fatalf("archive = %+v, %v", archived, err)
		}

		// Nothing is active and the key resolves as missing, but the
		// path and its revisions stay readable, marked archived.
		if _, err := store.GetActiveEscalationPathRevision(ctx, application.GetActiveEscalationPathRevisionInput{OrganizationID: firstOrg, PathKey: "archive-me"}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("active revision after archive = %v, want ErrNotFound", err)
		}
		revision, err := store.GetEscalationPathRevision(ctx, application.GetEscalationPathRevisionInput{OrganizationID: firstOrg, PathKey: "archive-me", Revision: 1})
		if err != nil || revision.IsActive || revision.ArchivedAt == nil || !revision.ArchivedAt.Equal(archivedAt) {
			t.Fatalf("revision after archive = %+v, %v", revision, err)
		}
		if page, err := store.ListEscalationPathRevisions(ctx, application.ListEscalationPathRevisionsInput{OrganizationID: firstOrg, PathKey: "archive-me"}); err != nil || len(page.Revisions) != 1 || page.Revisions[0].ArchivedAt == nil {
			t.Fatalf("revisions after archive = %+v, %v", page, err)
		}
		listed := func(status application.EscalationPathStatus) *domain.EscalationPathSummary {
			summaries, err := store.ListEscalationPathSummaries(ctx, application.ListEscalationPathSummariesInput{OrganizationID: firstOrg, Status: status})
			if err != nil {
				t.Fatal(err)
			}
			for _, summary := range summaries {
				if summary.PathKey == "archive-me" {
					return &summary
				}
			}
			return nil
		}
		if summary := listed(""); summary != nil {
			t.Fatalf("archived path in the default list: %+v", summary)
		}
		if summary := listed(application.EscalationPathStatusArchived); summary == nil || summary.ArchivedAt == nil || !summary.ArchivedAt.Equal(archivedAt) || summary.ActiveRevision != nil {
			t.Fatalf("archived list = %+v", summary)
		}
		if summary := listed(application.EscalationPathStatusAll); summary == nil || summary.ArchivedAt == nil {
			t.Fatalf("all list = %+v", summary)
		}
		if live := listed(application.EscalationPathStatusLive); live != nil {
			t.Fatalf("archived path in the live list: %+v", live)
		}
		if resolved, err := store.ResolveEscalationPaths(ctx, application.ResolveEscalationPathsInput{OrganizationID: firstOrg, PathKeys: []string{"archive-me"}}); err != nil || !slices.Equal(resolved.Missing, []string{"archive-me"}) {
			t.Fatalf("archived key resolution = %+v, %v, want missing", resolved, err)
		}

		// Archived is final: no activation, no new revision, no second
		// archive, and the key can never be created again.
		if _, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
			OrganizationID: firstOrg, PathKey: "archive-me", Revision: 1, ActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrEscalationPathArchived) {
			t.Fatalf("activating an archived revision = %v, want ErrEscalationPathArchived", err)
		}
		if _, err := store.ArchiveEscalationPath(ctx, application.ArchiveEscalationPathInput{
			OrganizationID: firstOrg, PathKey: "archive-me", ActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrEscalationPathArchived) {
			t.Fatalf("second archive = %v, want ErrEscalationPathArchived", err)
		}
		if _, err := store.CreateEscalationPathRevision(ctx, application.CreateEscalationPathRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: firstOrg, PathKey: "archive-me",
			Name: "Reborn", Document: routingTestPathDocument(), DocumentVersion: 2,
			CreatedByActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrEscalationPathArchived) {
			t.Fatalf("revision on an archived key = %v, want ErrEscalationPathArchived", err)
		}
		if _, err := store.CreateEscalationPath(ctx, application.CreateEscalationPathInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: firstOrg, PathKey: "archive-me",
			Name: "Reborn", Document: routingTestPathDocument(), DocumentVersion: 2,
			CreatedByActorID: firstMembership, Now: now,
		}); !errors.Is(err, domain.ErrEscalationPathExists) {
			t.Fatalf("create on an archived key = %v, want ErrEscalationPathExists", err)
		}

		// The rows stay, frozen: neither un-archived nor re-activated.
		assertSQLRejected(t, ctx, store, `UPDATE escalation_paths SET archived_at = NULL WHERE id = $1`, "frozen", first.ID)
		assertSQLRejected(t, ctx, store, `UPDATE escalation_paths SET is_active = true WHERE id = $1`, "frozen", first.ID)
		assertSQLRejected(t, ctx, store, `DELETE FROM escalation_paths WHERE id = $1`, "append-only", first.ID)

		var eventTypes []string
		rows, err := store.Pool().Query(ctx, `SELECT event_type FROM test_audit_events
			WHERE organization_id = $1 AND data->>'path_key' = 'archive-me' ORDER BY sequence`, firstOrg)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var eventType string
			if err := rows.Scan(&eventType); err != nil {
				t.Fatal(err)
			}
			eventTypes = append(eventTypes, eventType)
		}
		rows.Close()
		if want := []string{"escalation_path.created", "escalation_path.activated", "escalation_path.archived"}; !slices.Equal(eventTypes, want) {
			t.Fatalf("audit trail = %v, want %v", eventTypes, want)
		}
	})

	t.Run("snapshot persists with the request", func(t *testing.T) {
		ownerDocument := json.RawMessage(`{"working_hours":[],"nodes":[{"id":"` + uuid.NewString() +
			`","type":"level","targets":[{"type":"user","id":"` + string(firstMembership) + `"}],"escalate_after":"15m"}]}`)
		ownerRevision := createRoutingTestPathRevision(t, ctx, store, firstOrg, firstMembership, "owner-only", ownerDocument, now)
		if _, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
			OrganizationID: firstOrg, PathKey: "owner-only", Revision: ownerRevision.Revision,
			ActorID: firstMembership, Now: now,
		}); err != nil {
			t.Fatalf("activate owner-only path: %v", err)
		}

		var userID domain.ID
		if err := store.Pool().QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE id = $1`,
			firstMembership).Scan(&userID); err != nil {
			t.Fatalf("resolve membership user: %v", err)
		}
		agent, err := store.CreateAgent(ctx, application.CreateAgentInput{
			OrganizationID: firstOrg, ActorID: firstMembership, Name: "routing-agent",
			AgentSlug: "billing", AgentQuota: application.UnlimitedQuota(), Now: now,
		})
		if err != nil {
			t.Fatalf("CreateAgent: %v", err)
		}
		secret := sha256.Sum256([]byte("routing-secret"))
		instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
			OrganizationID: firstOrg, AgentID: agent.ID, ActorID: userID, Name: "routing-instance",
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
		request := domain.ApprovalRequest{
			Timeout: "1h",
			ID:      domain.ID(uuid.NewString()), OrganizationID: firstOrg,
			AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
			Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount":1}`),
			IdempotencyKey: "routing-req-1", SemanticHash: "routing-hash-1",
			Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour),
			CreatedAt: now, UpdatedAt: now,
		}
		initialization := pendingApprovalInitialization(request)
		initialization.Routing = &domain.ApprovalRequestRouting{
			RequestID: request.ID, OrganizationID: firstOrg,
			Outcome: domain.RoutingEscalated, Reason: domain.RoutingReasonPipelineDefault,
			EscalationPathKey: "owner-only", EscalationPathRevisionID: ownerRevision.ID,
			Urgency: "interrupt", CreatedAt: now,
		}
		if _, err := store.InitializeApproval(ctx, initialization); err != nil {
			t.Fatalf("InitializeApproval with routing: %v", err)
		}
		view, err := store.GetApprovalRouting(ctx, application.GetApprovalRoutingInput{OrganizationID: firstOrg, RequestID: request.ID})
		if err != nil || view.Outcome != domain.RoutingEscalated || view.Reason != domain.RoutingReasonPipelineDefault ||
			view.EscalationPathKey != "owner-only" || view.EscalationPathName != "Payments escalation path" ||
			view.EscalationPathRevisionID != ownerRevision.ID || view.Urgency != "interrupt" {
			t.Fatalf("routing view = %+v, %v", view, err)
		}
		assertSQLRejected(t, ctx, store, `UPDATE approval_request_routing SET urgency = 'standard'
			WHERE request_id = $1`, "append-only", request.ID)

		recipients, err := store.ResolveApprovalRecipients(ctx, application.ResolveApprovalRecipientsInput{
			OrganizationID: firstOrg, RequestID: request.ID,
		})
		if err != nil || recipients.Outcome != domain.RoutingEscalated || recipients.Urgency != domain.NotificationInterrupt {
			t.Fatalf("recipients = %+v, %v", recipients, err)
		}
		if ids := recipients.MembershipIDs(); !slices.Equal(ids, []domain.ID{firstMembership}) {
			t.Fatalf("pinned path targets resolved to %v, want the owner", ids)
		}

		// Archiving the path afterwards leaves the pinned revision readable:
		// the request keeps the document it was routed on.
		if _, err := store.ArchiveEscalationPath(ctx, application.ArchiveEscalationPathInput{
			OrganizationID: firstOrg, PathKey: "owner-only", ActorID: firstMembership, Now: now,
		}); err != nil {
			t.Fatalf("archive the pinned path: %v", err)
		}
		view, err = store.GetApprovalRouting(ctx, application.GetApprovalRoutingInput{OrganizationID: firstOrg, RequestID: request.ID})
		if err != nil || view.EscalationPathRevisionID != ownerRevision.ID || view.EscalationPathName != "Payments escalation path" {
			t.Fatalf("routing view after archive = %+v, %v", view, err)
		}
		recipients, err = store.ResolveApprovalRecipients(ctx, application.ResolveApprovalRecipientsInput{
			OrganizationID: firstOrg, RequestID: request.ID,
		})
		if err != nil || recipients.Outcome != domain.RoutingEscalated || !slices.Equal(recipients.MembershipIDs(), []domain.ID{firstMembership}) {
			t.Fatalf("recipients after archive = %+v, %v", recipients, err)
		}
	})

	t.Run("history pages by cursor", func(t *testing.T) {
		const pathKey = "paged"
		for index := 0; index < 7; index++ {
			createRoutingTestPathRevision(t, ctx, store, firstOrg, firstMembership, pathKey, routingTestPathDocument(), now.Add(time.Duration(index)*time.Second))
		}
		assertRevisionPaging(t, func(cursor string, limit int) (revisionPageOf, error) {
			page, err := store.ListEscalationPathRevisions(ctx, application.ListEscalationPathRevisionsInput{
				OrganizationID: firstOrg, PathKey: pathKey, Limit: limit, Cursor: cursor,
			})
			result := revisionPageOf{nextCursor: page.NextCursor, totalCount: page.TotalCount}
			for _, revision := range page.Revisions {
				result.numbers = append(result.numbers, revision.Revision)
			}
			return result, err
		}, descending(7), 3)
	})
}

// branchingPathDocument routes on the request's tool: a request whose tool
// equals tool reaches thenTarget, any other reaches elseTarget (none when
// elseTarget is nil). The snapshot's facts key the condition as
// "/" + conditionID; tests pin the facts themselves.
func branchingPathDocument(t *testing.T, conditionID, tool string, thenTarget routing.Target, elseTarget *routing.Target) json.RawMessage {
	t.Helper()
	value, _ := json.Marshal(tool)
	node := routing.Node{
		ID: uuid.NewString(), Type: routing.NodeIfElse,
		Conditions: []routing.Condition{{Type: routing.ConditionRequest, When: &approvalblock.Condition{
			ID: conditionID, Field: "/request/tool", Operator: approvalblock.Equals, Value: value,
		}}},
		Then: []routing.Node{{ID: uuid.NewString(), Type: routing.NodeLevel, Targets: []routing.Target{thenTarget}, EscalateAfter: "15m"}},
	}
	if elseTarget != nil {
		node.Else = []routing.Node{{ID: uuid.NewString(), Type: routing.NodeLevel, Targets: []routing.Target{*elseTarget}, EscalateAfter: "15m"}}
	}
	document := routing.PathDocument{WorkingHours: []routing.WorkingHoursSet{}, Nodes: []routing.Node{node}}
	if err := document.Validate(); err != nil {
		t.Fatalf("branching path document: %v", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func routingTestPathDocument() json.RawMessage {
	return json.RawMessage(`{"working_hours":[{"id":"office","name":"Office hours","timezone":"UTC","intervals":[{"weekdays":["mon"],"start":"09:00","end":"17:00"}]}],"nodes":[{"id":"` + uuid.NewString() + `","type":"defer","working_hours_id":"office"}]}`)
}

// createRoutingTestPathRevision writes the next revision of the key,
// creating the path when it has none yet.
func createRoutingTestPathRevision(t *testing.T, ctx context.Context, store *Store, organizationID, membershipID domain.ID, pathKey string, document json.RawMessage, now time.Time) domain.EscalationPathRevision {
	t.Helper()
	input := application.CreateEscalationPathRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: organizationID, PathKey: pathKey,
		Name: "Payments escalation path", Document: document, DocumentVersion: 2,
		CreatedByActorID: membershipID, Now: now,
	}
	revision, err := store.CreateEscalationPath(ctx, application.CreateEscalationPathInput(input))
	if errors.Is(err, domain.ErrEscalationPathExists) {
		revision, err = store.CreateEscalationPathRevision(ctx, input)
	}
	if err != nil {
		t.Fatalf("create %s path revision: %v", pathKey, err)
	}
	return revision
}
