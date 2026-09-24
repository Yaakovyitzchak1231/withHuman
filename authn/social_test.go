package authn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type socialAuthenticatorStub struct {
	key   string
	label string
}

func (stub socialAuthenticatorStub) Key() string           { return stub.key }
func (stub socialAuthenticatorStub) Label() string         { return stub.label }
func (socialAuthenticatorStub) Capabilities() []Capability { return []Capability{CapabilitySocial} }
func (socialAuthenticatorStub) Begin(context.Context, map[string]string) (Flow, error) {
	return Flow{}, nil
}
func (socialAuthenticatorStub) Complete(context.Context, map[string]string) (Assertion, error) {
	return Assertion{}, nil
}

func googleAssertion(now time.Time) Assertion {
	return Assertion{
		ProviderKey: "google", Issuer: "https://accounts.google.com", Subject: "sub-1",
		Email: " Ada@Example.com ", EmailVerified: true, DisplayName: "Ada",
		AuthenticatedAt: now, Methods: []string{"social"}, Assurance: "single_factor",
	}
}

func socialTestService(repository *identityRepositoryStub, now time.Time) *Service {
	service := loginTestService(repository, now)
	service.RegisterSocialAuthenticator(socialAuthenticatorStub{key: "google", label: "Google"})
	return service
}

func TestSocialProvidersReportRegistrationOrderAndEmptyDefault(t *testing.T) {
	service := loginTestService(loginTestRepository(), time.Unix(1, 0).UTC())
	if providers := service.SocialProviders(); providers == nil || len(providers) != 0 {
		t.Fatalf("an edition without providers must report an empty list, got %#v", providers)
	}
	service.RegisterSocialAuthenticator(socialAuthenticatorStub{key: "google", label: "Google"})
	service.RegisterSocialAuthenticator(socialAuthenticatorStub{key: "github", label: "GitHub"})
	providers := service.SocialProviders()
	if len(providers) != 2 || providers[0] != (SocialProvider{Key: "google", Label: "Google"}) || providers[1].Key != "github" {
		t.Fatalf("unexpected providers: %#v", providers)
	}
}

func TestRegisterSocialAuthenticatorRefusesReservedAndDuplicateKeys(t *testing.T) {
	for _, key := range []string{LocalProviderKey, "google"} {
		service := socialTestService(loginTestRepository(), time.Unix(1, 0).UTC())
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("registering %q did not panic", key)
				}
			}()
			service.RegisterSocialAuthenticator(socialAuthenticatorStub{key: key})
		}()
	}
}

func TestLoginWithAssertionRefusesUnregisteredProvider(t *testing.T) {
	repository := loginTestRepository()
	service := socialTestService(repository, time.Unix(1, 0).UTC())
	assertion := googleAssertion(time.Unix(1, 0).UTC())
	assertion.ProviderKey = "github"
	if _, err := service.LoginWithAssertion(context.Background(), AssertionLoginInput{Assertion: assertion}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("got %v, want unauthenticated", err)
	}
	if repository.sessions != 0 || len(repository.userLinks) != 0 {
		t.Fatal("an unregistered provider produced a session or a link")
	}
}

func TestLoginWithAssertionSignsKnownIdentityIn(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	repository := loginTestRepository()
	// The linked account's canonical email differs from the one the
	// provider reports today; the triple wins.
	repository.externalUser = &domain.User{ID: "user-1", PrimaryEmail: "ada@example.com", Status: domain.UserActive}
	service := socialTestService(repository, now)

	assertion := googleAssertion(now.Add(-time.Minute))
	assertion.Email = "ada@new-employer.example"
	assertion.EmailVerified = false
	result, err := service.LoginWithAssertion(context.Background(), AssertionLoginInput{Assertion: assertion})
	if err != nil {
		t.Fatal(err)
	}
	if result.Authentication == nil || result.Authentication.Principal.OrganizationID != "organization-1" {
		t.Fatalf("known identity did not complete the login: %#v", result)
	}
	if repository.session.Auth.Method != "social_google" || repository.session.Auth.Assurance != "single_factor" {
		t.Fatalf("unexpected social session: %#v", repository.session.Auth)
	}
	if repository.session.Auth.AuthenticatedAt != now.Add(-time.Minute) {
		t.Fatalf("session must carry the provider authentication time: %#v", repository.session.Auth)
	}
	if len(repository.userLinks) != 0 {
		t.Fatal("a known identity was linked again")
	}
}

