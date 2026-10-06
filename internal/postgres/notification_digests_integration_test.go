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

// Exercises notification windows against real PostgreSQL: the first
// prompt sends and opens a window, later ones hold, a close hands back the
// eligible holds newest first, widens the window and audits each request,
// a stale close does nothing, an empty close resets, and an overdue window
// with eligible holds keeps holding until it is closed.
func TestNotificationWindowsAgainstRealPostgres(t *testing.T) {
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
	userID := domain.ID(uuid.NewString())
	membershipID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "digests", OrganizationName: "Digests",
		UserID: userID, MembershipID: membershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
		OrganizationID: orgID, ActorID: membershipID, Name: "digest-agent",
		AgentSlug: "deploys", AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	secret := sha256.Sum256([]byte("digest-secret"))
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
		OrganizationID: orgID, AgentID: agent.ID, ActorID: userID, Name: "digest-instance",
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
	created := 0
	newRequest := func() domain.ApprovalRequest {
		t.Helper()
		created++
		at := now.Add(time.Duration(created) * time.Second)
		key := uuid.NewString()
		request := domain.ApprovalRequest{
			Timeout: "1h", ID: domain.ID(uuid.NewString()), OrganizationID: orgID,
			AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
			Tool: "deploy", Arguments: json.RawMessage(`{}`),
			IdempotencyKey: key, SemanticHash: "hash-" + key,
			Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour),
			CreatedAt: at, UpdatedAt: at,
		}
		if _, err := store.InitializeApproval(ctx, pendingApprovalInitialization(request)); err != nil {
			t.Fatalf("InitializeApproval: %v", err)
		}
		return request
	}
	windows := []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour}
	admit := func(request domain.ApprovalRequest, at time.Time) application.NotificationAdmission {
		t.Helper()
		admission, err := store.AdmitNotification(ctx, application.AdmitNotificationInput{
			OrganizationID: orgID, UserID: userID, Kind: domain.ContactEmail,
			RequestID: request.ID, Windows: windows, Now: at,
		})
		if err != nil {
			t.Fatalf("AdmitNotification: %v", err)
		}
		return admission
	}
	closeAt := func(closesAt, at time.Time) []domain.ApprovalRequest {
		t.Helper()
		closed, err := store.CloseNotificationWindow(ctx, application.CloseNotificationWindowInput{
			OrganizationID: orgID, UserID: userID, MembershipID: membershipID, Kind: domain.ContactEmail,
			ClosesAt: closesAt, Windows: windows, Now: at,
		})
		if err != nil {
			t.Fatalf("CloseNotificationWindow: %v", err)
		}
		return closed.Requests
	}
	digested := func(requestID domain.ID) int {
		t.Helper()
		var count int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM test_audit_events
			WHERE organization_id = $1 AND event_type = 'notification.digested' AND subject_id = $2`, orgID, requestID).Scan(&count); err != nil {
			t.Fatalf("count digested events: %v", err)
		}
		return count
	}

	first, second, third, decided, claimed := newRequest(), newRequest(), newRequest(), newRequest(), newRequest()
	if admission := admit(first, now); admission.Held {
		t.Fatalf("the first prompt in a quiet period must send at once: %+v", admission)
	}
	opened := now.Add(5 * time.Minute)
	for _, request := range []domain.ApprovalRequest{second, third, decided, claimed} {
		if admission := admit(request, now.Add(time.Minute)); !admission.Held || !admission.ClosesAt.Equal(opened) {
			t.Fatalf("a prompt inside the window must hold until it closes: %+v", admission)
		}
	}
	// Another email kind is its own window.
	if admission, err := store.AdmitNotification(ctx, application.AdmitNotificationInput{
		OrganizationID: orgID, UserID: userID, Kind: domain.ContactSlack, RequestID: second.ID,
		Windows: []time.Duration{time.Minute}, Now: now.Add(time.Minute),
	}); err != nil || admission.Held {
		t.Fatalf("a different kind must not share the window: %+v, %v", admission, err)
	}
	if _, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{OrganizationID: orgID, RequestID: decided.ID, Now: now.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("ExpireApproval: %v", err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO approval_request_claims (id, request_id, organization_id, membership_id, claimed_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, uuid.NewString(), claimed.ID, orgID, membershipID, now.Add(2*time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if stale := closeAt(opened.Add(time.Second), opened); len(stale) != 0 {
		t.Fatalf("a close for another instant must do nothing: %d requests", len(stale))
	}
	requests := closeAt(opened, opened)
	if len(requests) != 2 || requests[0].ID != third.ID || requests[1].ID != second.ID {
		t.Fatalf("the close must hand back the pending, unclaimed holds newest first: %+v", requests)
	}
	if digested(second.ID) != 1 || digested(third.ID) != 1 || digested(decided.ID) != 0 || digested(claimed.ID) != 0 {
		t.Fatal("each request a digest names must be audited, and only those")
	}
	var data map[string]any
	if err := store.Pool().QueryRow(ctx, `SELECT data FROM test_audit_events
		WHERE organization_id = $1 AND event_type = 'notification.digested' AND subject_id = $2`, orgID, second.ID).Scan(&data); err != nil {
		t.Fatalf("read digested event: %v", err)
	}
	if data["display_name"] != "Owner" || data["kind"] != "email" || data["membership_id"] != string(membershipID) || data["request_count"] != float64(2) {
		t.Fatalf("digested event data = %v", data)
	}
	if again := closeAt(opened, opened.Add(time.Second)); len(again) != 0 {
		t.Fatalf("a second holder's close must do nothing: %d requests", len(again))
	}

	// The burst continues: the window widened to fifteen minutes.
	fourth := newRequest()
	widened := opened.Add(15 * time.Minute)
	if admission := admit(fourth, opened.Add(time.Minute)); !admission.Held || !admission.ClosesAt.Equal(widened) {
		t.Fatalf("the second window must be the wider one: %+v", admission)
	}
	// Overdue with an eligible hold: still holds, answering the past close.
	fifth := newRequest()
	if admission := admit(fifth, widened.Add(time.Minute)); !admission.Held || !admission.ClosesAt.Equal(widened) {
		t.Fatalf("an overdue window with eligible holds must hold: %+v", admission)
	}
	if requests := closeAt(widened, widened.Add(time.Minute)); len(requests) != 2 {
		t.Fatalf("the late close must name both holds: %+v", requests)
	}
	var level int
	if err := store.Pool().QueryRow(ctx, `SELECT level FROM notification_windows WHERE organization_id = $1 AND user_id = $2 AND kind = 'email'`, orgID, userID).Scan(&level); err != nil || level != 2 {
		t.Fatalf("the window must reach its widest: level %d, %v", level, err)
	}

	// A window that closes with nothing eligible resets the kind.
	sixth := newRequest()
	widest := widened.Add(time.Minute).Add(time.Hour)
	if admission := admit(sixth, widest.Add(-time.Minute)); !admission.Held {
		t.Fatal("the widest window must still hold")
	}
	if _, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{OrganizationID: orgID, RequestID: sixth.ID, Now: widest.Add(-time.Second)}); err != nil {
		t.Fatalf("ExpireApproval: %v", err)
	}
	if requests := closeAt(widest, widest); len(requests) != 0 {
		t.Fatalf("nothing eligible must send nothing: %+v", requests)
	}
	if admission := admit(newRequest(), widest.Add(time.Minute)); admission.Held {
		t.Fatal("after an empty close the next prompt must send at once")
	}

	// An abandoned window (its holds all decided, nobody closed it) resets
	// on the next prompt instead of holding it.
	seventh := newRequest()
	restart := widest.Add(time.Minute).Add(5 * time.Minute)
	if admission := admit(seventh, widest.Add(2*time.Minute)); !admission.Held || !admission.ClosesAt.Equal(restart) {
		t.Fatalf("the reset window must hold for its first width: %+v", admission)
	}
	if _, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{OrganizationID: orgID, RequestID: seventh.ID, Now: widest.Add(3 * time.Minute)}); err != nil {
		t.Fatalf("ExpireApproval: %v", err)
	}
	if admission := admit(newRequest(), restart.Add(time.Minute)); admission.Held {
		t.Fatal("an abandoned overdue window must send the next prompt at once")
	}

	purged, err := store.Pool().Exec(ctx, `DELETE FROM notification_windows WHERE organization_id = $1`, orgID)
	if err != nil || purged.RowsAffected() == 0 {
		t.Fatalf("windows must be deletable for the purge: %v", err)
	}
}
