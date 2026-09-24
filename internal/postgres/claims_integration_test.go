package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

func claimTargetPathDocument(membershipID domain.ID) json.RawMessage {
	return json.RawMessage(`{"working_hours":[],"nodes":[{"id":"` + uuid.NewString() +
		`","type":"level","targets":[{"type":"user","id":"` + string(membershipID) + `"}],"escalate_after":"15m"}]}`)
}

// Exercises the claim compare-and-set, release, lapse, closure on decision
// and expiry, and the read model against a real PostgreSQL with the
// production migrations and triggers.
func TestClaimsAgainstRealPostgres(t *testing.T) {
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
		OrganizationID: orgID, OrganizationSlug: "claims", OrganizationName: "Claims",
		UserID: ownerID, MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	claimantMembershipID := admitTeamTestMember(t, ctx, store, orgID, ownerMembershipID, "claimant@example.com", "claimant-secret", now)
	reviewerMembershipID := admitTeamTestMember(t, ctx, store, orgID, ownerMembershipID, "reviewer@example.com", "reviewer-secret", now)

	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "claims-agent",
		AgentSlug: "billing", AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	secret := sha256.Sum256([]byte("claims-secret"))
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
		OrganizationID: orgID, AgentID: agent.ID, ActorID: ownerID, Name: "claims-instance",
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

	pinRequest := func(key string, deadline time.Time, routed *domain.ApprovalRequestRouting) domain.ApprovalRequest {
		t.Helper()
		request := domain.ApprovalRequest{
			Timeout: "1h",
			ID:      domain.ID(uuid.NewString()), OrganizationID: orgID,
			AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
			Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount":1}`),
			IdempotencyKey: key, SemanticHash: "hash-" + key,
			Status: domain.ApprovalPending, DeadlineAt: deadline,
			CreatedAt: now, UpdatedAt: now,
		}
		initialization := pendingApprovalInitialization(request)
		if routed != nil {
			routed.RequestID, routed.OrganizationID, routed.CreatedAt = request.ID, orgID, now
			initialization.Routing = routed
		}
		if _, err := store.InitializeApproval(ctx, initialization); err != nil {
			t.Fatalf("InitializeApproval %s: %v", key, err)
		}
		return request
	}
	claimAs := func(membershipID, requestID domain.ID, at time.Time) (domain.Claim, error) {
		t.Helper()
		var userID string
		if err := store.Pool().QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE id = $1`, membershipID).Scan(&userID); err != nil {
			t.Fatalf("resolve claimant user: %v", err)
		}
		return store.ClaimApproval(ctx, application.ClaimApprovalInput{
			OrganizationID: orgID, RequestID: requestID,
			Principal: domain.Principal{
				ActorType: domain.ActorHuman, ActorID: domain.ID(userID),
				OrganizationID: orgID, MembershipID: membershipID,
			},
			Now: at,
		})
	}
	countEvents := func(eventType string, subjectID domain.ID) int {
		t.Helper()
		var count int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM test_audit_events
			WHERE organization_id = $1 AND event_type = $2 AND subject_id = $3`,
			orgID, eventType, subjectID).Scan(&count); err != nil {
			t.Fatalf("count %s events: %v", eventType, err)
		}
		return count
	}
	claimRow := func(claimID domain.ID) (releasedAt *time.Time, reason string) {
		t.Helper()
		var released *time.Time
		var releaseReason *string
		if err := store.Pool().QueryRow(ctx, `SELECT released_at, release_reason
			FROM approval_request_claims WHERE id = $1`, claimID).Scan(&released, &releaseReason); err != nil {
			t.Fatalf("read claim row: %v", err)
		}
		if releaseReason != nil {
			reason = *releaseReason
		}
		return released, reason
	}

	t.Run("policy carries the default retrigger window", func(t *testing.T) {
		policy, err := store.GetOrganizationPolicy(ctx, application.GetOrganizationPolicyInput{OrganizationID: orgID})
		if err != nil || policy.ClaimRetriggerWindow != 15*time.Minute {
			t.Fatalf("policy = %+v, %v", policy, err)
		}
	})

	t.Run("claim lifecycle on an unrouted request", func(t *testing.T) {
		request := pinRequest("claims-req-1", now.Add(time.Hour), nil)
		claim, err := claimAs(claimantMembershipID, request.ID, now)
		if err != nil {
			t.Fatalf("ClaimApproval: %v", err)
		}
		if claim.MembershipID != claimantMembershipID || claim.ClaimantDisplayName != "claimant@example.com" {
			t.Fatalf("claim = %+v", claim)
		}
		if !claim.ExpiresAt.Equal(now.Add(15 * time.Minute)) {
			t.Fatalf("expiry not the retrigger default: %s", claim.ExpiresAt)
		}
		if got := countEvents("routing.claimed", request.ID); got != 1 {
			t.Fatalf("routing.claimed events = %d", got)
		}

		// Same claimant re-claims idempotently: same claim, no new event.
		again, err := claimAs(claimantMembershipID, request.ID, now.Add(time.Minute))
		if err != nil || again.ID != claim.ID || !again.ExpiresAt.Equal(claim.ExpiresAt) {
			t.Fatalf("re-claim = %+v, %v", again, err)
		}
		if got := countEvents("routing.claimed", request.ID); got != 1 {
			t.Fatalf("idempotent re-claim wrote an event: %d", got)
		}

		// Another reviewer conflicts; there is no takeover.
		if _, err := claimAs(reviewerMembershipID, request.ID, now.Add(time.Minute)); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("second claimant error = %v", err)
		}

		// Only the claimant releases.
		if _, err := store.ReleaseClaim(ctx, application.ReleaseClaimInput{
			OrganizationID: orgID, RequestID: request.ID,
			MembershipID: reviewerMembershipID, ActorID: domain.ID(uuid.NewString()), Now: now.Add(time.Minute),
		}); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("non-claimant release error = %v", err)
		}
		if _, err := store.ReleaseClaim(ctx, application.ReleaseClaimInput{
			OrganizationID: orgID, RequestID: request.ID,
			MembershipID: claimantMembershipID, ActorID: ownerID, Now: now.Add(2 * time.Minute),
		}); err != nil {
			t.Fatalf("release: %v", err)
		}
		if _, reason := claimRow(claim.ID); reason != "released" {
			t.Fatalf("release reason = %q", reason)
		}
		if got := countEvents("routing.claim_released", request.ID); got != 1 {
			t.Fatalf("routing.claim_released events = %d", got)
		}
		if _, err := store.ReleaseClaim(ctx, application.ReleaseClaimInput{
			OrganizationID: orgID, RequestID: request.ID,
			MembershipID: claimantMembershipID, ActorID: ownerID, Now: now.Add(2 * time.Minute),
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("release without live claim error = %v", err)
		}

		// After a release the request is claimable again, and a decision
		// closes the new claim silently.
		reclaim, err := claimAs(reviewerMembershipID, request.ID, now.Add(3*time.Minute))
		if err != nil {
			t.Fatalf("claim after release: %v", err)
		}
		read, err := store.GetApproval(ctx, application.GetApprovalInput{OrganizationID: orgID, RequestID: request.ID})
		if err != nil || read.Claim == nil || read.Claim.ID != reclaim.ID || read.Claim.ClaimantDisplayName != "reviewer@example.com" {
			t.Fatalf("read model claim = %+v, %v", read.Claim, err)
		}
		if applied := teamTestDecision(t, ctx, store, orgID, request.ID, claimantMembershipID, "claims-decision-1", now.Add(4*time.Minute)); applied.Status != domain.OperationApplied {
			t.Fatalf("decision on claimed request = %+v", applied)
		}
		if _, reason := claimRow(reclaim.ID); reason != "decided" {
			t.Fatalf("decision closure reason = %q", reason)
		}
		if got := countEvents("routing.claim_released", request.ID) + countEvents("routing.claim_lapsed", request.ID); got != 1 {
			t.Fatalf("decision closure emitted extra claim events: %d", got)
		}
		read, err = store.GetApproval(ctx, application.GetApprovalInput{OrganizationID: orgID, RequestID: request.ID})
		if err != nil || read.Claim != nil {
			t.Fatalf("closed claim still on read model: %+v, %v", read.Claim, err)
		}

		// Terminal requests reject claims.
		if _, err := claimAs(reviewerMembershipID, request.ID, now.Add(5*time.Minute)); !errors.Is(err, domain.ErrAlreadyTerminal) {
			t.Fatalf("terminal claim error = %v", err)
		}
	})

	t.Run("routed requests restrict claimants to targets", func(t *testing.T) {
		revision, err := store.CreateEscalationPath(ctx, application.CreateEscalationPathInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PathKey: "claims-payments",
			Name: "Claims payments path", Document: claimTargetPathDocument(claimantMembershipID), DocumentVersion: 2,
			CreatedByActorID: ownerMembershipID, Now: now,
		})
		if err != nil {
			t.Fatalf("create path revision: %v", err)
		}
		request := pinRequest("claims-req-2", now.Add(time.Hour), &domain.ApprovalRequestRouting{
			Outcome: domain.RoutingEscalated, Reason: domain.RoutingReasonBlockEscalation,
			EscalationPathKey: "claims-payments", EscalationPathRevisionID: revision.ID,
			Urgency: "interrupt",
		})
		if _, err := claimAs(reviewerMembershipID, request.ID, now); !errors.Is(err, domain.ErrNotTargeted) {
			t.Fatalf("untargeted claim error = %v", err)
		}
		if _, err := claimAs(claimantMembershipID, request.ID, now); err != nil {
			t.Fatalf("targeted claim: %v", err)
		}
	})

	t.Run("claim expiry is capped at the request deadline", func(t *testing.T) {
		request := pinRequest("claims-req-3", now.Add(5*time.Minute), nil)
		claim, err := claimAs(claimantMembershipID, request.ID, now)
		if err != nil || !claim.ExpiresAt.Equal(request.DeadlineAt) {
			t.Fatalf("capped claim = %+v, %v", claim, err)
		}
		if _, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{
			OrganizationID: orgID, RequestID: request.ID, Now: now.Add(6 * time.Minute),
		}); err != nil {
			t.Fatalf("expire: %v", err)
		}
		if _, reason := claimRow(claim.ID); reason != "expired" {
			t.Fatalf("expiry closure reason = %q", reason)
		}
	})

	t.Run("stale claims are swept with a lapse event", func(t *testing.T) {
		request := pinRequest("claims-req-4", now.Add(time.Hour), nil)
		stale, err := claimAs(claimantMembershipID, request.ID, now)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// The next claim, after the expiry passed, sweeps the stale row and
		// backfills the lapse audit even though no workflow timer fired.
		fresh, err := claimAs(reviewerMembershipID, request.ID, stale.ExpiresAt.Add(time.Second))
		if err != nil {
			t.Fatalf("claim over stale: %v", err)
		}
		if fresh.ID == stale.ID || fresh.MembershipID != reviewerMembershipID {
			t.Fatalf("sweep did not produce a fresh claim: %+v", fresh)
		}
		if _, reason := claimRow(stale.ID); reason != "lapsed" {
			t.Fatalf("stale claim reason = %q", reason)
		}
		if got := countEvents("routing.claim_lapsed", request.ID); got != 1 {
			t.Fatalf("routing.claim_lapsed events = %d", got)
		}
	})

	t.Run("lapse is a compare-and-set by claim id", func(t *testing.T) {
		request := pinRequest("claims-req-5", now.Add(time.Hour), nil)
		first, err := claimAs(claimantMembershipID, request.ID, now)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := store.LapseClaim(ctx, application.LapseClaimInput{
			OrganizationID: orgID, RequestID: request.ID, ClaimID: first.ID, Now: now.Add(15 * time.Minute),
		}); err != nil {
			t.Fatalf("lapse: %v", err)
		}
		if _, reason := claimRow(first.ID); reason != "lapsed" {
			t.Fatalf("lapse reason = %q", reason)
		}
		// A duplicate timer firing and a stale timer against a newer claim
		// both no-op.
		if err := store.LapseClaim(ctx, application.LapseClaimInput{
			OrganizationID: orgID, RequestID: request.ID, ClaimID: first.ID, Now: now.Add(16 * time.Minute),
		}); err != nil {
			t.Fatalf("duplicate lapse: %v", err)
		}
		second, err := claimAs(reviewerMembershipID, request.ID, now.Add(16*time.Minute))
		if err != nil {
			t.Fatalf("re-claim: %v", err)
		}
		if err := store.LapseClaim(ctx, application.LapseClaimInput{
			OrganizationID: orgID, RequestID: request.ID, ClaimID: first.ID, Now: now.Add(17 * time.Minute),
		}); err != nil {
			t.Fatalf("stale lapse: %v", err)
		}
		if released, _ := claimRow(second.ID); released != nil {
			t.Fatalf("stale lapse touched the newer claim: %v", released)
		}
		if got := countEvents("routing.claim_lapsed", request.ID); got != 1 {
			t.Fatalf("routing.claim_lapsed events = %d", got)
		}
	})

	t.Run("claiming a missing request is not found", func(t *testing.T) {
		if _, err := claimAs(claimantMembershipID, domain.ID(uuid.NewString()), now); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("missing request error = %v", err)
		}
	})

	// Concurrent claimants race the partial unique index. Exactly one wins
	// and everyone else must see a mapped conflict, never a driver error
	// leaking out as a 500.
	t.Run("concurrent claimants resolve to one winner and conflicts", func(t *testing.T) {
		request := pinRequest("claims-req-6", now.Add(time.Hour), nil)
		claimants := []domain.ID{claimantMembershipID, reviewerMembershipID, ownerMembershipID}
		var waitGroup sync.WaitGroup
		results := make([]error, len(claimants))
		claims := make([]domain.Claim, len(claimants))
		for index, membershipID := range claimants {
			waitGroup.Add(1)
			go func(index int, membershipID domain.ID) {
				defer waitGroup.Done()
				claims[index], results[index] = claimAs(membershipID, request.ID, now)
			}(index, membershipID)
		}
		waitGroup.Wait()

		winners, conflicts := 0, 0
		for index, err := range results {
			switch {
			case err == nil:
				winners++
				if claims[index].MembershipID != claimants[index] {
					t.Fatalf("winner %d holds another reviewer's claim: %+v", index, claims[index])
				}
			case errors.Is(err, domain.ErrConflict):
				conflicts++
			default:
				t.Fatalf("claimant %d got an unmapped error: %v", index, err)
			}
		}
		if winners != 1 || conflicts != len(claimants)-1 {
			t.Fatalf("winners=%d conflicts=%d, want 1 and %d", winners, conflicts, len(claimants)-1)
		}
		if got := countEvents("routing.claimed", request.ID); got != 1 {
			t.Fatalf("routing.claimed events = %d, want exactly one", got)
		}
	})

	// A claim that outlived its expiry without the workflow timer reaching
	// it closes as lapsed, so the trail never shows a reviewer holding the
	// request right up to a decision made by somebody else.
	t.Run("a decision lapses a stale claim rather than crediting it", func(t *testing.T) {
		request := pinRequest("claims-req-7", now.Add(time.Hour), nil)
		stale, err := claimAs(claimantMembershipID, request.ID, now)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if applied := teamTestDecision(t, ctx, store, orgID, request.ID, reviewerMembershipID,
			"claims-decision-2", stale.ExpiresAt.Add(time.Minute)); applied.Status != domain.OperationApplied {
			t.Fatalf("decision = %+v", applied)
		}
		if _, reason := claimRow(stale.ID); reason != "lapsed" {
			t.Fatalf("stale claim closed as %q, want lapsed", reason)
		}
		if got := countEvents("routing.claim_lapsed", request.ID); got != 1 {
			t.Fatalf("routing.claim_lapsed events = %d", got)
		}
	})

	// Claims must not outlive their organization: the table's composite
	// foreign keys make a missing purge statement fail the whole purge
	// transaction, stranding the tenant forever. Runs last because it
	// destroys the fixture organization.
	t.Run("purge removes claims with the organization", func(t *testing.T) {
		request := pinRequest("claims-req-8", now.Add(time.Hour), nil)
		if _, err := claimAs(claimantMembershipID, request.ID, now); err != nil {
			t.Fatalf("claim: %v", err)
		}
		purgeAt := now.Add(time.Hour)
		if _, err := store.SetOrganizationStatus(ctx, application.SetOrganizationStatusInput{
			OrganizationID: orgID, Status: domain.OrganizationPendingDeletion,
			Reason: "offboarding", DeletionScheduledFor: &purgeAt,
			ActorType: "human", ActorID: ownerID, Now: now,
		}); err != nil {
			t.Fatalf("schedule deletion: %v", err)
		}
		purged, err := store.PurgeOrganization(ctx, application.PurgeOrganizationInput{
			OrganizationID: orgID, Now: purgeAt.Add(time.Minute),
		})
		if err != nil || !purged {
			t.Fatalf("purge = %v, %v", purged, err)
		}
		var remaining int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM approval_request_claims
			WHERE organization_id = $1`, orgID).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("claims survived the purge: count=%d err=%v", remaining, err)
		}
	})
}
