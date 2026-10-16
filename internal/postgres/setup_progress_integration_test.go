package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// The setup checklist's agent and test items follow what the
// organization has: a live instance of an active agent, and a request a
// person decided. An agent withdrawing its own request is not a test.
func TestSetupProgressAgainstRealPostgres(t *testing.T) {
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
	membershipID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "progress", OrganizationName: "Progress",
		UserID: ownerID, MembershipID: membershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	progress := func(at time.Time) domain.SetupChecklist {
		t.Helper()
		checklist, err := store.GetSetupProgress(ctx, application.GetSetupProgressInput{OrganizationID: orgID, Now: at})
		if err != nil {
			t.Fatalf("GetSetupProgress: %v", err)
		}
		return checklist
	}
	if got := progress(now); got != (domain.SetupChecklist{}) {
		t.Fatalf("a new organization has set nothing up: %+v", got)
	}

	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
		OrganizationID: orgID, ActorID: membershipID, Name: "Billing",
		AgentSlug: "billing", AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if progress(now).AgentConnected {
		t.Fatal("an agent without an instance is not connected")
	}
	secret := sha256.Sum256([]byte("laptop"))
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
		OrganizationID: orgID, AgentID: agent.ID, ActorID: ownerID, Name: "laptop",
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
	if !progress(now).AgentConnected {
		t.Fatal("an instance holding a live credential is connected")
	}
	if progress(now.Add(25 * time.Hour)).AgentConnected {
		t.Fatal("an expired credential does not connect the agent")
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE agents SET status = 'disabled' WHERE id = $1`, agent.ID); err != nil {
		t.Fatal(err)
	}
	if progress(now).AgentConnected {
		t.Fatal("a disabled agent is not connected")
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE agents SET status = 'active' WHERE id = $1`, agent.ID); err != nil {
		t.Fatal(err)
	}

	openRequest := func(key string) domain.ApprovalRequest {
		t.Helper()
		created, err := store.InitializeApproval(ctx, pendingApprovalInitialization(domain.ApprovalRequest{
			Timeout: "1h",
			ID:      domain.ID(uuid.NewString()), OrganizationID: orgID,
			AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
			Tool: "shell", Arguments: json.RawMessage(`{"command":"ls"}`),
			IdempotencyKey: key, SemanticHash: "hash-" + key,
			Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		}))
		if err != nil {
			t.Fatalf("InitializeApproval: %v", err)
		}
		return created
	}
	withdrawn := openRequest("withdrawn")
	if _, err := store.CancelApproval(ctx, application.CancelApprovalInput{
		OrganizationID: orgID, RequestID: withdrawn.ID, AgentInstanceID: instance.ID,
		Auth: domain.AuthContext{Method: "bearer", Assurance: "machine", AuthenticatedAt: now, CredentialID: domain.ID(uuid.NewString())},
		Now:  now,
	}); err != nil {
		t.Fatalf("CancelApproval: %v", err)
	}
	if progress(now).Tested {
		t.Fatal("an agent withdrawing its request is not a test")
	}
	decided := openRequest("decided")
	if _, err := store.Pool().Exec(ctx, `INSERT INTO decisions (id, request_id, organization_id, membership_id, source, status, channel, idempotency_key, decided_at)
		VALUES ($1, $2, $3, $4, 'human', 'denied', 'web', 'decide-1', $5)`,
		uuid.NewString(), decided.ID, orgID, membershipID, now); err != nil {
		t.Fatalf("insert decision: %v", err)
	}
	if got := progress(now); !got.AgentConnected || !got.Tested || got.RulesWritten {
		t.Fatalf("a person decided a request; the store leaves the rules to the service: %+v", got)
	}
}
