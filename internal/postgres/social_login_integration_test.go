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

// Social login against real PostgreSQL: a founder admitted through a
// provider identity gets that identity and no password, the identity
// triple (not the email) finds them again, verified-email linking binds a
// provider identity to an existing account and audits it in every
// organization, and a triple bound to another account is refused.
func TestSocialIdentitiesAgainstRealPostgres(t *testing.T) {
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
	const issuer = "https://accounts.google.com"

	activate := func(slug, email, passwordHash, subject string) (application.ActivationResult, error) {
		return store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
			OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: slug,
			OrganizationName: "Org " + slug, UserID: domain.ID(uuid.NewString()),
			MembershipID: domain.ID(uuid.NewString()), DisplayName: "Founder",
			Email: email, PasswordHash: passwordHash,
			ProviderKey: map[bool]string{true: "google", false: ""}[subject != ""],
			Issuer:      map[bool]string{true: issuer, false: ""}[subject != ""],
			Subject:     subject, AdmissionSource: domain.AdmissionSignup, Now: now,
		})
	}
	countRows := func(query string, args ...any) int {
		t.Helper()
		var count int
		if err := store.Pool().QueryRow(ctx, query, args...).Scan(&count); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return count
	}

	// A provider-verified founder: provider identity, no local identity,
	// no password.
	social, err := activate("social-org", "social@example.com", "", "sub-social")
	if err != nil {
		t.Fatalf("social activation: %v", err)
	}
	userID := string(social.Principal.ActorID)
	if countRows(`SELECT count(*) FROM local_credentials WHERE user_id = $1`, userID) != 0 {
		t.Fatal("a provider-verified founder received a local credential")
	}
	if countRows(`SELECT count(*) FROM external_identities WHERE user_id = $1 AND provider_key = 'google' AND subject = 'sub-social' AND email_verified`, userID) != 1 ||
		countRows(`SELECT count(*) FROM external_identities WHERE user_id = $1 AND provider_key = 'local'`, userID) != 0 {
		t.Fatal("founder identity rows are wrong")
	}

	// The same subject with a changed email at the provider is the same
	// person: the triple wins over the email.
	again, err := activate("social-org-2", "renamed@example.com", "", "sub-social")
	if err != nil {
		t.Fatalf("second social activation: %v", err)
	}
	if again.Principal.ActorID != social.Principal.ActorID {
		t.Fatalf("provider identity did not find the existing founder: %s vs %s", again.Principal.ActorID, social.Principal.ActorID)
	}
	var observedEmail string
	if err := store.Pool().QueryRow(ctx, `SELECT last_observed_email FROM external_identities WHERE subject = 'sub-social'`).Scan(&observedEmail); err != nil || observedEmail != "renamed@example.com" {
		t.Fatalf("observed email = %q, %v", observedEmail, err)
	}
	if countRows(`SELECT count(*) FROM users WHERE lower(primary_email) = 'renamed@example.com'`) != 0 {
		t.Fatal("a provider email change created a second account")
	}

	// A password founder later founding another organization through a
	// provider gets the identity linked to the existing account.
	local, err := activate("local-org", "local@example.com", "argon2id$test", "")
	if err != nil {
		t.Fatalf("local activation: %v", err)
	}
	linkedByActivation, err := activate("local-org-2", "Local@Example.com", "", "sub-local")
	if err != nil {
		t.Fatalf("linking activation: %v", err)
	}
	if linkedByActivation.Principal.ActorID != local.Principal.ActorID {
		t.Fatal("existing founder was duplicated by a provider activation")
	}
	if countRows(`SELECT count(*) FROM external_identities WHERE user_id = $1`, string(local.Principal.ActorID)) != 2 {
		t.Fatal("provider identity was not linked to the existing founder")
	}

	// A bound subject asserting yet another email is still the bound
	// account: no account is created for the new email.
	bound, err := activate("bound-org", "other@example.com", "", "sub-local")
	if err != nil || bound.Principal.ActorID != local.Principal.ActorID {
		t.Fatalf("bound subject activation = %#v, %v", bound.Principal, err)
	}
	if countRows(`SELECT count(*) FROM users WHERE lower(primary_email) = 'other@example.com'`) != 0 {
		t.Fatal("a bound subject created a second account")
	}

	// Verified-email linking at login: binds, resolves, audits per active
	// organization, is idempotent, and refuses strangers and bound subjects.
	link := application.LinkVerifiedUserIdentityInput{
		Email: "Local@Example.COM", ProviderKey: "github", Issuer: "https://github.com", Subject: "gh-1", Now: now,
	}
	user, err := store.LinkVerifiedUserIdentity(ctx, link)
	if err != nil || user.ID != local.Principal.ActorID || user.PrimaryEmail != "local@example.com" {
		t.Fatalf("link = %#v, %v", user, err)
	}
	resolved, err := store.ResolveExternalIdentity(ctx, application.ResolveExternalIdentityInput{
		ProviderKey: "github", Issuer: "https://github.com", Subject: "gh-1", ObservedEmail: "local@example.com", Now: now,
	})
	if err != nil || resolved.ID != local.Principal.ActorID {
		t.Fatalf("linked identity did not resolve: %#v, %v", resolved, err)
	}
	for _, organization := range []domain.Organization{local.Organization, linkedByActivation.Organization} {
		if linked := auditEvents(t, ctx, store, `organization_id = $1 AND event_type = 'membership.identity_linked'`, organization.ID); len(linked) != 1 {
			t.Fatalf("identity link audit in %s = %d events", organization.Slug, len(linked))
		}
	}
	if _, err := store.LinkVerifiedUserIdentity(ctx, link); err != nil {
		t.Fatalf("relink: %v", err)
	}
	link.Email = "stranger@example.com"
	if _, err := store.LinkVerifiedUserIdentity(ctx, link); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stranger link = %v, want not found", err)
	}
	if _, err := store.LinkVerifiedUserIdentity(ctx, application.LinkVerifiedUserIdentityInput{
		Email: "social@example.com", ProviderKey: "github", Issuer: "https://github.com", Subject: "gh-1", Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("relinking a bound subject to another account = %v, want conflict", err)
	}
}
