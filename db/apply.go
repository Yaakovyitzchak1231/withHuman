package db

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Beginner starts transactions; a pgxpool.Pool satisfies it.
type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// ApplyOption adjusts how Apply records what it has run.
type ApplyOption func(*applyOptions)

type applyOptions struct {
	table string
}

// WithTable records applied file names in the named table instead of
// schema_migrations. An edition that owns a migration chain of its own
// keeps its record apart from core's this way.
func WithTable(name string) ApplyOption {
	return func(options *applyOptions) { options.table = name }
}

var tableNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Apply runs every .sql file in fsys once, by name, in lexical order, one
// transaction per file, and records each applied name. A recorded file is
// skipped, which is why a file that has shipped is frozen: an in-place
// edit would silently never reach a database that already ran it, so
// schema changes always go in a new numbered file. Concurrent starters
// serialize on the record table.
func Apply(ctx context.Context, conn Beginner, fsys fs.FS, options ...ApplyOption) error {
	settings := applyOptions{table: "schema_migrations"}
	for _, option := range options {
		option(&settings)
	}
	if !tableNamePattern.MatchString(settings.table) {
		return fmt.Errorf("migration record table %q is not a plain identifier", settings.table)
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if err := ensureRecordTable(ctx, conn, settings.table); err != nil {
		return err
	}
	for _, name := range names {
		if err := applyOne(ctx, conn, fsys, name, settings.table); err != nil {
			return err
		}
	}
	return nil
}

func ensureRecordTable(ctx context.Context, conn Beginner, table string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
        name text PRIMARY KEY,
        applied_at timestamptz NOT NULL DEFAULT now()
    )`, table)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func applyOne(ctx context.Context, conn Beginner, fsys fs.FS, name, table string) error {
	contents, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, fmt.Sprintf(`LOCK TABLE %s IN EXCLUSIVE MODE`, table)); err != nil {
		return err
	}
	var applied bool
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE name = $1)`, table), name).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, string(contents)); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (name) VALUES ($1)`, table), name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
