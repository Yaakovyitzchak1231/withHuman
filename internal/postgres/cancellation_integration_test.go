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

// Exercises the agent-side withdrawal against a real PostgreSQL with the
// production migrations and triggers: the one terminal decision, claim
// closure, the audit record, idempotent retries, and the refusals.
func TestCancelApprovalAgainstRealPostgres(t *testing.T) {
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
		OrganizationID: orgID, OrganizationSlug: "cancel", OrganizationName: "Cancel",
		UserID: ownerID, MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "cancel-agent",
		AgentSlug: "billing", AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	newInstance := func(name string) domain.AgentInstance {
		t.Helper()
		secret := sha256.Sum256([]byte(name))
		instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
			OrganizationID: orgID, AgentID: agent.ID, ActorID: ownerID, Name: name,
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
		return instance
	}
	instance := newInstance("laptop")
	otherInstance := newInstance("pod-7")

	openRequest := func(key string) domain.ApprovalRequest {
		t.Helper()
		request := domain.ApprovalRequest{
			Timeout: "1h",
			ID:      domain.ID(uuid.NewString()), OrganizationID: orgID,
			AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
			Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount":4900}`),
			IdempotencyKey: key, SemanticHash: "hash-" + key,
			Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		}
		created, err := store.InitializeApproval(ctx, pendingApprovalInitialization(request))
		if err != nil {
			t.Fatalf("InitializeApproval: %v", err)
		}
		return created
	}
	cancelAs := func(instanceID, requestID domain.ID, at time.Time) (domain.ApprovalRequest, error) {
		return store.CancelApproval(ctx, application.CancelApprovalInput{
			OrganizationID: orgID, RequestID: requestID, AgentInstanceID: instanceID,
			Auth: domain.AuthContext{Method: "bearer", Assurance: "machine", AuthenticatedAt: at, CredentialID: domain.ID(uuid.NewString())},
			Now:  at,
		})
	}

	t.Run("withdraws a pending request with one terminal decision and an audit record", func(t *testing.T) {
		request := openRequest("cancel-1")
		claim, err := store.ClaimApproval(ctx, application.ClaimApprovalInput{
			OrganizationID: orgID, RequestID: request.ID,
			Principal: domain.Principal{ActorType: domain.ActorHuman, ActorID: ownerID, OrganizationID: orgID, MembershipID: ownerMembershipID},
			Now:       now,
		})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		cancelled, err := cancelAs(instance.ID, request.ID, now.Add(time.Minute))
		if err != nil {
			t.Fatalf("CancelApproval: %v", err)
		}
		if cancelled.Status != domain.ApprovalCancelled || cancelled.Decision == nil {
			t.Fatalf("cancellation not recorded: %#v", cancelled)
		}
		decision := cancelled.Decision
		if decision.Source != domain.DecisionSourceAgent || decision.Status != domain.ApprovalCancelled || decision.Channel != "aap" ||
			decision.Auth.Method != "bearer" || decision.Auth.CredentialID == "" || decision.MembershipID != "" {
			t.Fatalf("cancellation provenance is incomplete: %#v", decision)
		}
		if cancelled.Claim != nil {
			t.Fatalf("the open claim survived the cancellation: %#v", cancelled.Claim)
		}
		var reason string
		if err := store.Pool().QueryRow(ctx, `SELECT release_reason FROM approval_request_claims WHERE id = $1`, claim.ID).Scan(&reason); err != nil || reason != "cancelled" {
			t.Fatalf("claim closure reason = %q, %v", reason, err)
		}
		var eventType, actorType, actorID string
		var data []byte
		if err := store.Pool().QueryRow(ctx, `SELECT event_type, actor_type, actor_id::text, data FROM test_audit_events
			WHERE organization_id=$1 AND subject_id=$2 ORDER BY sequence DESC LIMIT 1`, orgID, request.ID).Scan(&eventType, &actorType, &actorID, &data); err != nil {
			t.Fatal(err)
		}
		if eventType != "approval.cancelled" || actorType != "agent" || actorID != string(agent.ID) {
			t.Fatalf("audit record = %s by %s %s", eventType, actorType, actorID)
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil || payload["agent_instance_id"] != string(instance.ID) || payload["agent_instance_name"] != "laptop" {
			t.Fatalf("audit data = %s (%v)", data, err)
		}
		if count, err := store.CountPendingApprovals(ctx, application.CountPendingApprovalsInput{OrganizationID: orgID}); err != nil || count != 0 {
			t.Fatalf("CountPendingApprovals after cancellation = %d, %v", count, err)
		}
		if cancelled := auditEvents(t, ctx, store, `organization_id = $1 AND event_type = 'approval.cancelled'`, orgID); len(cancelled) != 1 || cancelled[0].SubjectID != request.ID {
			t.Fatalf("cancelled events = %+v", cancelled)
		}

		// A retried cancel from the same instance is idempotent.
		again, err := cancelAs(instance.ID, request.ID, now.Add(2*time.Minute))
		if err != nil || again.Status != domain.ApprovalCancelled || again.Decision.ID != decision.ID {
			t.Fatalf("retried cancel = %#v, %v", again, err)
		}
		var decisions int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM decisions WHERE request_id = $1`, request.ID).Scan(&decisions); err != nil || decisions != 1 {
			t.Fatalf("decision rows = %d, %v, want exactly one", decisions, err)
		}
	})

	t.Run("only the opening instance may cancel", func(t *testing.T) {
		request := openRequest("cancel-2")
		if _, err := cancelAs(otherInstance.ID, request.ID, now); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("other instance cancel error = %v", err)
		}
		if _, err := cancelAs(instance.ID, domain.ID(uuid.NewString()), now); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown request cancel error = %v", err)
		}
		current, err := store.GetApproval(ctx, application.GetApprovalInput{OrganizationID: orgID, RequestID: request.ID})
		if err != nil || current.Status != domain.ApprovalPending {
			t.Fatalf("refused cancel changed the request: %#v %v", current, err)
		}
	})

	t.Run("a decided or expired request stays as it is", func(t *testing.T) {
		request := openRequest("cancel-3")
		if _, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{OrganizationID: orgID, RequestID: request.ID, Now: now.Add(2 * time.Hour)}); err != nil {
			t.Fatalf("expire: %v", err)
		}
		if _, err := cancelAs(instance.ID, request.ID, now.Add(3*time.Hour)); !errors.Is(err, domain.ErrAlreadyTerminal) {
			t.Fatalf("cancel after expiry error = %v", err)
		}
		current, err := store.GetApproval(ctx, application.GetApprovalInput{OrganizationID: orgID, RequestID: request.ID})
		if err != nil || current.Status != domain.ApprovalExpired {
			t.Fatalf("refused cancel changed the expired request: %#v %v", current, err)
		}

		// And the other way round: nothing can follow a cancellation.
		withdrawn := openRequest("cancel-4")
		if _, err := cancelAs(instance.ID, withdrawn.ID, now.Add(time.Minute)); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		expired, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{OrganizationID: orgID, RequestID: withdrawn.ID, Now: now.Add(2 * time.Hour)})
		if err != nil || expired.Status != domain.ApprovalCancelled {
			t.Fatalf("expiry after cancellation = %s, %v", expired.Status, err)
		}
		if _, err := store.Pool().Exec(ctx, `UPDATE approval_requests SET status='approved' WHERE id=$1`, withdrawn.ID); err == nil {
			t.Fatal("terminal-state trigger let a cancelled request change")
		}
	})
}
