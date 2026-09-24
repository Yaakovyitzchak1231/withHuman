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

func TestPasswordCredentialsAndResetsAgainstRealPostgres(t *testing.T) {
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

	activation, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: "acme", OrganizationName: "Acme",
		UserID: domain.ID(uuid.NewString()), MembershipID: domain.ID(uuid.NewString()), DisplayName: "Founder",
		Email: "founder@example.com", PasswordHash: "argon2id$old", AdmissionSource: domain.AdmissionSignup, Now: now,
	})
	if err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	userID, orgID := activation.Principal.ActorID, activation.Organization.ID

	// The credential is replaced in place: still exactly one row.
	if err := store.SetLocalCredential(ctx, application.SetLocalCredentialInput{
		UserID: userID, PasswordHash: "argon2id$new", Now: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SetLocalCredential: %v", err)
	}
	credential, err := store.GetLocalCredential(ctx, application.GetLocalCredentialInput{Email: "founder@example.com"})
	if err != nil || credential.PasswordHash != "argon2id$new" {
		t.Fatalf("credential not replaced: %+v %v", credential, err)
	}
	var count int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM local_credentials WHERE user_id = $1`, string(userID)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected one credential row, got %d (%v)", count, err)
	}

	// Two sessions; revoking "everywhere else" keeps exactly the named one.
	keep, other := tokenHashOf("keep"), tokenHashOf("other")
	var keepID domain.ID
	for _, secret := range [][]byte{keep, other} {
		principal, err := store.CreateSession(ctx, application.CreateSessionInput{
			UserID: userID, OrganizationID: orgID, SecretHash: secret, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
			Auth: domain.AuthContext{Method: "local_password", Assurance: "single_factor", AuthenticatedAt: now},
		})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if string(secret) == string(keep) {
			keepID = principal.Auth.SessionID
		}
	}
	if err := store.RevokeUserSessions(ctx, application.RevokeUserSessionsInput{UserID: userID, ExceptSessionID: keepID, Now: now.Add(time.Minute)}); err != nil {
		t.Fatalf("RevokeUserSessions: %v", err)
	}
	if _, err := store.ResolveSession(ctx, application.ResolveSessionInput{SecretHash: keep, Now: now.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("kept session must resolve: %v", err)
	}
	if _, err := store.ResolveSession(ctx, application.ResolveSessionInput{SecretHash: other, Now: now.Add(2 * time.Minute)}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("other session must be revoked, got %v", err)
	}
	if err := store.RevokeUserSessions(ctx, application.RevokeUserSessionsInput{UserID: userID, Now: now.Add(3 * time.Minute)}); err != nil {
		t.Fatalf("RevokeUserSessions (all): %v", err)
	}
	if _, err := store.ResolveSession(ctx, application.ResolveSessionInput{SecretHash: keep, Now: now.Add(4 * time.Minute)}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("every session must be revoked, got %v", err)
	}

	// A reset is single use, hash-checked, and expires.
	resetID := domain.ID(uuid.NewString())
	secret := tokenHashOf("reset-secret")
	if err := store.CreatePasswordReset(ctx, application.CreatePasswordResetInput{
		ResetID: resetID, UserID: userID, SecretHash: secret, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreatePasswordReset: %v", err)
	}
	if _, err := store.GetPasswordReset(ctx, application.GetPasswordResetInput{ResetID: resetID, SecretHash: tokenHashOf("wrong"), Now: now}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("wrong secret must be refused, got %v", err)
	}
	if _, err := store.GetPasswordReset(ctx, application.GetPasswordResetInput{ResetID: resetID, SecretHash: secret, Now: now.Add(2 * time.Hour)}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired reset must be refused, got %v", err)
	}
	record, err := store.GetPasswordReset(ctx, application.GetPasswordResetInput{ResetID: resetID, SecretHash: secret, Now: now})
	if err != nil || record.UserID != userID {
		t.Fatalf("live reset must preview: %+v %v", record, err)
	}
	record, err = store.ConsumePasswordReset(ctx, application.ConsumePasswordResetInput{ResetID: resetID, SecretHash: secret, Now: now.Add(time.Minute)})
	if err != nil || record.UserID != userID {
		t.Fatalf("consume: %+v %v", record, err)
	}
	if _, err := store.ConsumePasswordReset(ctx, application.ConsumePasswordResetInput{ResetID: resetID, SecretHash: secret, Now: now.Add(2 * time.Minute)}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second consume must be refused, got %v", err)
	}
	if _, err := store.GetPasswordReset(ctx, application.GetPasswordResetInput{ResetID: resetID, SecretHash: secret, Now: now.Add(2 * time.Minute)}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("consumed reset must not preview, got %v", err)
	}
}
