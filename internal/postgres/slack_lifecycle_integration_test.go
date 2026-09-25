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
	"github.com/withHumanAI/withHuman/internal/secrets"
)

func installSlackFixture(t *testing.T, store *Store, org domain.ID, now time.Time) domain.ID {
	t.Helper()
	sealer, err := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	store.WithSealer(sealer)
	sealed, err := sealer.Seal(slackTokenLabel, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	var member domain.ID
	if err := store.pool.QueryRow(context.Background(), `SELECT id::text FROM memberships WHERE organization_id=$1 LIMIT 1`, org).Scan(&member); err != nil {
		t.Fatal(err)
	}
	connection, err := store.UpsertSlackConnection(context.Background(), application.UpsertSlackConnectionInput{OrganizationID: org,
		Installation:   application.SlackInstallation{TeamID: "T" + string(org), TeamName: "Fixture", BotUserID: "BOT", Scopes: []string{}},
		SealedBotToken: sealed, InstalledByMembershipID: member, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return connection.InstallationID
}

func TestSlackLifecycleKeepsRulesAndRejectsStaleGenerations(t *testing.T) {
	server := embeddedpgtest.Start(t)
	ctx := context.Background()
	store, err := Open(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatal(err)
	}
	recordAudit(t, store)
	now := time.Now().UTC()
	org, user, member := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	_, err = store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: org, OrganizationSlug: "lifecycle", OrganizationName: "Lifecycle", UserID: user, MembershipID: member, DisplayName: "Owner", Email: "lifecycle@example.com", PasswordHash: "test", AdmissionSource: domain.AdmissionManual, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	generation := installSlackFixture(t, store, org, now)
	link, err := store.LinkOrganizationContactMethod(ctx, application.LinkOrganizationContactMethodInput{MethodID: domain.ID(uuid.NewString()), UserID: user, OrganizationID: org, Kind: domain.ContactSlack, ExternalID: "U1", SlackInstallationID: generation, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !link.Available() {
		t.Fatal("fresh match unavailable")
	}
	_, err = store.ReplaceNotificationRules(ctx, application.ReplaceNotificationRulesInput{UserID: user, Urgency: domain.NotificationInterrupt, Rungs: []application.NotificationRung{{ContactMethodID: link.ID}}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	version := application.SlackConnectionVersion{OrganizationID: org, InstallationID: generation}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService, OrganizationID: org, ActorID: member, Name: "fixture", AgentSlug: "fixture", AgentQuota: application.UnlimitedQuota(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	secret := sha256.Sum256([]byte("fixture-secret"))
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{
		ActorType: domain.ActorHuman, OrganizationID: org, AgentID: agent.ID, ActorID: user, Name: "fixture",
		Metadata:   json.RawMessage(`{}`),
		Credential: application.AgentCredentialRecord{ID: domain.ID(uuid.NewString()), SecretHash: secret[:], IssuedAt: now, ExpiresAt: now.Add(time.Hour)}, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := domain.ApprovalRequest{ID: domain.ID(uuid.NewString()), OrganizationID: org, AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
		Tool: "deploy", Arguments: json.RawMessage(`{}`), IdempotencyKey: "slack-queue", SemanticHash: "slack-queue", Timeout: "1h",
		Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	if _, err := store.InitializeApproval(ctx, pendingApprovalInitialization(request)); err != nil {
		t.Fatal(err)
	}
	changed, err := store.DisconnectSlackConnection(ctx, application.DisconnectSlackConnectionInput{SlackConnectionVersion: version, Reason: "app_uninstalled", Now: now})
	if err != nil || !changed {
		t.Fatalf("disconnect=%v,%v", changed, err)
	}
	notices, err := store.ListSlackDisconnections(ctx, application.ListPendingSlackEventsInput{Limit: 10})
	if err != nil || len(notices) != 1 || notices[0].RequestID != request.ID || notices[0].InstallationID != generation {
		t.Fatalf("notices=%+v,%v", notices, err)
	}
	if err := store.CompleteSlackDisconnection(ctx, application.CompleteSlackEventInput{ID: notices[0].ID, Now: now}); err != nil {
		t.Fatal(err)
	}
	notices, err = store.ListSlackDisconnections(ctx, application.ListPendingSlackEventsInput{Limit: 10})
	if err != nil || len(notices) != 0 {
		t.Fatalf("completed notices=%+v,%v", notices, err)
	}
	for i := 0; i < 2; i++ {
		claimed, err := store.ClaimSlackEmailFallback(ctx, application.ClaimSlackEmailFallbackInput{OrganizationID: org, RequestID: request.ID, UserID: user, Now: now})
		if err != nil || claimed != (i == 0) {
			t.Fatalf("fallback %d=%v,%v", i, claimed, err)
		}
	}
	if _, err := store.OpenSlackBotToken(ctx, application.GetSlackConnectionInput{OrganizationID: org}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("token remains: %v", err)
	}
	methods, err := store.ListContactMethods(ctx, application.ListContactMethodsInput{UserID: user, OrganizationID: org})
	if err != nil || len(methods) != 1 || methods[0].Available() {
		t.Fatalf("methods=%+v,%v", methods, err)
	}
	rules, err := store.ListNotificationRules(ctx, application.ListNotificationRulesInput{UserID: user})
	if err != nil || len(rules) != 1 || rules[0].ContactMethodID != link.ID {
		t.Fatalf("rules lost: %+v,%v", rules, err)
	}
	newer := installSlackFixture(t, store, org, now.Add(time.Second))
	changed, err = store.DisconnectSlackConnection(ctx, application.DisconnectSlackConnectionInput{SlackConnectionVersion: version, Reason: "tokens_revoked", Now: now})
	if err != nil || changed {
		t.Fatalf("stale disconnect=%v,%v", changed, err)
	}
	methods, _ = store.ListContactMethods(ctx, application.ListContactMethodsInput{UserID: user, OrganizationID: org})
	if methods[0].Available() {
		t.Fatal("old match reactivated")
	}
	if _, err := store.GetSlackDelivery(ctx, application.GetSlackDeliveryInput{OrganizationID: org, UserID: user, InstallationID: generation, SlackUserID: "U1"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stale delivery=%v", err)
	}
	relink, err := store.LinkOrganizationContactMethod(ctx, application.LinkOrganizationContactMethodInput{MethodID: domain.ID(uuid.NewString()), UserID: user, OrganizationID: org, Kind: domain.ContactSlack, ExternalID: "U2", SlackInstallationID: newer, Now: now})
	if err != nil || relink.ID != link.ID || !relink.Available() {
		t.Fatalf("relink=%+v,%v", relink, err)
	}
	if _, err := store.GetSlackDelivery(ctx, application.GetSlackDeliveryInput{OrganizationID: org, UserID: user, InstallationID: newer, SlackUserID: "U2"}); err != nil {
		t.Fatal(err)
	}
	event := application.SlackEvent{AppID: "A", ExternalEventID: "E", TeamID: "T", Type: "app_uninstalled", OccurredAt: now, ReceivedAt: now}
	for i := 0; i < 2; i++ {
		if err := store.InsertSlackEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := store.ListPendingSlackEvents(ctx, application.ListPendingSlackEventsInput{Limit: 10})
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v,%v", pending, err)
	}
	if err := store.CompleteSlackEvent(ctx, application.CompleteSlackEventInput{ID: pending[0].ID, Now: now}); err != nil {
		t.Fatal(err)
	}
	pending, _ = store.ListPendingSlackEvents(ctx, application.ListPendingSlackEventsInput{Limit: 10})
	if len(pending) != 0 {
		t.Fatal("applied event pending")
	}
}