func TestLoginWithAssertionLinksVerifiedEmailToExistingAccount(t *testing.T) {
	now := time.Unix(2_500, 0).UTC()
	repository := loginTestRepository()
	repository.userByEmail = &domain.User{ID: "user-1", PrimaryEmail: "ada@example.com", Status: domain.UserActive}
	service := socialTestService(repository, now)

	result, err := service.LoginWithAssertion(context.Background(), AssertionLoginInput{Assertion: googleAssertion(now)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Authentication == nil || repository.session.Auth.Method != "social_google" {
		t.Fatalf("linked identity did not sign in: %#v", result)
	}
	if len(repository.userLinks) != 1 || repository.userLinks[0].Email != "ada@example.com" || repository.userLinks[0].Subject != "sub-1" {
		t.Fatalf("identity was not linked with the normalized email: %#v", repository.userLinks)
	}
}

func TestLoginWithAssertionRefusesUnknownIdentities(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	tests := []struct {
		name      string
		configure func(*identityRepositoryStub, *Assertion)
		want      error
		linked    bool
	}{
		{name: "unverified email never links", configure: func(repository *identityRepositoryStub, assertion *Assertion) {
			repository.userByEmail = &domain.User{ID: "user-1", PrimaryEmail: "ada@example.com"}
			assertion.EmailVerified = false
		}, want: ErrEmailUnverified},
		{name: "missing email never links", configure: func(repository *identityRepositoryStub, assertion *Assertion) {
			assertion.Email = ""
		}, want: ErrEmailUnverified},
		{name: "verified email without an account", configure: func(*identityRepositoryStub, *Assertion) {}, want: ErrNoAccount, linked: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := loginTestRepository()
			service := socialTestService(repository, now)
			assertion := googleAssertion(now)
			test.configure(repository, &assertion)

			_, err := service.LoginWithAssertion(context.Background(), AssertionLoginInput{Assertion: assertion})
			if !errors.Is(err, test.want) || !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("got %v, want %v (forbidden)", err, test.want)
			}
			if repository.sessions != 0 || len(repository.loginTickets) != 0 {
				t.Fatal("a refused identity produced a session or ticket")
			}
			if (len(repository.userLinks) != 0) != test.linked {
				t.Fatalf("link attempts = %d, want attempted=%v", len(repository.userLinks), test.linked)
			}
		})
	}
}

