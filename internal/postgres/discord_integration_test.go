package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

func TestDiscordConnectionsAgainstRealPostgres(t *testing.T) {
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
	activate := func(slug, email string) (domain.ID, domain.ID, domain.ID) {
		orgID, userID, membershipID := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
		if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
			OrganizationID: orgID, OrganizationSlug: slug, OrganizationName: slug,
			UserID: userID, MembershipID: membershipID, DisplayName: "Owner",
			Email: email, PasswordHash: "argon2id$test",
			AdmissionSource: domain.AdmissionManual, Now: now,
		}); err != nil {
			t.Fatalf("ActivateOrganization %s: %v", slug, err)
		}
		return orgID, userID, membershipID
	}
	acmeID, acmeUser, acmeOwner := activate("acme", "owner@acme.example")
	rivalID, _, rivalOwner := activate("rival", "owner@rival.example")
	installation := application.DiscordInstallation{GuildID: "G0ACME", GuildName: "Acme"}

	t.Run("an install is stored and read back", func(t *testing.T) {
		connection, err := store.UpsertDiscordConnection(ctx, application.UpsertDiscordConnectionInput{
			OrganizationID: acmeID, Installation: installation, InstalledByMembershipID: acmeOwner, Now: now,
		})
		if err != nil || connection.GuildName != "Acme" || connection.GuildID != "G0ACME" || connection.InstalledByMembershipID != acmeOwner || connection.LinkedMembers != 0 {
			t.Fatalf("UpsertDiscordConnection = %+v, %v", connection, err)
		}
		var events int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM test_audit_events WHERE organization_id = $1 AND event_type = 'discord_connection.created'`, acmeID).Scan(&events); err != nil || events != 1 {
			t.Fatalf("audit events = %d, %v", events, err)
		}
	})

	t.Run("a guild belongs to one organization", func(t *testing.T) {
		if _, err := store.UpsertDiscordConnection(ctx, application.UpsertDiscordConnectionInput{
			OrganizationID: rivalID, Installation: installation, InstalledByMembershipID: rivalOwner, Now: now,
		}); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("second tenant claiming the guild = %v, want conflict", err)
		}
	})

	t.Run("linked members counts active members' links only", func(t *testing.T) {
		generation := installSlackFixture(t, store, acmeID, now)
		if _, err := store.LinkOrganizationContactMethod(ctx, application.LinkOrganizationContactMethodInput{
			MethodID: domain.ID(uuid.NewString()), UserID: acmeUser, OrganizationID: acmeID,
			Kind: domain.ContactDiscord, ExternalID: "100200300", Label: "Owner (@owner)", Now: now,
		}); err != nil {
			t.Fatalf("link: %v", err)
		}
		if _, err := store.LinkOrganizationContactMethod(ctx, application.LinkOrganizationContactMethodInput{
			MethodID: domain.ID(uuid.NewString()), UserID: acmeUser, OrganizationID: acmeID,
			SlackInstallationID: generation, Kind: domain.ContactSlack, ExternalID: "U0OWNER", Label: "@owner", Now: now,
		}); err != nil {
			t.Fatalf("slack link: %v", err)
		}
		connection, err := store.GetDiscordConnection(ctx, application.GetDiscordConnectionInput{OrganizationID: acmeID})
		if err != nil || connection.LinkedMembers != 1 {
			t.Fatalf("connection after link = %+v, %v, want one linked member (the Slack link does not count)", connection, err)
		}
	})

	t.Run("an OAuth state redeems once, for its own purpose, and expires", func(t *testing.T) {
		hash := sha256.Sum256([]byte("state-secret"))
		if err := store.CreateDiscordOAuthState(ctx, application.CreateDiscordOAuthStateInput{
			StateHash: hash[:], Purpose: application.DiscordPurposeLink, OrganizationID: acmeID, MembershipID: acmeOwner, UserID: acmeUser,
			ExpiresAt: now.Add(10 * time.Minute), Now: now,
		}); err != nil {
			t.Fatalf("CreateDiscordOAuthState: %v", err)
		}
		if _, err := store.ConsumeDiscordOAuthState(ctx, application.ConsumeDiscordOAuthStateInput{StateHash: hash[:], Purpose: application.DiscordPurposeInstall, Now: now.Add(time.Minute)}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("link state redeemed as an install = %v, want not found", err)
		}
		state, err := store.ConsumeDiscordOAuthState(ctx, application.ConsumeDiscordOAuthStateInput{StateHash: hash[:], Purpose: application.DiscordPurposeLink, Now: now.Add(time.Minute)})
		if err != nil || state.OrganizationID != acmeID || state.MembershipID != acmeOwner || state.UserID != acmeUser {
			t.Fatalf("ConsumeDiscordOAuthState = %+v, %v", state, err)
		}
		if _, err := store.ConsumeDiscordOAuthState(ctx, application.ConsumeDiscordOAuthStateInput{StateHash: hash[:], Purpose: application.DiscordPurposeLink, Now: now.Add(time.Minute)}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("second redemption = %v, want not found", err)
		}
		stale := sha256.Sum256([]byte("stale"))
		if err := store.CreateDiscordOAuthState(ctx, application.CreateDiscordOAuthStateInput{
			StateHash: stale[:], Purpose: application.DiscordPurposeInstall, OrganizationID: acmeID, MembershipID: acmeOwner, UserID: acmeUser,
			ExpiresAt: now.Add(time.Minute), Now: now,
		}); err != nil {
			t.Fatalf("create stale state: %v", err)
		}
		if _, err := store.ConsumeDiscordOAuthState(ctx, application.ConsumeDiscordOAuthStateInput{StateHash: stale[:], Purpose: application.DiscordPurposeInstall, Now: now.Add(2 * time.Minute)}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expired state = %v, want not found", err)
		}
	})

	t.Run("disconnecting returns the guild, removes the links, and frees the guild", func(t *testing.T) {
		guildID, err := store.DeleteDiscordConnection(ctx, application.DeleteDiscordConnectionInput{OrganizationID: acmeID, ActorMembershipID: acmeOwner, Now: now})
		if err != nil || guildID != "G0ACME" {
			t.Fatalf("DeleteDiscordConnection = %q, %v", guildID, err)
		}
		if _, err := store.GetDiscordConnection(ctx, application.GetDiscordConnectionInput{OrganizationID: acmeID}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("connection after delete = %v", err)
		}
		methods, err := store.ListContactMethods(ctx, application.ListContactMethodsInput{UserID: acmeUser, OrganizationID: acmeID})
		if err != nil || len(methods) != 1 || methods[0].Kind != domain.ContactSlack {
			t.Fatalf("methods after disconnect = %+v, %v, want the Slack link alone", methods, err)
		}
		if _, err := store.UpsertDiscordConnection(ctx, application.UpsertDiscordConnectionInput{
			OrganizationID: rivalID, Installation: installation, InstalledByMembershipID: rivalOwner, Now: now,
		}); err != nil {
			t.Fatalf("guild not freed for another organization: %v", err)
		}
		if _, err := store.DeleteDiscordConnection(ctx, application.DeleteDiscordConnectionInput{OrganizationID: acmeID, ActorMembershipID: acmeOwner, Now: now}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("second delete = %v, want not found", err)
		}
	})
}
