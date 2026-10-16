package authn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type signupOfferStub struct{ offered []string }

func (offer *signupOfferStub) OfferSignup(_ context.Context, email string) {
	offer.offered = append(offer.offered, email)
}

func TestARefusedPasswordForAnEmailWithoutAnAccountIsOfferedASignup(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	repository := &identityRepositoryStub{credentialErr: domain.ErrNotFound}
	service := loginTestService(repository, now)
	offer := &signupOfferStub{}
	service.RegisterSignupOffer(offer)

	_, err := service.LoginLocal(context.Background(), LocalLoginInput{Email: " New@Example.com ", Password: "anything at all"})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("the login must be refused the same way: got %v", err)
	}
	if len(offer.offered) != 1 || offer.offered[0] != "new@example.com" {
		t.Fatalf("expected one offer to the normalized address, got %v", offer.offered)
	}
	if !service.OffersSignup() {
		t.Fatal("a registered offer must be reported")
	}
}

func TestAnEmailWithAnAccountIsNeverOfferedASignup(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for name, repository := range map[string]*identityRepositoryStub{
		"wrong password": loginTestRepository(),
		// A social or SSO account has no password but is still an account.
		"no password": {
			credentialErr:      domain.ErrNotFound,
			loginOrganizations: application.LoginOrganizations{UserID: "user-1"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := loginTestService(repository, now)
			offer := &signupOfferStub{}
			service.RegisterSignupOffer(offer)
			_, err := service.LoginLocal(context.Background(), LocalLoginInput{Email: "owner@example.com", Password: "wrong"})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("expected ErrInvalidCredentials, got %v", err)
			}
			if len(offer.offered) != 0 {
				t.Fatalf("an account holder was offered a signup: %v", offer.offered)
			}
		})
	}
}

func TestWithoutAnOfferTheRefusalIsUnchanged(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	service := loginTestService(&identityRepositoryStub{credentialErr: domain.ErrNotFound}, now)
	if service.OffersSignup() {
		t.Fatal("no offer is registered")
	}
	if _, err := service.LoginLocal(context.Background(), LocalLoginInput{Email: "new@example.com", Password: "x"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}