func TestLoginWithAssertionSharesTheOrganizationStep(t *testing.T) {
	now := time.Unix(4_000, 0).UTC()
	t.Run("several organizations mint a social ticket", func(t *testing.T) {
		repository := loginTestRepository()
		repository.externalUser = &domain.User{ID: "user-1", PrimaryEmail: "ada@example.com"}
		repository.loginOrganizations.Organizations = append(repository.loginOrganizations.Organizations,
			domain.Organization{ID: "organization-2", Slug: "beta", Name: "Beta", Status: domain.OrganizationActive})
		service := socialTestService(repository, now)

		result, err := service.LoginWithAssertion(context.Background(), AssertionLoginInput{Assertion: googleAssertion(now)})
		if err != nil {
			t.Fatal(err)
		}
		if result.Authentication != nil || !strings.HasPrefix(result.Ticket, "whl_") || len(result.Organizations) != 2 {
			t.Fatalf("unexpected selection step: %#v", result)
		}
		if len(repository.loginTickets) != 1 || repository.loginTickets[0].AuthMethod != "social_google" || repository.loginTickets[0].Assurance != "single_factor" {
			t.Fatalf("ticket does not carry the social method: %#v", repository.loginTickets)
		}
	})
	t.Run("selecting redeems the ticket with its own method", func(t *testing.T) {
		repository := loginTestRepository()
		repository.ticketRecord = application.LoginTicketRecord{UserID: "user-1", AuthMethod: "social_google", Assurance: "single_factor", IssuedAt: now}
		service := socialTestService(repository, now.Add(time.Minute))
		if _, err := service.SelectLoginOrganization(context.Background(), SelectOrganizationInput{
			Ticket: "whl_1b4e28ba-2fa1-11d2-883f-0016d3cca427_secret", OrganizationID: "organization-1",
		}); err != nil {
			t.Fatal(err)
		}
		if repository.session.Auth.Method != "social_google" {
			t.Fatalf("redeemed session was relabeled: %#v", repository.session.Auth)
		}
	})
	t.Run("enterprise organizations hand off to SSO", func(t *testing.T) {
		repository := loginTestRepository()
		repository.externalUser = &domain.User{ID: "user-1", PrimaryEmail: "ada@example.com"}
		service := socialTestService(repository, now)
		service.RegisterLoginPolicy(externalLoginEverywhere())
		result, err := service.LoginWithAssertion(context.Background(), AssertionLoginInput{Assertion: googleAssertion(now)})
		if err != nil {
			t.Fatal(err)
		}
		if !result.SSORequired || result.Ticket != "" || len(result.Organizations) != 1 || !result.Organizations[0].SSO {
			t.Fatalf("SSO-only account was not handed to SSO: %#v", result)
		}
		if repository.sessions != 0 {
			t.Fatal("a social login minted a session for a connected organization")
		}
	})
	t.Run("no organizations", func(t *testing.T) {
		repository := loginTestRepository()
		repository.externalUser = &domain.User{ID: "user-1", PrimaryEmail: "ada@example.com"}
		repository.loginOrganizations.Organizations = nil
		service := socialTestService(repository, now)
		if _, err := service.LoginWithAssertion(context.Background(), AssertionLoginInput{Assertion: googleAssertion(now)}); !errors.Is(err, ErrNoOrganizations) {
			t.Fatalf("got %v, want %v", err, ErrNoOrganizations)
		}
	})
}

func TestSocialLoginPreservesNativeClientThroughSelection(t *testing.T) {
	now := time.Unix(4000, 0).UTC()
	client := application.SessionClient{Kind: "ios", Context: []byte(`{"device_id":"phone-1"}`)}
	for _, multiple := range []bool{false, true} {
		repository := loginTestRepository()
		repository.externalUser = &domain.User{ID: "user-1", PrimaryEmail: "ada@example.com"}
		if multiple {
			repository.loginOrganizations.Organizations = append(repository.loginOrganizations.Organizations,
				domain.Organization{ID: "organization-2", Slug: "beta", Name: "Beta", Status: domain.OrganizationActive})
		}
		service := socialTestService(repository, now)
		result, err := service.LoginWithAssertion(context.Background(), AssertionLoginInput{Assertion: googleAssertion(now), Client: client})
		if err != nil {
			t.Fatal(err)
		}
		if multiple {
			ticket := repository.loginTickets[0]
			repository.ticketRecord = application.LoginTicketRecord{UserID: ticket.UserID, AuthMethod: ticket.AuthMethod, Assurance: ticket.Assurance, IssuedAt: ticket.IssuedAt, Client: ticket.Client}
			if _, err := service.SelectLoginOrganization(context.Background(), SelectOrganizationInput{Ticket: result.Ticket, OrganizationID: "organization-1"}); err != nil {
				t.Fatal(err)
			}
		}
		if repository.session.Client.Kind != client.Kind || string(repository.session.Client.Context) != string(client.Context) || repository.session.Auth.Method != "social_google" {
			t.Fatalf("native identity or device lost (multiple=%v): %#v", multiple, repository.session)
		}
	}
}
