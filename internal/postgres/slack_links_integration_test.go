package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// A Slack link is organization scoped: one per person per workspace, one
// person per Slack account, invisible from another organization, and
// carried into recipient resolution for its own organization only.
func TestSlackLinksAgainstRealPostgres(t *testing.T) {
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
			UserID: userID, MembershipID: membershipID, DisplayName: slug + " owner",
			Email: email, PasswordHash: "argon2id$test", AdmissionSource: domain.AdmissionManual, Now: now,
		}); err != nil {
			t.Fatalf("ActivateOrganization %s: %v", slug, err)
		}
		return orgID, userID, membershipID
	}
	acmeID, acmeUser, _ := activate("acme", "owner@acme.example")
	rivalID, _, _ := activate("rival", "owner@rival.example")

	generation := installSlackFixture(t, store, acmeID, now)
	linkKind := func(kind domain.ContactMethodKind, user domain.ID, org domain.ID, externalID string) (domain.ContactMethod, error) {
		return store.LinkOrganizationContactMethod(ctx, application.LinkOrganizationContactMethodInput{
			MethodID: domain.ID(uuid.NewString()), UserID: user, OrganizationID: org,
			SlackInstallationID: generation, Kind: kind, ExternalID: externalID, Label: "Owner (@owner)", Now: now,
		})
	}
	link := func(user domain.ID, org domain.ID, slackID string) (domain.ContactMethod, error) {
		return linkKind(domain.ContactSlack, user, org, slackID)
	}

	linked, err := link(acmeUser, acmeID, "U0OWNER")
	if err != nil || !linked.Verified() || linked.Kind != domain.ContactSlack || linked.OrganizationID != acmeID {
		t.Fatalf("LinkOrganizationContactMethod = %+v, %v", linked, err)
	}

	t.Run("a person-wide kind cannot be linked this way", func(t *testing.T) {
		if _, err := linkKind(domain.ContactEmail, acmeUser, acmeID, "owner@acme.example"); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("email link = %v, want invalid", err)
		}
	})

	t.Run("re-linking refreshes in place", func(t *testing.T) {
		again, err := link(acmeUser, acmeID, "U0OWNER2")
		if err != nil || again.ID != linked.ID || again.Value != "U0OWNER2" {
			t.Fatalf("re-link = %+v, %v", again, err)
		}
	})

	t.Run("the list is scoped to the organization asked for", func(t *testing.T) {
		mine, err := store.ListContactMethods(ctx, application.ListContactMethodsInput{UserID: acmeUser, OrganizationID: acmeID})
		if err != nil || len(mine) != 1 || mine[0].Kind != domain.ContactSlack {
			t.Fatalf("acme view = %+v, %v", mine, err)
		}
		elsewhere, err := store.ListContactMethods(ctx, application.ListContactMethodsInput{UserID: acmeUser, OrganizationID: rivalID})
		if err != nil || len(elsewhere) != 0 {
			t.Fatalf("the link leaked into another organization's view: %+v, %v", elsewhere, err)
		}
	})

	t.Run("a Discord link sits beside the Slack one, scoped the same way", func(t *testing.T) {
		discord, err := linkKind(domain.ContactDiscord, acmeUser, acmeID, "100200300")
		if err != nil || discord.Kind != domain.ContactDiscord || !discord.Verified() || discord.ID == linked.ID {
			t.Fatalf("discord link = %+v, %v", discord, err)
		}
		mine, err := store.ListContactMethods(ctx, application.ListContactMethodsInput{UserID: acmeUser, OrganizationID: acmeID})
		if err != nil || len(mine) != 2 {
			t.Fatalf("acme view with both links = %+v, %v", mine, err)
		}
		again, err := linkKind(domain.ContactDiscord, acmeUser, acmeID, "100200301")
		if err != nil || again.ID != discord.ID || again.Value != "100200301" {
			t.Fatalf("discord re-link = %+v, %v", again, err)
		}
	})

	t.Run("recipient resolution carries the links for its organization", func(t *testing.T) {
		recipients, err := store.eligibleApprovalRecipients(ctx, acmeID)
		if err != nil || len(recipients) != 1 || recipients[0].SlackUserID != "U0OWNER2" || recipients[0].DiscordUserID != "100200301" {
			t.Fatalf("acme recipients = %+v, %v", recipients, err)
		}
		others, err := store.eligibleApprovalRecipients(ctx, rivalID)
		if err != nil || len(others) != 1 || others[0].SlackUserID != "" || others[0].DiscordUserID != "" {
			t.Fatalf("rival recipients = %+v, %v", others, err)
		}
	})

	t.Run("one Slack account links to one member per workspace", func(t *testing.T) {
		otherUser := domain.ID(uuid.NewString())
		if _, err := store.Pool().Exec(ctx, `INSERT INTO users (id, display_name, primary_email, status, created_at, updated_at)
            VALUES ($1, 'Other', 'other@acme.example', 'active', $2, $2)`, otherUser, now); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		if _, err := link(otherUser, acmeID, "U0OWNER2"); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("second member on the same Slack account = %v, want conflict", err)
		}
		if _, err := linkKind(domain.ContactDiscord, otherUser, acmeID, "100200301"); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("second member on the same Discord account = %v, want conflict", err)
		}
		// The same id under a different kind is a different account.
		if _, err := linkKind(domain.ContactDiscord, otherUser, acmeID, "U0OWNER2"); err != nil {
			t.Fatalf("discord link reusing a Slack id = %v, want allowed", err)
		}
	})

	t.Run("member addresses exclude unconfirmed emails", func(t *testing.T) {
		if _, err := store.CreateContactMethod(ctx, application.CreateContactMethodInput{
			MethodID: domain.ID(uuid.NewString()), UserID: acmeUser, Kind: domain.ContactEmail,
			Value: "typo@example.com", CodeHash: []byte("hash"), ExpiresAt: now.Add(time.Hour), Now: now,
		}); err != nil {
			t.Fatalf("create unconfirmed: %v", err)
		}
		members, err := store.ListMemberAddresses(ctx, application.ListMemberAddressesInput{OrganizationID: acmeID})
		if err != nil || len(members) != 1 {
			t.Fatalf("ListMemberAddresses = %+v, %v", members, err)
		}
		for _, address := range members[0].Emails {
			if address == "typo@example.com" {
				t.Fatalf("an unconfirmed address could be matched against Slack: %v", members[0].Emails)
			}
		}
	})
}
