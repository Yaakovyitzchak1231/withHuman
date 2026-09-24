package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// The directory sync state the product shows comes from the active
// directory rows the reconciliation sweep stamps; the pending row a setup
// link mints never counts as a connection.
func TestDirectorySyncStateAgainstRealPostgres(t *testing.T) {
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
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "acme", OrganizationName: "Acme",
		UserID: domain.ID(uuid.NewString()), MembershipID: domain.ID(uuid.NewString()), DisplayName: "Owner",
		Email: "owner@acme.example", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	lookup := application.GetDirectorySyncStateInput{OrganizationID: orgID, AdapterKey: "polis"}

	state, err := store.GetDirectorySyncState(ctx, lookup)
	if err != nil || state.ReconciliationStatus != "" || state.LastSuccessfulSyncAt != nil {
		t.Fatalf("no rows: state = %#v, err = %v", state, err)
	}

	// The setup-link record is pending, not a directory.
	if _, err := store.UpsertDirectoryConnection(ctx, application.UpsertDirectoryConnectionInput{
		OrganizationID: orgID, AdapterKey: "polis", ProviderResourceID: "tenant:" + string(orgID), State: "pending", Now: now,
	}); err != nil {
		t.Fatalf("UpsertDirectoryConnection pending: %v", err)
	}
	state, err = store.GetDirectorySyncState(ctx, lookup)
	if err != nil || state.ReconciliationStatus != "" {
		t.Fatalf("pending only: state = %#v, err = %v", state, err)
	}

	if _, err := store.UpsertDirectoryConnection(ctx, application.UpsertDirectoryConnectionInput{
		OrganizationID: orgID, AdapterKey: "polis", ProviderResourceID: "dir-1", State: "active", Now: now,
	}); err != nil {
		t.Fatalf("UpsertDirectoryConnection active: %v", err)
	}
	state, err = store.GetDirectorySyncState(ctx, lookup)
	if err != nil || state.ReconciliationStatus != "pending" || state.LastSuccessfulSyncAt != nil {
		t.Fatalf("before any sweep: state = %#v, err = %v", state, err)
	}

	swept := now.Add(time.Minute)
	if err := store.UpdateDirectoryReconciliation(ctx, application.UpdateDirectoryReconciliationInput{
		OrganizationID: orgID, AdapterKey: "polis", Status: "healthy", Now: swept,
	}); err != nil {
		t.Fatalf("UpdateDirectoryReconciliation healthy: %v", err)
	}
	state, err = store.GetDirectorySyncState(ctx, lookup)
	if err != nil || state.ReconciliationStatus != "healthy" || state.LastSuccessfulSyncAt == nil || !state.LastSuccessfulSyncAt.Equal(swept) {
		t.Fatalf("after a healthy sweep: state = %#v, err = %v", state, err)
	}

	// A degraded sweep keeps the last success.
	if err := store.UpdateDirectoryReconciliation(ctx, application.UpdateDirectoryReconciliationInput{
		OrganizationID: orgID, AdapterKey: "polis", Status: "degraded", Now: swept.Add(time.Minute),
	}); err != nil {
		t.Fatalf("UpdateDirectoryReconciliation degraded: %v", err)
	}
	state, err = store.GetDirectorySyncState(ctx, lookup)
	if err != nil || state.ReconciliationStatus != "degraded" || state.LastSuccessfulSyncAt == nil || !state.LastSuccessfulSyncAt.Equal(swept) {
		t.Fatalf("after a degraded sweep: state = %#v, err = %v", state, err)
	}

	// Another organization's directory is invisible.
	other, err := store.GetDirectorySyncState(ctx, application.GetDirectorySyncStateInput{OrganizationID: domain.ID(uuid.NewString()), AdapterKey: "polis"})
	if err != nil || other.ReconciliationStatus != "" {
		t.Fatalf("other organization: state = %#v, err = %v", other, err)
	}
}
