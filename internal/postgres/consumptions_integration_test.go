package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// Exercises AAP consume and outcome against a real PostgreSQL with the
// production migrations and triggers: the state checks under the row
// lock, the database clock, key replay, the race between attempts, the
// once-only outcome, and the approval validity that depends on webhook
// delivery.
func TestApprovalConsumptionAgainstRealPostgres(t *testing.T) {
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
		OrganizationID: orgID, OrganizationSlug: "consume", OrganizationName: "Consume",
		UserID: ownerID, MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "consume-agent",
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
	webhookInstance := newInstance("webhook-worker")
	if _, err := store.Pool().Exec(ctx, `INSERT INTO agent_instance_deliveries
		(agent_instance_id, organization_id, type, url, registration_id, sealed_signing_secret, verified_at)
		VALUES ($1, $2, 'webhook', 'https://receiver.example.com/aap', $3, '\x00', $4)`,
		webhookInstance.ID, orgID, uuid.NewString(), now); err != nil {
		t.Fatalf("configure webhook delivery: %v", err)
	}

	request := func(instanceID domain.ID, key string, status domain.ApprovalStatus, decidedAt time.Time) domain.ApprovalRequest {
		t.Helper()
		request := domain.ApprovalRequest{
			Timeout: "1h",
			ID:      domain.ID(uuid.NewString()), OrganizationID: orgID,
			AgentID: agent.ID, AgentInstanceID: instanceID, AgentSlug: agent.AgentSlug,
			Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount":4900}`),
			IdempotencyKey: key, SemanticHash: "hash-" + key,
			Status: status, DeadlineAt: decidedAt.Add(time.Hour), CreatedAt: decidedAt, UpdatedAt: decidedAt,
		}
		initialization := pendingApprovalInitialization(request)
		if status != domain.ApprovalPending {
			initialization = automaticApprovalInitialization(request, false)
		}
		created, err := store.InitializeApproval(ctx, initialization)
		if err != nil {
			t.Fatalf("InitializeApproval: %v", err)
		}
		return created
	}
	approved := func(key string) domain.ApprovalRequest {
		return request(instance.ID, key, domain.ApprovalApproved, time.Now().UTC())
	}
	consume := func(instanceID, requestID domain.ID, key string) (domain.ApprovalRequest, error) {
		return store.ConsumeApproval(ctx, application.ConsumeApprovalInput{
			OrganizationID: orgID, RequestID: requestID, AgentInstanceID: instanceID, IdempotencyKey: key,
		})
	}
	report := func(instanceID, requestID domain.ID, status domain.ConsumptionOutcomeStatus, note string) (domain.ApprovalRequest, error) {
		return store.ReportApprovalOutcome(ctx, application.ReportApprovalOutcomeInput{
			OrganizationID: orgID, RequestID: requestID, AgentInstanceID: instanceID, Status: status, Note: note,
		})
	}
	events := func(requestID domain.ID, eventType string) []domain.AuditEvent {
		return auditEvents(t, ctx, store, `organization_id = $1 AND subject_id = $2 AND event_type = $3`, orgID, requestID, eventType)
	}

	t.Run("approvals stay valid 30 minutes with webhook delivery and 5 without", func(t *testing.T) {
		for name, tc := range map[string]struct {
			instanceID domain.ID
			validity   time.Duration
		}{
			"webhook delivery": {webhookInstance.ID, 30 * time.Minute},
			"no delivery":      {instance.ID, 5 * time.Minute},
		} {
			got := request(tc.instanceID, "validity-"+name, domain.ApprovalApproved, now)
			if got.Decision == nil || got.Decision.ExpiresAt == nil || !got.Decision.ExpiresAt.Equal(got.Decision.DecidedAt.Add(tc.validity)) {
				t.Fatalf("%s: expiry = %+v, want decided_at + %s", name, got.Decision, tc.validity)
			}
		}
		denied := request(webhookInstance.ID, "validity-denied", domain.ApprovalDenied, now)
		if denied.Decision == nil || denied.Decision.ExpiresAt != nil {
			t.Fatalf("a denial carries an expiry: %+v", denied.Decision)
		}
	})

	t.Run("consumes an approved request once and audits it", func(t *testing.T) {
		target := approved("consume-1")
		consumed, err := consume(instance.ID, target.ID, "attempt-1")
		if err != nil {
			t.Fatalf("ConsumeApproval: %v", err)
		}
		if consumed.Consumption == nil || consumed.Consumption.ConsumedAt.IsZero() || consumed.Consumption.Outcome != nil {
			t.Fatalf("consumption = %+v", consumed.Consumption)
		}
		if consumed.Status != domain.ApprovalApproved || consumed.Decision.ID != target.Decision.ID {
			t.Fatalf("consuming changed the request: %+v", consumed)
		}
		recorded := events(target.ID, "approval.consumed")
		if len(recorded) != 1 || recorded[0].ActorType != domain.ActorAgent || recorded[0].ActorID != agent.ID {
			t.Fatalf("approval.consumed events = %+v", recorded)
		}
		var payload map[string]any
		if err := json.Unmarshal(recorded[0].Data, &payload); err != nil || payload["agent_instance_id"] != string(instance.ID) || payload["agent_instance_name"] != "laptop" {
			t.Fatalf("approval.consumed data = %s (%v)", recorded[0].Data, err)
		}

		replay, err := consume(instance.ID, target.ID, "attempt-1")
		if err != nil || replay.Consumption == nil || !replay.Consumption.ConsumedAt.Equal(consumed.Consumption.ConsumedAt) {
			t.Fatalf("same-key replay = %+v, %v", replay.Consumption, err)
		}
		if len(events(target.ID, "approval.consumed")) != 1 {
			t.Fatal("a replay was audited again")
		}
		_, err = consume(instance.ID, target.ID, "attempt-2")
		var already domain.ApprovalAlreadyConsumedError
		if !errors.Is(err, domain.ErrApprovalAlreadyConsumed) || !errors.As(err, &already) || !already.ConsumedAt.Equal(consumed.Consumption.ConsumedAt) {
			t.Fatalf("another key = %v, want already consumed at %s", err, consumed.Consumption.ConsumedAt)
		}
		if !strings.Contains(err.Error(), consumed.Consumption.ConsumedAt.UTC().Format(time.RFC3339)) {
			t.Fatalf("already consumed message does not name the time: %q", err.Error())
		}
		if _, err := consume(otherInstance.ID, target.ID, "attempt-1"); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("another instance's consume = %v, want forbidden", err)
		}
	})

	t.Run("refuses requests that are not approved", func(t *testing.T) {
		pending := request(instance.ID, "consume-pending", domain.ApprovalPending, now)
		denied := request(instance.ID, "consume-denied", domain.ApprovalDenied, now)
		cancelled := request(instance.ID, "consume-cancelled", domain.ApprovalPending, now)
		if _, err := store.CancelApproval(ctx, application.CancelApprovalInput{
			OrganizationID: orgID, RequestID: cancelled.ID, AgentInstanceID: instance.ID,
			Auth: domain.AuthContext{Method: "bearer", Assurance: "machine", AuthenticatedAt: now}, Now: now,
		}); err != nil {
			t.Fatalf("CancelApproval: %v", err)
		}
		expired := request(instance.ID, "consume-expired-status", domain.ApprovalPending, now)
		if _, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{OrganizationID: orgID, RequestID: expired.ID, Now: now}); err != nil {
			t.Fatalf("ExpireApproval: %v", err)
		}
		for name, target := range map[string]domain.ApprovalRequest{"pending": pending, "denied": denied, "cancelled": cancelled, "expired": expired} {
			if _, err := consume(instance.ID, target.ID, "attempt"); !errors.Is(err, domain.ErrApprovalNotApproved) {
				t.Fatalf("%s consume = %v, want not approved", name, err)
			}
		}
		if _, err := consume(instance.ID, domain.ID(uuid.NewString()), "attempt"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown request consume = %v, want not found", err)
		}
		if _, err := store.Pool().Exec(ctx, `INSERT INTO approval_consumptions (request_id, organization_id, idempotency_key, consumed_at)
			VALUES ($1, $2, 'direct', now())`, denied.ID, orgID); err == nil || !strings.Contains(err.Error(), "only an approved request") {
			t.Fatalf("direct consumption of a denied request = %v", err)
		}
	})

	t.Run("refuses an approval past its expiry by the database clock", func(t *testing.T) {
		stale := request(instance.ID, "consume-stale", domain.ApprovalApproved, time.Now().UTC().Add(-6*time.Minute))
		if _, err := consume(instance.ID, stale.ID, "attempt"); !errors.Is(err, domain.ErrApprovalExpired) {
			t.Fatalf("stale consume = %v, want approval expired", err)
		}
		lasting := request(webhookInstance.ID, "consume-webhook", domain.ApprovalApproved, time.Now().UTC().Add(-6*time.Minute))
		if _, err := consume(webhookInstance.ID, lasting.ID, "attempt"); err != nil {
			t.Fatalf("a webhook instance's approval expired after five minutes: %v", err)
		}
	})

	t.Run("replays the consuming key after expiry", func(t *testing.T) {
		closing := request(instance.ID, "consume-closing", domain.ApprovalApproved, time.Now().UTC().Add(-5*time.Minute+1500*time.Millisecond))
		consumed, err := consume(instance.ID, closing.ID, "attempt-1")
		if err != nil {
			t.Fatalf("consume inside the window: %v", err)
		}
		time.Sleep(time.Until(*closing.Decision.ExpiresAt) + 100*time.Millisecond)
		replay, err := consume(instance.ID, closing.ID, "attempt-1")
		if err != nil || !replay.Consumption.ConsumedAt.Equal(consumed.Consumption.ConsumedAt) {
			t.Fatalf("replay after expiry = %+v, %v", replay.Consumption, err)
		}
		if _, err := consume(instance.ID, closing.ID, "attempt-2"); !errors.Is(err, domain.ErrApprovalAlreadyConsumed) {
			t.Fatalf("another key after expiry = %v, want already consumed", err)
		}
	})

	t.Run("only one of concurrent attempts consumes", func(t *testing.T) {
		target := approved("consume-race")
		var wg sync.WaitGroup
		results := make([]error, 8)
		for index := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, results[index] = consume(instance.ID, target.ID, fmt.Sprintf("attempt-%d", index))
			}()
		}
		wg.Wait()
		succeeded := 0
		for _, err := range results {
			switch {
			case err == nil:
				succeeded++
			case !errors.Is(err, domain.ErrApprovalAlreadyConsumed):
				t.Fatalf("a losing attempt = %v, want already consumed", err)
			}
		}
		if succeeded != 1 || len(events(target.ID, "approval.consumed")) != 1 {
			t.Fatalf("%d attempts consumed the approval", succeeded)
		}

		retried := approved("consume-retries")
		for index := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, results[index] = consume(instance.ID, retried.ID, "same-attempt")
			}()
		}
		wg.Wait()
		for _, err := range results {
			if err != nil {
				t.Fatalf("a retry of the same attempt = %v", err)
			}
		}
	})

	t.Run("records the outcome once", func(t *testing.T) {
		target := approved("outcome-1")
		if _, err := report(instance.ID, target.ID, domain.ConsumptionSucceeded, ""); !errors.Is(err, domain.ErrApprovalNotConsumed) {
			t.Fatalf("outcome before consume = %v, want not consumed", err)
		}
		if _, err := consume(instance.ID, target.ID, "attempt-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := report(otherInstance.ID, target.ID, domain.ConsumptionSucceeded, ""); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("another instance's outcome = %v, want forbidden", err)
		}
		reported, err := report(instance.ID, target.ID, domain.ConsumptionFailed, "Card declined")
		if err != nil {
			t.Fatalf("ReportApprovalOutcome: %v", err)
		}
		outcome := reported.Consumption.Outcome
		if outcome == nil || outcome.Status != domain.ConsumptionFailed || outcome.Note != "Card declined" || outcome.ReportedAt.IsZero() {
			t.Fatalf("outcome = %+v", outcome)
		}
		if reported.Status != domain.ApprovalApproved || reported.Decision.Status != domain.ApprovalApproved {
			t.Fatalf("the outcome changed the request: %s / %s", reported.Status, reported.Decision.Status)
		}
		again, err := report(instance.ID, target.ID, domain.ConsumptionFailed, "Card declined")
		if err != nil || !again.Consumption.Outcome.ReportedAt.Equal(outcome.ReportedAt) {
			t.Fatalf("identical outcome again = %+v, %v", again.Consumption, err)
		}
		recorded := events(target.ID, "approval.outcome_reported")
		if len(recorded) != 1 || recorded[0].ActorID != agent.ID {
			t.Fatalf("approval.outcome_reported events = %+v", recorded)
		}
		var payload map[string]any
		if err := json.Unmarshal(recorded[0].Data, &payload); err != nil || payload["status"] != "failed" || payload["note"] != "Card declined" {
			t.Fatalf("approval.outcome_reported data = %s (%v)", recorded[0].Data, err)
		}
		for name, tc := range map[string]struct {
			status domain.ConsumptionOutcomeStatus
			note   string
		}{
			"other status": {domain.ConsumptionSucceeded, "Card declined"},
			"other note":   {domain.ConsumptionFailed, "Timed out"},
			"no note":      {domain.ConsumptionFailed, ""},
		} {
			if _, err := report(instance.ID, target.ID, tc.status, tc.note); !errors.Is(err, domain.ErrOutcomeConflict) {
				t.Fatalf("%s = %v, want outcome conflict", name, err)
			}
		}
		replay, err := consume(instance.ID, target.ID, "attempt-1")
		if err != nil || replay.Consumption.Outcome == nil || replay.Consumption.Outcome.Status != domain.ConsumptionFailed {
			t.Fatalf("consume replay after the outcome = %+v, %v", replay.Consumption, err)
		}
	})

	t.Run("the note limit counts characters, not bytes", func(t *testing.T) {
		target := approved("note-limit-1")
		if _, err := consume(instance.ID, target.ID, "attempt-1"); err != nil {
			t.Fatal(err)
		}
		note := strings.Repeat("é", domain.MaximumConsumptionNoteLength)
		reported, err := report(instance.ID, target.ID, domain.ConsumptionSucceeded, note)
		if err != nil || reported.Consumption.Outcome == nil || reported.Consumption.Outcome.Note != note {
			t.Fatalf("a note of %d two-byte characters = %+v, %v", domain.MaximumConsumptionNoteLength, reported.Consumption, err)
		}
	})

	t.Run("the protective trigger holds against direct SQL", func(t *testing.T) {
		target := approved("protect-1")
		if _, err := consume(instance.ID, target.ID, "attempt-1"); err != nil {
			t.Fatal(err)
		}
		for name, statement := range map[string]string{
			"moved consumption": `UPDATE approval_consumptions SET consumed_at = consumed_at - interval '1 hour' WHERE request_id = $1`,
			"deleted":           `DELETE FROM approval_consumptions WHERE request_id = $1`,
		} {
			if _, err := store.Pool().Exec(ctx, statement, target.ID); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		}
		if _, err := report(instance.ID, target.ID, domain.ConsumptionSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Pool().Exec(ctx, `UPDATE approval_consumptions SET outcome_status = 'failed' WHERE request_id = $1`, target.ID); err == nil {
			t.Fatal("a recorded outcome was rewritten")
		}
	})
}
