package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// The migration seeds the one installation row; the claim lets one send
// through per interval and again on an upgrade, and the answer is kept.
func TestInstallationHeartbeatAgainstRealPostgres(t *testing.T) {
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

	installation, err := store.GetInstallation(ctx)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if installation.ID == "" || installation.LatestVersion != "" || !installation.CheckedAt.IsZero() || len(installation.Advisories) != 0 {
		t.Fatalf("fresh installation = %+v, want an ID and nothing checked", installation)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO installations DEFAULT VALUES`); err == nil {
		t.Fatal("a second installation row was accepted")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	claim := func(at time.Time, version string) bool {
		t.Helper()
		id, claimed, err := store.ClaimHeartbeat(ctx, application.ClaimHeartbeatInput{Now: at, Version: version})
		if err != nil {
			t.Fatalf("ClaimHeartbeat: %v", err)
		}
		if claimed && id != installation.ID {
			t.Fatalf("claimed ID = %q, want %q", id, installation.ID)
		}
		return claimed
	}
	if !claim(now, "1.0.0") {
		t.Fatal("first claim refused")
	}
	if claim(now.Add(time.Minute), "1.0.0") {
		t.Fatal("second claim within the interval accepted")
	}
	if !claim(now.Add(2*time.Minute), "1.0.1") {
		t.Fatal("claim after an upgrade refused")
	}
	if claim(now.Add(application.HeartbeatInterval), "1.0.1") {
		t.Fatal("claim before the interval since the upgrade accepted")
	}
	if !claim(now.Add(2*time.Minute+application.HeartbeatInterval), "1.0.1") {
		t.Fatal("claim a full interval later refused")
	}

	advisory := domain.SecurityAdvisory{ID: "GHSA-test", Summary: "Fix", Severity: "high", URL: "https://example.com/a", FixedIn: "1.0.2"}
	if err := store.RecordHeartbeatAnswer(ctx, application.RecordHeartbeatAnswerInput{
		CheckedAt: now, LatestVersion: "1.0.3", Advisories: []domain.SecurityAdvisory{advisory},
	}); err != nil {
		t.Fatalf("RecordHeartbeatAnswer: %v", err)
	}
	installation, err = store.GetInstallation(ctx)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if installation.LatestVersion != "1.0.3" || !installation.CheckedAt.Equal(now) ||
		len(installation.Advisories) != 1 || installation.Advisories[0] != advisory {
		t.Fatalf("installation after answer = %+v", installation)
	}
}
