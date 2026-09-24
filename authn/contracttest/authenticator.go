package contracttest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/domain"
)

type RegisteringAuthenticator interface {
	authn.Authenticator
	Register(string, authn.Assertion)
}

// Authenticator runs the provider-neutral behavior every authenticator adapter
// must satisfy. New production adapters call this same suite from their tests.
func Authenticator(t *testing.T, factory func() RegisteringAuthenticator) {
	t.Helper()
	t.Run("normalizes assertion", func(t *testing.T) {
		adapter := factory()
		assertion := authn.Assertion{ProviderKey: adapter.Key(), Issuer: "issuer", Subject: "subject-1", Email: "person@example.com", EmailVerified: true, DisplayName: "Person", AuthenticatedAt: time.Unix(100, 0), Methods: []string{"password"}, Assurance: "single_factor"}
		adapter.Register("valid", assertion)
		flow, err := adapter.Begin(context.Background(), map[string]string{"email": assertion.Email})
		if err != nil || flow.ID == "" || flow.RedirectURL == "" || flow.ExpiresAt.IsZero() {
			t.Fatalf("invalid begin result: %#v, %v", flow, err)
		}
		actual, err := adapter.Complete(context.Background(), map[string]string{"code": "valid"})
		if err != nil {
			t.Fatal(err)
		}
		if actual.ProviderKey != assertion.ProviderKey || actual.Issuer != assertion.Issuer || actual.Subject != assertion.Subject || !actual.EmailVerified {
			t.Fatalf("assertion was not preserved: %#v", actual)
		}
	})
	t.Run("rejects invalid and replayed callbacks", func(t *testing.T) {
		adapter := factory()
		adapter.Register("once", authn.Assertion{Subject: "subject"})
		if _, err := adapter.Complete(context.Background(), map[string]string{"code": "missing"}); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("expected unauthenticated, got %v", err)
		}
		if _, err := adapter.Complete(context.Background(), map[string]string{"code": "once"}); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Complete(context.Background(), map[string]string{"code": "once"}); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("replay was accepted: %v", err)
		}
	})
}

// Sessions verifies expiry, rotation, and revocation independently of the
// authenticator implementation.
func Sessions(t *testing.T, factory func(*time.Time) authn.SessionPort) {
	t.Helper()
	now := time.Unix(1_000, 0)
	port := factory(&now)
	session, token, err := port.Create(context.Background(), "user-1", "org-1", authn.Assertion{AuthenticatedAt: now, Assurance: "strong"})
	if err != nil || token == "" {
		t.Fatalf("create failed: %v", err)
	}
	if _, err := port.Authenticate(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	_, rotated, err := port.Rotate(context.Background(), token)
	if err != nil || rotated == token {
		t.Fatalf("rotation failed: %v", err)
	}
	if _, err := port.Authenticate(context.Background(), token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("old token remained valid: %v", err)
	}
	if err := port.Revoke(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := port.Authenticate(context.Background(), rotated); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked token remained valid: %v", err)
	}

	second, secondToken, err := port.Create(context.Background(), "user-1", "org-1", authn.Assertion{AuthenticatedAt: now})
	if err != nil || second.ID == "" {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := port.Authenticate(context.Background(), secondToken); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expired token remained valid: %v", err)
	}
}
