package dbtest_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/db/dbtest"
)

func TestStartAppliesCoreSchema(t *testing.T) {
	url := dbtest.Start(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM organizations").Scan(&count); err != nil {
		t.Fatalf("organizations table is not queryable: %v", err)
	}
}
