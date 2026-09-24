package postgres

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// Every table that scopes rows by organization must be deleted by the
// purge, or a tenant deletion fails on a foreign key and retries forever.
// The routing tables were missed this way once; this pins the invariant
// against the live schema so a new table cannot repeat it.
func TestPurgeCoversEveryOrganizationScopedTable(t *testing.T) {
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
	rows, err := store.Pool().Query(ctx, `SELECT table_name FROM information_schema.columns
        WHERE table_schema = 'public' AND column_name = 'organization_id'
        AND table_name IN (SELECT table_name FROM information_schema.tables
            WHERE table_schema = 'public' AND table_type = 'BASE TABLE')
        ORDER BY table_name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	purged := make(map[string]bool)
	pattern := regexp.MustCompile(`DELETE FROM (\w+)`)
	for _, statement := range purgeStatements {
		if match := pattern.FindStringSubmatch(statement); match != nil {
			purged[match[1]] = true
		}
	}
	var missing []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !purged[table] {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("tables with organization_id that the purge never deletes: %s", strings.Join(missing, ", "))
	}
}
