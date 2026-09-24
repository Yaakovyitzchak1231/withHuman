// Package dbtest boots a disposable, fully migrated PostgreSQL for
// integration tests, including tests in other modules that build on core's
// schema. It is test-only machinery: nothing links it into a binary, which
// is why it may reach the embedded PostgreSQL package that the split
// binaries must never depend on.
package dbtest

import (
	"context"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
	"github.com/withHumanAI/withHuman/internal/postgres"
)

// Start boots an embedded PostgreSQL, applies every core migration,
// reconciles the built-in roles from the registry the way a server boot
// does, and returns the connection URL. The instance stops when the test
// finishes. Tests are skipped in short mode.
func Start(t *testing.T) string {
	t.Helper()
	server := embeddedpgtest.Start(t)
	url := server.URL("postgres")
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatalf("connect to embedded PostgreSQL: %v", err)
	}
	defer store.Close()
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatalf("apply core migrations: %v", err)
	}
	if _, err := store.ReconcileBuiltInRoles(ctx, application.ReconcileBuiltInRolesInput{
		Roles: domain.BuiltInRoles(), Permissions: domain.PermissionKeys(), Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("reconcile built-in roles: %v", err)
	}
	return url
}
