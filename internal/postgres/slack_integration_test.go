package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
	"github.com/withHumanAI/withHuman/internal/secrets"
)

func TestSlackConnectionsAgainstRealPostgres(t *testing.T) {
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
	sealer, err := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	store.WithSealer(sealer)

	now := time.Now().UTC().Truncate(time.Microsecond)
	activate := func(slug, email string) (domain.ID, domain.ID) {
		orgID, userID, membershipID := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
		if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
			OrganizationID: orgID, OrganizationSlug: slug, OrganizationName: slug,
			UserID: userID, MembershipID: membershipID, DisplayName: "Owner",
			Email: email, PasswordHash: "argon2id$test",
			AdmissionSource: domain.AdmissionManual, Now: now,
		}); err != nil {
			t.Fatalf("ActivateOrganization %s: %v", slug, err)
		}
		return orgID, membershipID
	}
	acmeID, acmeOwner := activate("acme", "owner@acme.example")
	rivalID, rivalOwner := activate("rival", "owner@rival.example")
	t.Run("concurrent workspace locks leave pooled connections for their work", func(t *testing.T) {
		config := store.pool.Config()
		config.MaxConns = 2
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		lockStore := &Store{pool: pool}
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		results := make(chan error, 8)
		for range 8 {
			go func() {
				results <- lockStore.WithSlackLock(bounded, application.SlackLockInput{OrganizationID: acmeID, TeamID: "T_LOCK", Work: func(ctx context.Context) error {
					_, err := pool.Exec(ctx, `SELECT pg_sleep(0.02)`)
					return err
				}})
			}()
		}
		for range 8 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
	})

	installation := application.SlackInstallation{
		TeamID: "T0ACME", TeamName: "Acme", BotUserID: "U0BOT",
		Scopes: []string{"chat:write", "users:read"}, BotToken: "xoxb-acme-token",
	}
	sealed, err := sealer.Seal(slackTokenLabel, installation.BotToken)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	t.Run("an install is stored sealed and read back without the token", func(t *testing.T) {
		connection, err := store.UpsertSlackConnection(ctx, application.UpsertSlackConnectionInput{
			OrganizationID: acmeID, Installation: installation, SealedBotToken: sealed,
			InstalledByMembershipID: acmeOwner, Now: now,
		})
		if err != nil || connection.TeamName != "Acme" || connection.InstalledByMembershipID != acmeOwner {
			t.Fatalf("UpsertSlackConnection = %+v, %v", connection, err)
		}
		var raw []byte
		if err := store.Pool().QueryRow(ctx, `SELECT sealed_bot_token FROM slack_connections WHERE organization_id = $1`,
			acmeID).Scan(&raw); err != nil || string(raw) == installation.BotToken {
			t.Fatalf("token stored in the clear or unreadable: %q, %v", raw, err)
		}
		token, err := store.OpenSlackBotToken(ctx, application.GetSlackConnectionInput{OrganizationID: acmeID})
		if err != nil || token != installation.BotToken {
			t.Fatalf("OpenSlackBotToken = %q, %v", token, err)
		}
	})

	t.Run("a workspace belongs to one organization", func(t *testing.T) {
		if _, err := store.UpsertSlackConnection(ctx, application.UpsertSlackConnectionInput{
			OrganizationID: rivalID, Installation: installation, SealedBotToken: sealed,
			InstalledByMembershipID: rivalOwner, Now: now,
		}); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("second tenant claiming the workspace = %v, want conflict", err)
		}
	})

	t.Run("reinstalling refreshes the token", func(t *testing.T) {
		resealed, _ := sealer.Seal(slackTokenLabel, "xoxb-acme-rotated")
		if _, err := store.UpsertSlackConnection(ctx, application.UpsertSlackConnectionInput{
			OrganizationID: acmeID, Installation: installation, SealedBotToken: resealed,
			InstalledByMembershipID: acmeOwner, Now: now.Add(time.Minute),
		}); err != nil {
			t.Fatalf("reinstall: %v", err)
		}
		token, err := store.OpenSlackBotToken(ctx, application.GetSlackConnectionInput{OrganizationID: acmeID})
		if err != nil || token != "xoxb-acme-rotated" {
			t.Fatalf("token after reinstall = %q, %v", token, err)
		}
	})

	t.Run("an install state redeems exactly once and expires", func(t *testing.T) {
		hash := sha256.Sum256([]byte("state-secret"))
		if err := store.CreateSlackInstallState(ctx, application.CreateSlackInstallStateInput{
			StateHash: hash[:], OrganizationID: acmeID, MembershipID: acmeOwner,
			ExpiresAt: now.Add(10 * time.Minute), Now: now,
		}); err != nil {
			t.Fatalf("CreateSlackInstallState: %v", err)
		}
		state, err := store.ConsumeSlackInstallState(ctx, application.ConsumeSlackInstallStateInput{StateHash: hash[:], Now: now.Add(time.Minute)})
		if err != nil || state.OrganizationID != acmeID || state.MembershipID != acmeOwner {
			t.Fatalf("ConsumeSlackInstallState = %+v, %v", state, err)
		}
		if _, err := store.ConsumeSlackInstallState(ctx, application.ConsumeSlackInstallStateInput{StateHash: hash[:], Now: now.Add(time.Minute)}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("second redemption = %v, want not found", err)
		}
		stale := sha256.Sum256([]byte("stale"))
		if err := store.CreateSlackInstallState(ctx, application.CreateSlackInstallStateInput{
			StateHash: stale[:], OrganizationID: acmeID, MembershipID: acmeOwner,
			ExpiresAt: now.Add(time.Minute), Now: now,
		}); err != nil {
			t.Fatalf("create stale state: %v", err)
		}
		if _, err := store.ConsumeSlackInstallState(ctx, application.ConsumeSlackInstallStateInput{StateHash: stale[:], Now: now.Add(2 * time.Minute)}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expired state = %v, want not found", err)
		}
	})

	t.Run("disconnecting returns the token to revoke and frees the workspace", func(t *testing.T) {
		current, _ := store.GetSlackConnection(ctx, application.GetSlackConnectionInput{OrganizationID: acmeID})
		token, err := store.DeleteSlackConnection(ctx, application.DeleteSlackConnectionInput{InstallationID: current.InstallationID, OrganizationID: acmeID, ActorMembershipID: acmeOwner, Now: now})
		if err != nil || token != "xoxb-acme-rotated" {
			t.Fatalf("DeleteSlackConnection = %q, %v", token, err)
		}
		if connection, err := store.GetSlackConnection(ctx, application.GetSlackConnectionInput{OrganizationID: acmeID}); err != nil || connection.Status != "disconnected" {
			t.Fatalf("connection after delete = %v", err)
		}
		if _, err := store.UpsertSlackConnection(ctx, application.UpsertSlackConnectionInput{
			OrganizationID: rivalID, Installation: installation, SealedBotToken: sealed,
			InstalledByMembershipID: rivalOwner, Now: now,
		}); err != nil {
			t.Fatalf("workspace not freed for another organization: %v", err)
		}
	})

	t.Run("competing recoveries cannot steal a newer generation", func(t *testing.T) {
		thirdID, thirdOwner := activate("third", "owner@third.example")
		previous, err := store.GetSlackConnection(ctx, application.GetSlackConnectionInput{OrganizationID: rivalID})
		if err != nil {
			t.Fatal(err)
		}
		version := &application.SlackConnectionVersion{OrganizationID: rivalID, InstallationID: previous.InstallationID}
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, target := range []struct{ org, member domain.ID }{{acmeID, acmeOwner}, {thirdID, thirdOwner}} {
			go func() {
				<-start
				_, err := store.UpsertSlackConnection(ctx, application.UpsertSlackConnectionInput{
					OrganizationID: target.org, InstalledByMembershipID: target.member, Installation: installation,
					SealedBotToken: sealed, PreviousOwner: version, Now: now,
				})
				results <- err
			}()
		}
		close(start)
		wins, conflicts := 0, 0
		for range 2 {
			err := <-results
			if err == nil {
				wins++
			} else if errors.Is(err, domain.ErrConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if wins != 1 || conflicts != 1 {
			t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
		}
		owner, err := store.FindSlackWorkspace(ctx, application.FindSlackWorkspaceInput{TeamID: installation.TeamID})
		if err != nil || owner.OrganizationID == rivalID {
			t.Fatalf("owner=%+v,%v", owner, err)
		}
		// Even a valid revocation observation must roll back if the destination
		// cannot be committed (here its installing membership does not exist).
		_, err = store.UpsertSlackConnection(ctx, application.UpsertSlackConnectionInput{
			OrganizationID: rivalID, InstalledByMembershipID: domain.ID(uuid.NewString()), Installation: installation,
			SealedBotToken: sealed, PreviousOwner: &application.SlackConnectionVersion{OrganizationID: owner.OrganizationID, InstallationID: owner.InstallationID}, Now: now,
		})
		if err == nil {
			t.Fatal("invalid installing member accepted")
		}
		retained, err := store.FindSlackWorkspace(ctx, application.FindSlackWorkspaceInput{TeamID: installation.TeamID})
		if err != nil || retained.InstallationID != owner.InstallationID {
			t.Fatalf("failed install removed owner: %+v,%v", retained, err)
		}
		if _, err := store.OpenSlackBotToken(ctx, application.GetSlackConnectionInput{OrganizationID: owner.OrganizationID}); err != nil {
			t.Fatal(err)
		}
	})
}
