package authn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/domain"
)

// DeterministicAuthenticator is a provider-free adapter for contract tests and
// local development. Callers register the exact assertion a flow must return;
// no provider-shaped claim leaks across this boundary.
type DeterministicAuthenticator struct {
	mu         sync.Mutex
	assertions map[string]Assertion
	now        func() time.Time
}

func NewDeterministicAuthenticator(now func() time.Time) *DeterministicAuthenticator {
	return &DeterministicAuthenticator{assertions: make(map[string]Assertion), now: now}
}

func (a *DeterministicAuthenticator) Key() string { return "deterministic" }

func (a *DeterministicAuthenticator) Capabilities() []Capability {
	return []Capability{CapabilityPassword, CapabilityPasswordless, CapabilityStepUp}
}

func (a *DeterministicAuthenticator) Register(code string, assertion Assertion) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.assertions[code] = assertion
}

func (a *DeterministicAuthenticator) Begin(_ context.Context, hints map[string]string) (Flow, error) {
	flowID := uuid.NewString()
	return Flow{ID: flowID, RedirectURL: "deterministic://login/" + flowID + "?email=" + hints["email"], ExpiresAt: a.now().Add(5 * time.Minute)}, nil
}

func (a *DeterministicAuthenticator) Complete(_ context.Context, params map[string]string) (Assertion, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	assertion, ok := a.assertions[params["code"]]
	if !ok {
		return Assertion{}, domain.ErrUnauthenticated
	}
	delete(a.assertions, params["code"])
	if assertion.AuthenticatedAt.IsZero() {
		assertion.AuthenticatedAt = a.now()
	}
	return assertion, nil
}

// MemorySessionPort is intentionally small and deterministic. It provides the
// same session contract as hosted adapters while keeping unit tests free of a
// network or database dependency.
type MemorySessionPort struct {
	mu       sync.Mutex
	sessions map[string]Session
	now      func() time.Time
	ttl      time.Duration
}

func NewMemorySessionPort(now func() time.Time, ttl time.Duration) *MemorySessionPort {
	return &MemorySessionPort{sessions: make(map[string]Session), now: now, ttl: ttl}
}

func (s *MemorySessionPort) CompatibleAuthenticatorKeys() []string { return []string{"deterministic"} }

func (s *MemorySessionPort) Create(_ context.Context, userID, _ domain.ID, assertion Assertion) (Session, string, error) {
	token, err := memoryToken()
	if err != nil {
		return Session{}, "", err
	}
	session := Session{ID: domain.ID(uuid.NewString()), UserID: userID, ExpiresAt: s.now().Add(s.ttl), AuthenticatedAt: assertion.AuthenticatedAt, Assurance: assertion.Assurance}
	s.mu.Lock()
	s.sessions[token] = session
	s.mu.Unlock()
	return session, token, nil
}

func (s *MemorySessionPort) Authenticate(_ context.Context, token string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[token]
	if !ok || !s.now().Before(session.ExpiresAt) {
		return Session{}, domain.ErrUnauthenticated
	}
	return session, nil
}

func (s *MemorySessionPort) Rotate(ctx context.Context, token string) (Session, string, error) {
	session, err := s.Authenticate(ctx, token)
	if err != nil {
		return Session{}, "", err
	}
	newToken, err := memoryToken()
	if err != nil {
		return Session{}, "", err
	}
	s.mu.Lock()
	delete(s.sessions, token)
	s.sessions[newToken] = session
	s.mu.Unlock()
	return session, newToken, nil
}

func (s *MemorySessionPort) Revoke(_ context.Context, sessionID domain.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, session := range s.sessions {
		if session.ID == sessionID {
			delete(s.sessions, token)
			return nil
		}
	}
	return domain.ErrNotFound
}

func memoryToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func Supports(capabilities []Capability, capability Capability) bool {
	return slices.Contains(capabilities, capability)
}
