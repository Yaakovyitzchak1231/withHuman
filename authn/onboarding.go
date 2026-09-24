package authn

import (
	"context"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// RegisterOnboarding wires the guided setup's session repository. The
// whb_ credential class stays here so every opaque token format has one
// home; the onboarding service owns everything else about a session.
func (s *Service) RegisterOnboarding(repository application.OnboardingRepository) {
	s.onboarding = repository
}

// NewOnboardingSecret draws the secret half of a whb_ bearer. The session
// id is only known once the setup code has been redeemed, so the secret
// is drawn first, stored as a hash by the redemption, and joined to the
// id afterwards with FormatOnboardingCredential.
func NewOnboardingSecret() (secret string, secretHash []byte, err error) {
	secret, err = randomToken(32)
	if err != nil {
		return "", nil, err
	}
	return secret, HashToken(secret), nil
}

// OnboardingBearerPrefix marks the guided setup's bearer, a credential
// class disjoint from sessions and from both API key kinds (whk_, who_).
const OnboardingBearerPrefix = "whb"

// FormatOnboardingCredential assembles the whb_ bearer for a session.
func FormatOnboardingCredential(sessionID domain.ID, secret string) string {
	return OnboardingBearerPrefix + "_" + string(sessionID) + "_" + secret
}

// ResolveOnboardingCredential authenticates a presented whb_ bearer with
// a live lookup. It answers with the session rather than a principal: the
// bearer is not an actor, it is a time-boxed grant tied to the member who
// started the session, and the onboarding service decides what that
// grant may do.
func (s *Service) ResolveOnboardingCredential(ctx context.Context, token string) (domain.OnboardingSession, error) {
	if s.onboarding == nil {
		return domain.OnboardingSession{}, domain.ErrUnauthenticated
	}
	sessionID, secret, err := ParseOpaqueToken(token, OnboardingBearerPrefix)
	if err != nil {
		return domain.OnboardingSession{}, domain.ErrUnauthenticated
	}
	return s.onboarding.ResolveOnboardingCredential(ctx, application.ResolveOnboardingCredentialInput{
		SessionID:  domain.ID(sessionID),
		SecretHash: HashToken(secret),
		Now:        s.clock.Now(),
	})
}
