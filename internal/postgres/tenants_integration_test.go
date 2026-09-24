package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// Exercises the tenant lifecycle against real PostgreSQL: the shared
// activation transaction for new and existing users, the status state
// machine, instant suspension cutoff for sessions, and the purge.
func TestTenantLifecycleAgainstRealPostgres(t *testing.T) {
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
	hook := &recordingPurgeHook{}
	store.WithPurgeHooks(hook)
	now := time.Now().UTC().Truncate(time.Microsecond)

	activate := func(slug, email string) application.ActivationResult {
		t.Helper()
		result, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
			OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: slug,
			OrganizationName: "Org " + slug, UserID: domain.ID(uuid.NewString()),
			MembershipID: domain.ID(uuid.NewString()), DisplayName: "Founder",
			Email: email, PasswordHash: "argon2id$test",
			AdmissionSource: domain.AdmissionSignup, Now: now,
		})
		if err != nil {
			t.Fatalf("ActivateOrganization(%s): %v", slug, err)
		}
		return result
	}

	first := activate("first-org", "founder@example.com")
	if first.Principal.MembershipID == "" || first.Organization.Status != domain.OrganizationActive {
		t.Fatalf("activation result incomplete: %#v", first)
	}

	// A second activation by the same email reuses the user: multi-org.
	second := activate("second-org", "Founder@Example.com")
	if second.Principal.ActorID != first.Principal.ActorID {
		t.Fatalf("existing founder was duplicated: %s vs %s", second.Principal.ActorID, first.Principal.ActorID)
	}
	var credentialCount int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM local_credentials WHERE user_id = $1`,
		string(first.Principal.ActorID)).Scan(&credentialCount); err != nil || credentialCount != 1 {
		t.Fatalf("existing user's credentials were touched: count=%d err=%v", credentialCount, err)
	}

	// Slug collisions surface as conflicts for explicit slugs.
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: "first-org",
		OrganizationName: "Imitator", UserID: domain.ID(uuid.NewString()),
		MembershipID: domain.ID(uuid.NewString()), DisplayName: "Imitator",
		Email: "imitator@example.com", AdmissionSource: domain.AdmissionSignup, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("slug collision = %v, want conflict", err)
	}

	// An active session dies the moment the organization is suspended.
	sessionHash := tokenHashOf("session-secret")
	if _, err := store.CreateSession(ctx, application.CreateSessionInput{
		UserID: first.Principal.ActorID, OrganizationID: first.Organization.ID,
		SecretHash: sessionHash, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		Auth: domain.AuthContext{Method: "signup", Assurance: "single_factor", AuthenticatedAt: now},
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	resolve := func() error {
		_, err := store.ResolveSession(ctx, application.ResolveSessionInput{SecretHash: sessionHash, Now: now.Add(time.Minute)})
		return err
	}
	if err := resolve(); err != nil {
		t.Fatalf("session did not resolve while active: %v", err)
	}
	suspended, err := store.SetOrganizationStatus(ctx, application.SetOrganizationStatusInput{
		OrganizationID: first.Organization.ID, Status: domain.OrganizationSuspended,
		Reason: "payment failed", ActorType: "system", Now: now,
	})
	if err != nil || suspended.Status != domain.OrganizationSuspended || suspended.StatusReason != "payment failed" {
		t.Fatalf("suspend = %#v, %v", suspended, err)
	}
	if err := resolve(); !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("session resolved during suspension: %v", err)
	}

	// Illegal transition: suspended from suspended.
	if _, err := store.SetOrganizationStatus(ctx, application.SetOrganizationStatusInput{
		OrganizationID: first.Organization.ID, Status: domain.OrganizationSuspended,
		Reason: "again", ActorType: "system", Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("double suspension = %v, want conflict", err)
	}

	// Resume restores access and clears the lifecycle bookkeeping.
	resumed, err := store.SetOrganizationStatus(ctx, application.SetOrganizationStatusInput{
		OrganizationID: first.Organization.ID, Status: domain.OrganizationActive,
		ActorType: "human", ActorID: first.Principal.ActorID, Now: now,
	})
	if err != nil || resumed.Status != domain.OrganizationActive || resumed.StatusReason != "" || resumed.SuspendedAt != nil {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	if err := resolve(); err != nil {
		t.Fatalf("session did not resolve after resume: %v", err)
	}

	// A purge before the schedule, or without pending_deletion, refuses.
	if purged, err := store.PurgeOrganization(ctx, application.PurgeOrganizationInput{
		OrganizationID: first.Organization.ID, Now: now,
	}); err != nil || purged {
		t.Fatalf("purge of active organization = %v, %v; want refusal", purged, err)
	}
	purgeAt := now.Add(time.Hour)
	if _, err := store.SetOrganizationStatus(ctx, application.SetOrganizationStatusInput{
		OrganizationID: first.Organization.ID, Status: domain.OrganizationPendingDeletion,
		Reason: "offboarding", DeletionScheduledFor: &purgeAt, ActorType: "human",
		ActorID: first.Principal.ActorID, Now: now,
	}); err != nil {
		t.Fatalf("schedule deletion: %v", err)
	}
	if err := resolve(); err == nil {
		t.Fatal("session resolved while pending deletion")
	}
	if purged, err := store.PurgeOrganization(ctx, application.PurgeOrganizationInput{
		OrganizationID: first.Organization.ID, Now: now,
	}); err != nil || purged {
		t.Fatalf("early purge = %v, %v; want refusal before the schedule", purged, err)
	}
	purged, err := store.PurgeOrganization(ctx, application.PurgeOrganizationInput{
		OrganizationID: first.Organization.ID, Now: purgeAt.Add(time.Minute),
	})
	if err != nil || !purged {
		t.Fatalf("purge = %v, %v", purged, err)
	}
	// Purge is idempotent: a second run finds nothing.
	if purged, err := store.PurgeOrganization(ctx, application.PurgeOrganizationInput{
		OrganizationID: first.Organization.ID, Now: purgeAt.Add(time.Minute),
	}); err != nil || purged {
		t.Fatalf("re-purge = %v, %v; want no-op", purged, err)
	}

	// Everything organization-scoped is gone; the global user and the
	// other organization survive.
	for _, table := range []string{"memberships", "sessions", "organization_policies", "invitations"} {
		var count int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE organization_id = $1`,
			string(first.Organization.ID)).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s still holds %d rows after purge (err=%v)", table, count, err)
		}
	}
	var organizationCount int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM organizations WHERE id = $1`,
		string(first.Organization.ID)).Scan(&organizationCount); err != nil || organizationCount != 0 {
		t.Fatalf("organization row survived purge: count=%d err=%v", organizationCount, err)
	}
	if _, err := store.GetUser(ctx, application.GetUserInput{UserID: first.Principal.ActorID}); err != nil {
		t.Fatalf("global user did not survive purge: %v", err)
	}
	if _, err := store.GetOrganization(ctx, application.GetOrganizationInput{OrganizationID: second.Organization.ID}); err != nil {
		t.Fatalf("unrelated organization did not survive purge: %v", err)
	}

	// The edition's purge hook ran inside the purge transaction, while the
	// organization row still existed, and only for the purged tenant.
	if len(hook.purged) != 1 || hook.purged[0] != first.Organization.ID || !hook.sawOrganization {
		t.Fatalf("purge hook saw %v (organization present: %v), want the purged tenant inside its transaction", hook.purged, hook.sawOrganization)
	}
}

// recordingPurgeHook is an edition's purge step: it notes which tenant
// was purged and whether the organization row was still there when it ran.
type recordingPurgeHook struct {
	purged          []domain.ID
	sawOrganization bool
}

func (h *recordingPurgeHook) PurgeOrganization(ctx context.Context, tx db.Execer, organizationID domain.ID) error {
	h.purged = append(h.purged, organizationID)
	// The transaction is the store's own; asking it a question proves the
	// hook runs inside it rather than on the pool.
	rows, err := tx.(interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}).Query(ctx, `SELECT 1 FROM organizations WHERE id = $1`, string(organizationID))
	if err != nil {
		return err
	}
	defer rows.Close()
	h.sawOrganization = rows.Next()
	return nil
}

// Email-first login against real PostgreSQL: the organization listing a
// verified email resolves to, and the single-use ticket that bridges the
// password step and the organization selection.
func TestEmailFirstLoginAgainstRealPostgres(t *testing.T) {
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

	activate := func(slug string) application.ActivationResult {
		t.Helper()
		result, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
			OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: slug,
			OrganizationName: "Org " + slug, UserID: domain.ID(uuid.NewString()),
			MembershipID: domain.ID(uuid.NewString()), DisplayName: "Founder",
			Email: "multi@example.com", PasswordHash: "argon2id$test",
			AdmissionSource: domain.AdmissionSignup, Now: now,
		})
		if err != nil {
			t.Fatalf("ActivateOrganization(%s): %v", slug, err)
		}
		return result
	}
	first := activate("login-one")
	second := activate("login-two")

	login, err := store.ListLoginOrganizations(ctx, application.ListLoginOrganizationsInput{Email: "MULTI@example.com"})
	if err != nil || login.UserID != first.Principal.ActorID || !login.HasPassword || len(login.Organizations) != 2 {
		t.Fatalf("login organizations = %#v, %v", login, err)
	}

	// Unknown emails resolve to the zero value, never an error, so the
	// methods endpoint stays uniform.
	unknown, err := store.ListLoginOrganizations(ctx, application.ListLoginOrganizationsInput{Email: "nobody@example.com"})
	if err != nil || unknown.UserID != "" || unknown.HasPassword || len(unknown.Organizations) != 0 {
		t.Fatalf("unknown email = %#v, %v", unknown, err)
	}

	// Suspension removes the organization from the login listing.
	if _, err := store.SetOrganizationStatus(ctx, application.SetOrganizationStatusInput{
		OrganizationID: second.Organization.ID, Status: domain.OrganizationSuspended,
		Reason: "test", ActorType: "system", Now: now,
	}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	login, err = store.ListLoginOrganizations(ctx, application.ListLoginOrganizationsInput{Email: "multi@example.com"})
	if err != nil || len(login.Organizations) != 1 || login.Organizations[0].ID != first.Organization.ID {
		t.Fatalf("post-suspension listing = %#v, %v", login, err)
	}

	// The ticket is single use and expires.
	ticketID := domain.ID(uuid.NewString())
	hash := tokenHashOf("ticket-secret")
	if err := store.CreateLoginTicket(ctx, application.CreateLoginTicketInput{
		TicketID: ticketID, UserID: first.Principal.ActorID, SecretHash: hash,
		AuthMethod: "social_google", Assurance: "single_factor",
		Client:   application.SessionClient{Kind: "ios", Context: []byte(`{"device_id":"phone-1"}`)},
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateLoginTicket: %v", err)
	}
	if _, err := store.ConsumeLoginTicket(ctx, application.ConsumeLoginTicketInput{
		TicketID: ticketID, SecretHash: tokenHashOf("wrong-secret"), Now: now.Add(time.Minute),
	}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("wrong secret = %v, want unauthenticated", err)
	}
	record, err := store.ConsumeLoginTicket(ctx, application.ConsumeLoginTicketInput{
		TicketID: ticketID, SecretHash: hash, Now: now.Add(time.Minute),
	})
	if err != nil || record.UserID != first.Principal.ActorID || !record.IssuedAt.Equal(now) {
		t.Fatalf("consume = %#v, %v", record, err)
	}
	if record.Client.Kind != "ios" || !strings.Contains(string(record.Client.Context), "phone-1") {
		t.Fatalf("ticket lost its device: %#v", record.Client)
	}
	// The ticket hands back the authentication that earned it.
	if record.AuthMethod != "social_google" || record.Assurance != "single_factor" {
		t.Fatalf("ticket lost its authentication context: %#v", record)
	}
	if _, err := store.ConsumeLoginTicket(ctx, application.ConsumeLoginTicketInput{
		TicketID: ticketID, SecretHash: hash, Now: now.Add(2 * time.Minute),
	}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("replay = %v, want unauthenticated", err)
	}
	expiredID := domain.ID(uuid.NewString())
	if err := store.CreateLoginTicket(ctx, application.CreateLoginTicketInput{
		TicketID: expiredID, UserID: first.Principal.ActorID, SecretHash: hash,
		AuthMethod: "local_password", Assurance: "single_factor",
		IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("CreateLoginTicket: %v", err)
	}
	if _, err := store.ConsumeLoginTicket(ctx, application.ConsumeLoginTicketInput{
		TicketID: expiredID, SecretHash: hash, Now: now.Add(10 * time.Minute),
	}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expired ticket = %v, want unauthenticated", err)
	}

	// The selection step's membership lookup is organization-ID keyed.
	membership, err := store.FindMembershipForLogin(ctx, application.FindMembershipForLoginInput{
		UserID: first.Principal.ActorID, OrganizationID: first.Organization.ID,
	})
	if err != nil || membership.Status != domain.MembershipActive {
		t.Fatalf("membership for login = %#v, %v", membership, err)
	}
	if _, err := store.FindMembershipForLogin(ctx, application.FindMembershipForLoginInput{
		UserID: first.Principal.ActorID, OrganizationID: second.Organization.ID,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("suspended organization membership = %v, want not found", err)
	}
}

// SSO identity linking for existing members: the organization's IdP
// asserting a member's email binds the provider identity without touching
// membership state.
func TestLinkVerifiedMemberIdentityAgainstRealPostgres(t *testing.T) {
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
	result, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: "link-org",
		OrganizationName: "Link Org", UserID: domain.ID(uuid.NewString()),
		MembershipID: domain.ID(uuid.NewString()), DisplayName: "Founder",
		Email: "founder@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	})
	if err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}

	link := application.LinkVerifiedMemberIdentityInput{
		OrganizationID: result.Organization.ID, Email: "Founder@Example.COM",
		ProviderKey: "polis", Issuer: "https://auth.example.com", Subject: "google-123", Now: now,
	}
	membership, err := store.LinkVerifiedMemberIdentity(ctx, link)
	if err != nil || membership.Status != domain.MembershipActive || membership.UserID != result.Principal.ActorID {
		t.Fatalf("link = %#v, %v", membership, err)
	}
	// The linked identity resolves on the next SSO login.
	user, err := store.ResolveExternalIdentity(ctx, application.ResolveExternalIdentityInput{
		ProviderKey: "polis", Issuer: "https://auth.example.com", Subject: "google-123",
		ObservedEmail: "founder@example.com", Now: now,
	})
	if err != nil || user.ID != result.Principal.ActorID {
		t.Fatalf("linked identity did not resolve: %#v, %v", user, err)
	}
	// Linking again is an idempotent upsert.
	if _, err := store.LinkVerifiedMemberIdentity(ctx, link); err != nil {
		t.Fatalf("relink: %v", err)
	}
	// No member under the email means no link.
	link.Email = "stranger@example.com"
	if _, err := store.LinkVerifiedMemberIdentity(ctx, link); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stranger link = %v, want not found", err)
	}
}
