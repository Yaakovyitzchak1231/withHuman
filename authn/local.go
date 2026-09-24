package authn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

const (
	LocalProviderKey = "local"
	LocalIssuer      = "withhuman:self-hosted"
)

// LocalAuthenticator is the open edition's Authenticator adapter: email and
// password against the global local_credentials table. It deliberately has
// no membership requirement, which makes it usable for invitation
// redemption by users who are not yet members of the target organization.
type LocalAuthenticator struct {
	identities   application.IdentityRepository
	passwords    PasswordHasher
	clock        application.Clock
	publicOrigin string
}

func NewLocalAuthenticator(identities application.IdentityRepository, passwords PasswordHasher, clock application.Clock, publicOrigin string) *LocalAuthenticator {
	return &LocalAuthenticator{identities: identities, passwords: passwords, clock: clock, publicOrigin: publicOrigin}
}

func (a *LocalAuthenticator) Key() string { return LocalProviderKey }

func (a *LocalAuthenticator) Capabilities() []Capability {
	return []Capability{CapabilityPassword}
}

func (a *LocalAuthenticator) Begin(ctx context.Context, hints map[string]string) (Flow, error) {
	return Flow{
		ID:          "local",
		RedirectURL: strings.TrimRight(a.publicOrigin, "/") + "/",
		ExpiresAt:   a.clock.Now().Add(5 * time.Minute),
	}, nil
}

// ErrInvalidCredentials is the password outcome for an unknown email or a
// wrong password. It still is an ErrUnauthenticated, so every existing
// check holds, but it carries its own wire code so a sign-in form can say
// the credentials were wrong rather than that a session expired.
var ErrInvalidCredentials = fmt.Errorf("that email and password do not match: %w", domain.ErrUnauthenticated)

func (a *LocalAuthenticator) Complete(ctx context.Context, params map[string]string) (Assertion, error) {
	email := strings.ToLower(strings.TrimSpace(params["email"]))
	password := params["password"]
	if email == "" || password == "" || len(password) > maximumPasswordBytes {
		return Assertion{}, ErrInvalidCredentials
	}
	credential, err := a.identities.GetLocalCredential(ctx, application.GetLocalCredentialInput{Email: email})
	if errors.Is(err, domain.ErrNotFound) {
		return Assertion{}, ErrInvalidCredentials
	}
	if err != nil {
		return Assertion{}, err
	}
	valid, err := a.passwords.Verify(credential.PasswordHash, password)
	if err != nil {
		return Assertion{}, fmt.Errorf("verify local password: %w", err)
	}
	if !valid {
		return Assertion{}, ErrInvalidCredentials
	}
	return Assertion{
		ProviderKey:     LocalProviderKey,
		Issuer:          LocalIssuer,
		Subject:         string(credential.UserID),
		Email:           email,
		EmailVerified:   false,
		AuthenticatedAt: a.clock.Now(),
		Methods:         []string{"password"},
		Assurance:       "single_factor",
	}, nil
}

var _ Authenticator = (*LocalAuthenticator)(nil)
