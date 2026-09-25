package db_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

func TestApplyRefusesDatabaseMigratedByAnotherChain(t *testing.T) {
	server := embeddedpgtest.Start(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// The chain before the squash: its first file shares a name with the
	// squashed chain's only file, and its schema stops short of it.
	before := fstest.MapFS{
		"0001_initial.sql":      {Data: []byte(`CREATE TABLE early (id int)`)},
		"0002_second_table.sql": {Data: []byte(`CREATE TABLE second (id int)`)},
	}
	if err := db.Apply(ctx, pool, before); err != nil {
		t.Fatalf("apply old chain: %v", err)
	}

	after := fstest.MapFS{
		"0001_initial.sql":        {Data: []byte(`CREATE TABLE squashed (id int)`)},
		"0002_drop_something.sql": {Data: []byte(`CREATE TABLE later (id int)`)},
	}
	err = db.Apply(ctx, pool, after)
	var foreign *db.ForeignChainError
	if !errors.As(err, &foreign) {
		t.Fatalf("expected *db.ForeignChainError, got %v", err)
	}
	if foreign.Table != "schema_migrations" || len(foreign.Names) != 1 || foreign.Names[0] != "0002_second_table.sql" {
		t.Fatalf("unexpected refusal %+v", foreign)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename IN ('squashed', 'later')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("refusal must run nothing, found %d new tables", count)
	}

	// Re-applying the chain that made the record is still a no-op.
	if err := db.Apply(ctx, pool, before); err != nil {
		t.Fatalf("re-apply own chain: %v", err)
	}
}
