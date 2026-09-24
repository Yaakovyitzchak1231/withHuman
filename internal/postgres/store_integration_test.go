package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// Exercises the store against a real PostgreSQL instance: migrations with
// their triggers, the serializable transaction paths, idempotent retries,
// and the append-only and terminal-state invariants.
func TestStoreAgainstRealPostgres(t *testing.T) {
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
	// Applying again must be a no-op.
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatalf("re-apply migrations: %v", err)
	}
	recordAudit(t, store)

	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID := domain.ID(uuid.NewString())
	userID := domain.ID(uuid.NewString())
	membershipID := domain.ID(uuid.NewString())

	hasOwner, err := store.HasOwner(ctx)
	if err != nil || hasOwner {
		t.Fatalf("HasOwner before bootstrap = %v, %v", hasOwner, err)
	}
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "acme", OrganizationName: "Acme",
		UserID: userID, MembershipID: membershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	if hasOwner, err = store.HasOwner(ctx); err != nil || !hasOwner {
		t.Fatalf("HasOwner after bootstrap = %v, %v", hasOwner, err)
	}

	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{
		OrganizationID: orgID, ActorID: membershipID, Name: "test-agent",
		AgentSlug: "release", AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	secret := sha256.Sum256([]byte("test-secret"))
	credentialID := domain.ID(uuid.NewString())
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
		OrganizationID: orgID, AgentID: agent.ID, ActorID: userID, Name: "test-instance",
		Metadata: json.RawMessage(`{"host":"test"}`),
		Credential: application.AgentCredentialRecord{
			ID: credentialID, SecretHash: secret[:],
			IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgentInstanceWithCredential: %v", err)
	}
	principal, err := store.ResolveAgentCredential(ctx, application.ResolveAgentCredentialInput{
		CredentialID: credentialID, SecretHash: secret[:], Now: now,
	})
	if err != nil {
		t.Fatalf("ResolveAgentCredential: %v", err)
	}
	if principal.ActorID != agent.ID {
		t.Fatalf("resolved wrong agent: %+v", principal)
	}
	if principal.AgentSlug != agent.AgentSlug {
		t.Fatalf("credential resolution lost agent type: %+v", principal)
	}

	request := domain.ApprovalRequest{
		Timeout: "1h",
		ID:      domain.ID(uuid.NewString()), OrganizationID: orgID,
		AgentID: agent.ID, AgentInstanceID: instance.ID,
		AgentSlug: agent.AgentSlug,
		Tool:      "deploy", Arguments: json.RawMessage(`{"env":"prod"}`),
		AgentReasoning: "test", IdempotencyKey: "req-1", SemanticHash: "hash-1",
		Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}
	created, err := store.InitializeApproval(ctx, pendingApprovalInitialization(request))
	if err != nil {
		t.Fatalf("InitializeApproval: %v", err)
	}
	if count, err := store.CountPendingApprovals(ctx, application.CountPendingApprovalsInput{OrganizationID: orgID}); err != nil || count != 1 {
		t.Fatalf("CountPendingApprovals = %d, %v, want 1", count, err)
	}
	duplicate, err := store.InitializeApproval(ctx, pendingApprovalInitialization(request))
	if err != nil || duplicate.ID != created.ID {
		t.Fatalf("InitializeApproval idempotency: %v (ids %s vs %s)", err, duplicate.ID, created.ID)
	}
	workflowStarts, err := store.ListApprovalWorkflowStarts(ctx, application.ListApprovalWorkflowStartsInput{Limit: 10})
	if err != nil || len(workflowStarts) != 1 || workflowStarts[0].RequestID != created.ID {
		t.Fatalf("pending initialization outbox = %#v, %v", workflowStarts, err)
	}
	if err := store.MarkApprovalWorkflowStarted(ctx, application.MarkApprovalWorkflowStartedInput{
		OrganizationID: orgID, RequestID: created.ID, PublishedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("MarkApprovalWorkflowStarted: %v", err)
	}
	workflowStarts, err = store.ListApprovalWorkflowStarts(ctx, application.ListApprovalWorkflowStartsInput{Limit: 10})
	if err != nil || len(workflowStarts) != 0 {
		t.Fatalf("published workflow handoff remained visible: %#v, %v", workflowStarts, err)
	}
	failedPendingRequest := request
	failedPendingRequest.ID = domain.ID(uuid.NewString())
	failedPendingRequest.IdempotencyKey = "req-pending-rollback"
	failedPending := pendingApprovalInitialization(failedPendingRequest)
	failedPending.AuditEvents[1].ID = failedPending.AuditEvents[0].ID
	if _, err := store.InitializeApproval(ctx, failedPending); err == nil {
		t.Fatal("duplicate audit event did not fail pending initialization")
	}
	if _, err := store.GetApproval(ctx, application.GetApprovalInput{OrganizationID: orgID, RequestID: failedPendingRequest.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed pending initialization left a partial request: %v", err)
	}
	var failedPendingOutboxCount int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE organization_id=$1 AND payload->>'request_id'=$2`, orgID, failedPendingRequest.ID).Scan(&failedPendingOutboxCount); err != nil || failedPendingOutboxCount != 0 {
		t.Fatalf("failed pending initialization left an outbox event: count=%d err=%v", failedPendingOutboxCount, err)
	}

	automaticRequest := request
	automaticRequest.ID = domain.ID(uuid.NewString())
	automaticRequest.IdempotencyKey = "req-pipeline"
	automaticRequest.Status = domain.ApprovalApproved
	automaticRequest.UpdatedAt = now.Add(time.Second)
	failedInitialization := automaticApprovalInitialization(automaticRequest, true)
	if _, err := store.InitializeApproval(ctx, failedInitialization); err == nil {
		t.Fatal("duplicate audit event did not fail approval initialization")
	}
	if _, err := store.GetApproval(ctx, application.GetApprovalInput{OrganizationID: orgID, RequestID: automaticRequest.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed initialization left a partial request: %v", err)
	}
	initializedAutomatically, err := store.InitializeApproval(ctx, automaticApprovalInitialization(automaticRequest, false))
	if err != nil {
		t.Fatalf("automatic InitializeApproval: %v", err)
	}
	if initializedAutomatically.Status != domain.ApprovalApproved || initializedAutomatically.Decision == nil ||
		initializedAutomatically.Decision.Source != domain.DecisionSourcePipeline || initializedAutomatically.Decision.Auth.Method != "pipeline" {
		t.Fatalf("automatic decision was not stored atomically: %#v", initializedAutomatically)
	}
	var automaticOutboxCount int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE organization_id=$1 AND payload->>'request_id'=$2`, orgID, automaticRequest.ID).Scan(&automaticOutboxCount); err != nil || automaticOutboxCount != 0 {
		t.Fatalf("automatic request created human workflow handoff: count=%d err=%v", automaticOutboxCount, err)
	}
	rows, err := store.Pool().Query(ctx, `SELECT event_type FROM test_audit_events
		WHERE organization_id=$1 AND subject_type='approval_request' AND subject_id=$2 ORDER BY sequence`, orgID, automaticRequest.ID)
	if err != nil {
		t.Fatal(err)
	}
	var automaticEventTypes []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		automaticEventTypes = append(automaticEventTypes, eventType)
	}
	rows.Close()
	wantAutomaticEventTypes := []string{"approval.created", "request_approval_pipeline.started", "request_approval_pipeline.completed", "approval.decided"}
	if strings.Join(automaticEventTypes, ",") != strings.Join(wantAutomaticEventTypes, ",") {
		t.Fatalf("automatic audit order = %v, want %v", automaticEventTypes, wantAutomaticEventTypes)
	}

	expired, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{
		OrganizationID: orgID, RequestID: created.ID, Now: now.Add(2 * time.Hour),
	})
	if err != nil || expired.Status != domain.ApprovalExpired {
		t.Fatalf("ExpireApproval: %v status=%s", err, expired.Status)
	}
	if expired.Decision == nil || expired.Decision.Source != domain.DecisionSourceSystem || expired.Decision.Auth.Method != "system" {
		t.Fatalf("expiration provenance is incomplete: %#v", expired.Decision)
	}
	again, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{
		OrganizationID: orgID, RequestID: created.ID, Now: now.Add(3 * time.Hour),
	})
	if err != nil || again.Status != domain.ApprovalExpired {
		t.Fatalf("ExpireApproval idempotent retry: %v", err)
	}
	if count, err := store.CountPendingApprovals(ctx, application.CountPendingApprovalsInput{OrganizationID: orgID}); err != nil || count != 0 {
		t.Fatalf("CountPendingApprovals after expiry = %d, %v, want 0", count, err)
	}

	humanRequest := request
	humanRequest.ID = domain.ID(uuid.NewString())
	humanRequest.IdempotencyKey = "req-2"
	createdForHuman, err := store.InitializeApproval(ctx, pendingApprovalInitialization(humanRequest))
	if err != nil {
		t.Fatalf("InitializeApproval for human decision: %v", err)
	}
	operation := domain.DecisionOperation{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, RequestID: createdForHuman.ID,
		Status: domain.OperationPending, CreatedAt: now,
	}
	operation, err = store.CreateDecisionOperation(ctx, application.CreateDecisionOperationInput{
		Operation: operation, IdempotencyKey: "decision-1", IntentHash: "approve-request-2",
	})
	if err != nil {
		t.Fatalf("CreateDecisionOperation: %v", err)
	}
	operation, err = store.ApplyDecision(ctx, application.ApplyDecisionInput{
		Command: domain.DecisionCommand{
			OperationID: operation.ID, RequestID: createdForHuman.ID, OrganizationID: orgID,
			Principal: domain.Principal{
				ActorType: domain.ActorHuman, ActorID: userID, OrganizationID: orgID, MembershipID: membershipID,
				Auth: domain.AuthContext{
					Method: "local_password", Assurance: "single_factor", AuthenticatedAt: now,
					SessionID: domain.ID(uuid.NewString()),
				},
			},
			Status: domain.ApprovalApproved, Channel: "web", IdempotencyKey: "decision-1",
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("ApplyDecision: %v", err)
	}
	if operation.Decision == nil || operation.Decision.Source != domain.DecisionSourceHuman {
		t.Fatalf("human decision provenance is incomplete: %#v", operation.Decision)
	}

	for _, decision := range []*domain.Decision{initializedAutomatically.Decision, operation.Decision} {
		if decision.ExpiresAt == nil || !decision.ExpiresAt.Equal(decision.DecidedAt.Add(5*time.Minute)) {
			t.Fatalf("stored approval expiry: %+v", decision)
		}
	}

	// The review queue is for humans: the request that expired while
	// waiting and the human-decided one appear, the automatic decision does
	// not. The unfiltered listing still exposes it.
	queue, err := store.ListApprovals(ctx, application.ListApprovalsInput{OrganizationID: orgID, Limit: 10, HumanReviewOnly: true})
	if err != nil {
		t.Fatalf("ListApprovals for the review queue: %v", err)
	}
	if ids := approvalIDs(queue); !slices.Contains(ids, created.ID) || !slices.Contains(ids, createdForHuman.ID) || slices.Contains(ids, automaticRequest.ID) {
		t.Fatalf("review queue = %v, want the expired and human-decided requests without the automatic one", ids)
	}
	everything, err := store.ListApprovals(ctx, application.ListApprovalsInput{OrganizationID: orgID, Limit: 10})
	if err != nil || !slices.Contains(approvalIDs(everything), automaticRequest.ID) {
		t.Fatalf("unfiltered ListApprovals hid the automatic decision: %v, %v", approvalIDs(everything), err)
	}

	// The append-only and terminal-state triggers must hold even against
	// direct SQL.
	if _, err := store.Pool().Exec(ctx, `UPDATE decisions SET note = 'mutated'`); err == nil ||
		!strings.Contains(err.Error(), "append-only") {
		t.Fatalf("decisions append-only trigger did not fire: %v", err)
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE approval_requests SET status = 'pending' WHERE id = $1`, created.ID); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("terminal approval trigger did not fire: %v", err)
	}

	// Every write of the scenario handed its event to the sink inside the
	// transaction that committed it.
	for _, eventType := range []string{"approval.created", "approval.decided", "approval.expired"} {
		if len(auditEvents(t, ctx, store, `organization_id = $1 AND event_type = $2`, orgID, eventType)) == 0 {
			t.Fatalf("no %s event reached the sink", eventType)
		}
	}
	metrics, err := store.OperationalMetrics(ctx)
	if err != nil || metrics.Expired != 1 || metrics.Approved != 2 {
		t.Fatalf("OperationalMetrics: %+v %v", metrics, err)
	}
}

func pendingApprovalInitialization(request domain.ApprovalRequest) application.InitializeApprovalInput {
	data, _ := json.Marshal(map[string]any{
		"agent_instance_id": request.AgentInstanceID,
		"tool":              request.Tool,
		"deadline_at":       request.DeadlineAt,
	})
	completed, _ := json.Marshal(map[string]any{
		"pipeline_evaluation_id": uuid.NewString(),
		"agent_slug":             request.AgentSlug,
		"outcome":                "human",
		"reason_code":            "no_active_revision",
	})
	return application.InitializeApprovalInput{
		Request: request,
		AuditEvents: []domain.AuditEvent{{
			ID: domain.ID(uuid.NewString()), OrganizationID: request.OrganizationID,
			EventType: "approval.created", ActorType: domain.ActorAgent, ActorID: request.AgentID,
			SubjectType: "approval_request", SubjectID: request.ID, Data: data, OccurredAt: request.CreatedAt,
		}, {
			ID: domain.ID(uuid.NewString()), OrganizationID: request.OrganizationID,
			EventType: "request_approval_pipeline.completed", ActorType: domain.ActorSystem,
			SubjectType: "approval_request", SubjectID: request.ID, Data: completed, OccurredAt: request.CreatedAt,
		}},
	}
}

func approvalIDs(requests []domain.ApprovalRequest) []domain.ID {
	ids := make([]domain.ID, len(requests))
	for index, request := range requests {
		ids[index] = request.ID
	}
	return ids
}

func automaticApprovalInitialization(request domain.ApprovalRequest, duplicateAuditID bool) application.InitializeApprovalInput {
	evaluationID := domain.ID(uuid.NewString())
	decision := &domain.Decision{
		ID: domain.ID(uuid.NewString()), RequestID: request.ID, OrganizationID: request.OrganizationID,
		Source: domain.DecisionSourcePipeline, Status: request.Status, Channel: "pipeline",
		Auth:           domain.AuthContext{Method: "pipeline", Assurance: "automatic", AuthenticatedAt: request.UpdatedAt},
		IdempotencyKey: "pipeline:" + string(evaluationID), DecidedAt: request.UpdatedAt,
	}
	created := pendingApprovalInitialization(request).AuditEvents[0]
	startedID := domain.ID(uuid.NewString())
	completedID := domain.ID(uuid.NewString())
	decidedID := domain.ID(uuid.NewString())
	if duplicateAuditID {
		decidedID = completedID
	}
	startedData, _ := json.Marshal(map[string]any{
		"pipeline_evaluation_id": evaluationID,
		"agent_slug":             request.AgentSlug,
		"pipeline_id":            uuid.NewString(),
		"revision":               1,
		"block_count":            1,
		"runtime_fingerprint":    "test",
	})
	completedData, _ := json.Marshal(map[string]any{
		"pipeline_evaluation_id": evaluationID,
		"agent_slug":             request.AgentSlug,
		"outcome":                "approve",
		"reason_code":            "block_outcome",
	})
	decidedData, _ := json.Marshal(map[string]any{
		"pipeline_evaluation_id": evaluationID,
		"source":                 "pipeline",
		"status":                 request.Status,
	})
	return application.InitializeApprovalInput{
		Request: request, Decision: decision,
		AuditEvents: []domain.AuditEvent{
			created,
			{ID: startedID, OrganizationID: request.OrganizationID, EventType: "request_approval_pipeline.started", ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID, Data: startedData, OccurredAt: request.UpdatedAt},
			{ID: completedID, OrganizationID: request.OrganizationID, EventType: "request_approval_pipeline.completed", ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID, Data: completedData, OccurredAt: request.UpdatedAt},
			{ID: decidedID, OrganizationID: request.OrganizationID, EventType: "approval.decided", ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID, Data: decidedData, OccurredAt: request.UpdatedAt},
		},
	}
}
