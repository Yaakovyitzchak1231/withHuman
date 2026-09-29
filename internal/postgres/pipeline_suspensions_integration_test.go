package postgres

import (
	"context"
	"crypto/sha256"
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

func TestPipelineSuspensionsAgainstRealPostgres(t *testing.T) {
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
	orgID := domain.ID(uuid.NewString())
	ownerID := domain.ID(uuid.NewString())
	ownerMembershipID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "suspensions", OrganizationName: "Suspensions",
		UserID: ownerID, MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "judged-agent",
		AgentSlug: "billing", AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	secret := sha256.Sum256([]byte("suspension-secret"))
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
		OrganizationID: orgID, AgentID: agent.ID, ActorID: ownerID, Name: "judged-instance",
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
	revision := createPipelineTestRevision(t, ctx, store, orgID, ownerMembershipID, domain.EntryPipelineKey, []application.RequestApprovalBlockSnapshotInput{
		pipelineTestBlock("judge", `{"on_match":"human"}`),
	}, now)

	event := func(request domain.ApprovalRequest, eventType string, actorType domain.ActorType, actorID domain.ID, data string) domain.AuditEvent {
		return domain.AuditEvent{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, EventType: eventType,
			ActorType: actorType, ActorID: actorID, SubjectType: "approval_request", SubjectID: request.ID,
			Data: json.RawMessage(data), OccurredAt: now,
		}
	}
	suspend := func(key string) (domain.ApprovalRequest, domain.PipelineSuspension) {
		t.Helper()
		request := domain.ApprovalRequest{
			Timeout: "1h",
			ID:      domain.ID(uuid.NewString()), OrganizationID: orgID,
			AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
			Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount":1}`),
			IdempotencyKey: key, SemanticHash: "hash-" + key,
			Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour),
			CreatedAt: now, UpdatedAt: now,
		}
		suspension := domain.PipelineSuspension{
			RequestID: request.ID, OrganizationID: orgID, EvaluationID: domain.ID(uuid.NewString()),
			PinnedRevisions: map[string]domain.ID{domain.EntryPipelineKey: revision.ID}, ResumePosition: 0,
			PreviousMetadata: map[string]json.RawMessage{"entry/risk": json.RawMessage(`{"risk":"low"}`)},
			StartedAt:        now, SuspendedAt: now,
		}
		created, err := store.InitializeApproval(ctx, application.InitializeApprovalInput{
			Request: request,
			AuditEvents: []domain.AuditEvent{
				event(request, "approval.created", domain.ActorAgent, agent.ID, `{"tool":"stripe.refund"}`),
				event(request, "request_approval_pipeline.started", domain.ActorSystem, "", `{"block_count":1}`),
				event(request, "request_approval_pipeline.suspended", domain.ActorSystem, "", `{"resume_position":0}`),
			},
			Suspension: &suspension,
		})
		if err != nil {
			t.Fatalf("InitializeApproval %s: %v", key, err)
		}
		if created.Status != domain.ApprovalPending {
			t.Fatalf("suspended request must be pending, got %s", created.Status)
		}
		return created, suspension
	}

	t.Run("resume decides once", func(t *testing.T) {
		request, suspension := suspend("resume-decides")
		stored, err := store.GetPipelineSuspension(ctx, application.GetPipelineSuspensionInput{OrganizationID: orgID, RequestID: request.ID})
		if err != nil {
			t.Fatalf("GetPipelineSuspension: %v", err)
		}
		var risk map[string]string
		_ = json.Unmarshal(stored.PreviousMetadata["entry/risk"], &risk)
		if !stored.Open() || stored.EntryPipelineID() != revision.ID || len(stored.PinnedRevisions) != 1 || risk["risk"] != "low" {
			t.Fatalf("suspension did not round trip: %#v", stored)
		}
		decision := &domain.Decision{
			ID: domain.ID(uuid.NewString()), RequestID: request.ID, OrganizationID: orgID,
			Source: domain.DecisionSourcePipeline, Status: domain.ApprovalApproved, Channel: "pipeline",
			Auth:           domain.AuthContext{Method: "pipeline", Assurance: "automatic", AuthenticatedAt: now},
			IdempotencyKey: "pipeline:" + string(suspension.EvaluationID), DecidedAt: now.Add(time.Second),
		}
		resume := application.ResumeApprovalInput{
			OrganizationID: orgID, RequestID: request.ID, EvaluationID: suspension.EvaluationID, Decision: decision,
			AuditEvents: []domain.AuditEvent{
				event(request, "request_approval_block.evaluated", domain.ActorSystem, "", `{"outcome":"approve"}`),
				event(request, "request_approval_pipeline.completed", domain.ActorSystem, "", `{"outcome":"approve"}`),
				event(request, "approval.decided", domain.ActorSystem, "", `{"status":"approved"}`),
			},
			Now: now.Add(time.Second),
		}
		resumed, err := store.ResumeApproval(ctx, resume)
		if err != nil {
			t.Fatalf("ResumeApproval: %v", err)
		}
		if resumed.Status != domain.ApprovalApproved || resumed.Decision == nil || resumed.Decision.Source != domain.DecisionSourcePipeline {
			t.Fatalf("resume did not apply the decision: %#v", resumed)
		}
		closed, err := store.GetPipelineSuspension(ctx, application.GetPipelineSuspensionInput{OrganizationID: orgID, RequestID: request.ID})
		if err != nil || closed.Open() {
			t.Fatalf("suspension must be closed after resume: %#v %v", closed, err)
		}

		// A retry of the same resume changes nothing and appends nothing.
		var before int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM test_audit_events WHERE subject_id=$1`, request.ID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		resume.Decision.ID = domain.ID(uuid.NewString())
		again, err := store.ResumeApproval(ctx, resume)
		if err != nil {
			t.Fatalf("second ResumeApproval: %v", err)
		}
		var after, decisions int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM test_audit_events WHERE subject_id=$1`, request.ID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM decisions WHERE request_id=$1`, request.ID).Scan(&decisions); err != nil {
			t.Fatal(err)
		}
		if again.Decision.ExpiresAt == nil || !again.Decision.ExpiresAt.Equal(resumed.Decision.DecidedAt.Add(5*time.Minute)) {
			t.Fatal("resumed decision expiry changed")
		}
		if again.Decision.ID != resumed.Decision.ID || before != after || decisions != 1 {
			t.Fatalf("second resume was not a no-op: decisions=%d events %d -> %d", decisions, before, after)
		}
	})

	t.Run("resume routes a request that still needs a human", func(t *testing.T) {
		request, suspension := suspend("resume-routes")
		resumed, err := store.ResumeApproval(ctx, application.ResumeApprovalInput{
			OrganizationID: orgID, RequestID: request.ID, EvaluationID: suspension.EvaluationID,
			AuditEvents: []domain.AuditEvent{
				event(request, "request_approval_pipeline.completed", domain.ActorSystem, "", `{"outcome":"human"}`),
				event(request, "routing.evaluated", domain.ActorSystem, "", `{"outcome":"default_queue"}`),
			},
			Routing: &domain.ApprovalRequestRouting{
				RequestID: request.ID, OrganizationID: orgID, Outcome: domain.RoutingDefaultQueue,
				Reason: domain.RoutingReasonNoEscalation, Urgency: "standard", CreatedAt: now.Add(time.Second),
			},
			Now: now.Add(time.Second),
		})
		if err != nil {
			t.Fatalf("ResumeApproval: %v", err)
		}
		if resumed.Status != domain.ApprovalPending || resumed.Decision != nil {
			t.Fatalf("a human outcome must leave the request pending: %#v", resumed)
		}
		var routed int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM approval_request_routing WHERE request_id=$1`, request.ID).Scan(&routed); err != nil {
			t.Fatal(err)
		}
		if routed != 1 {
			t.Fatalf("routing snapshot count = %d, want 1", routed)
		}
	})

	t.Run("a human decision before the resume wins", func(t *testing.T) {
		request, suspension := suspend("human-first")
		expired, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{OrganizationID: orgID, RequestID: request.ID, Now: now.Add(time.Second)})
		if err != nil || expired.Status != domain.ApprovalExpired {
			t.Fatalf("ExpireApproval: %v %#v", err, expired)
		}
		resumed, err := store.ResumeApproval(ctx, application.ResumeApprovalInput{
			OrganizationID: orgID, RequestID: request.ID, EvaluationID: suspension.EvaluationID,
			Decision: &domain.Decision{
				ID: domain.ID(uuid.NewString()), RequestID: request.ID, OrganizationID: orgID,
				Source: domain.DecisionSourcePipeline, Status: domain.ApprovalApproved, Channel: "pipeline",
				Auth:           domain.AuthContext{Method: "pipeline", Assurance: "automatic", AuthenticatedAt: now},
				IdempotencyKey: "pipeline:" + string(suspension.EvaluationID), DecidedAt: now.Add(2 * time.Second),
			},
			AuditEvents: []domain.AuditEvent{
				event(request, "request_approval_pipeline.completed", domain.ActorSystem, "", `{"outcome":"approve"}`),
				event(request, "approval.decided", domain.ActorSystem, "", `{"status":"approved"}`),
			},
			Now: now.Add(2 * time.Second),
		})
		if err != nil {
			t.Fatalf("ResumeApproval after expiry: %v", err)
		}
		if resumed.Status != domain.ApprovalExpired {
			t.Fatalf("the earlier terminal state must stand, got %s", resumed.Status)
		}
		var decided int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM test_audit_events WHERE subject_id=$1 AND event_type='approval.decided'`, request.ID).Scan(&decided); err != nil {
			t.Fatal(err)
		}
		if decided != 0 {
			t.Fatalf("a dropped automatic decision must leave no decided event, got %d", decided)
		}
	})

	if _, err := store.GetPipelineSuspension(ctx, application.GetPipelineSuspensionInput{OrganizationID: orgID, RequestID: domain.ID(uuid.NewString())}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown request returned %v, want not found", err)
	}
}
