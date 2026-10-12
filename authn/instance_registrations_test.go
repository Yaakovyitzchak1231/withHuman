package authn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
)

type movingClock struct{ now time.Time }

func (c *movingClock) Now() time.Time { return c.now }

func registrationFixture(kind domain.AgentKind) (*Service, *agentRepositoryStub, *deliveryVerifierStub, *movingClock) {
	repository := &agentRepositoryStub{agentSlug: "harness", agentKind: kind}
	clock := &movingClock{now: time.Unix(80_000, 0).UTC()}
	service := NewService(&identityRepositoryStub{policy: domain.OrganizationPolicy{ApprovalReauthWindow: time.Hour}}, repository, nil, nil, entitlements.NewUnlimited(), nil, clock, ServiceConfig{CredentialTTL: time.Hour})
	verifier := &deliveryVerifierStub{}
	service.RegisterInstanceDeliveries(verifier)
	return service, repository, verifier, clock
}

func issuer(membershipID, userID domain.ID) domain.Principal {
	return domain.Principal{ActorType: domain.ActorHuman, ActorID: userID, OrganizationID: "org", MembershipID: membershipID}.
		WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionAgentCredentialIssue}))
}

// The first step shows a fresh registration ID and secret once and keeps
// only the sealed secret, bound to the agent and the caller; the second
// verifies that secret against the reserved instance and stores the
// receiver with the instance, consuming the registration.
func TestInstanceRegistrationVerifiesTheIssuedSecretAgainstTheReservedInstance(t *testing.T) {
	ctx := context.Background()
	service, repository, verifier, clock := registrationFixture(domain.AgentService)
	person := issuer("member", "user")

	registration, err := service.CreateAgentInstanceRegistration(ctx, person, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(string(registration.RegistrationID)); err != nil || !strings.HasPrefix(registration.SigningSecret, "whsec_") {
		t.Fatalf("registration = %+v", registration)
	}
	if !registration.ExpiresAt.Equal(clock.now.Add(time.Hour)) {
		t.Fatalf("registration expires at %s, want an hour from now", registration.ExpiresAt)
	}
	stored := repository.registrations[registration.RegistrationID]
	if stored.AgentID != "agent-1" || stored.ActorType != domain.ActorHuman || stored.ActorID != "user" || stored.InstanceID == "" {
		t.Fatalf("stored registration = %+v", stored)
	}
	if label := application.InstanceRegistrationSigningSecretLabel("org", registration.RegistrationID); !strings.HasPrefix(string(stored.SealedSigningSecret), label+"|") {
		t.Fatalf("the secret was not sealed under the registration's label: %q", stored.SealedSigningSecret)
	}

	created, err := service.CreateAgentInstanceWithDelivery(ctx, person, "agent-1", " receiver-runner ", nil,
		InstanceRegistrationDelivery{RegistrationID: registration.RegistrationID, URL: " https://harness.example.com/aap "})
	if err != nil {
		t.Fatal(err)
	}
	if verifier.input.InstanceID != stored.InstanceID || verifier.input.Delivery.SigningSecret != registration.SigningSecret ||
		verifier.input.Delivery.URL != "https://harness.example.com/aap" || verifier.input.Delivery.Type != "webhook" ||
		verifier.input.Delivery.RegistrationID != registration.RegistrationID {
		t.Fatalf("verification input = %+v", verifier.input)
	}
	input := repository.instanceInput
	if input.InstanceID != stored.InstanceID || input.Delivery == nil || input.Name != "receiver-runner" || input.ActorID != "user" {
		t.Fatalf("instance input = %+v", input)
	}
	if created.AgentInstance.Delivery == nil || !strings.HasPrefix(created.Credential.Token, "whc_") {
		t.Fatalf("created = %+v", created)
	}
	if _, ok := repository.registrations[registration.RegistrationID]; ok {
		t.Fatal("the registration was not consumed")
	}
}

// A failed verification keeps the registration and creates nothing, so
// the person can fix the receiver and try again until it expires.
func TestInstanceRegistrationSurvivesAFailedVerificationUntilItExpires(t *testing.T) {
	ctx := context.Background()
	service, repository, verifier, clock := registrationFixture(domain.AgentService)
	person := issuer("member", "user")
	registration, err := service.CreateAgentInstanceRegistration(ctx, person, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	delivery := InstanceRegistrationDelivery{RegistrationID: registration.RegistrationID, URL: "https://harness.example.com/aap"}

	verifier.err = domain.ErrWebhookVerificationFailed
	if _, err := service.CreateAgentInstanceWithDelivery(ctx, person, "agent-1", "runner", nil, delivery); !errors.Is(err, domain.ErrWebhookVerificationFailed) {
		t.Fatalf("failed verification = %v", err)
	}
	if repository.instanceInput.Name != "" {
		t.Fatalf("an instance was stored after a failed verification: %+v", repository.instanceInput)
	}
	if _, ok := repository.registrations[registration.RegistrationID]; !ok {
		t.Fatal("a failed verification dropped the registration")
	}

	// Another person, or the same person on another agent, cannot use it.
	if _, err := service.CreateAgentInstanceWithDelivery(ctx, issuer("other-member", "other-user"), "agent-1", "runner", nil, delivery); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another person's attempt = %v, want not found", err)
	}
	if _, err := service.CreateAgentInstanceWithDelivery(ctx, person, "agent-2", "runner", nil, delivery); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another agent's attempt = %v, want not found", err)
	}
	if _, err := service.CreateAgentInstanceWithDelivery(ctx, person, "agent-1", "  ", nil, delivery); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("blank name = %v, want invalid", err)
	}

	verifier.err = nil
	verifier.input = application.VerifyInstanceDeliveryInput{}
	clock.now = clock.now.Add(time.Hour)
	if _, err := service.CreateAgentInstanceWithDelivery(ctx, person, "agent-1", "runner", nil, delivery); !errors.Is(err, domain.ErrInstanceRegistrationExpired) {
		t.Fatalf("expired registration = %v", err)
	}
	if verifier.input.InstanceID != "" {
		t.Fatal("an expired registration reached the receiver")
	}
}

// Both steps need what generating an API key needs, a personal agent's
// instance belongs to the person, and a deployment without a secrets key
// offers neither step.
func TestInstanceRegistrationFollowsInstanceRegistrationRules(t *testing.T) {
	ctx := context.Background()
	service, repository, _, _ := registrationFixture(domain.AgentPersonal)
	person := issuer("member", "user")

	reviewer := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user", OrganizationID: "org", MembershipID: "member"}.
		WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleReviewer}))
	if _, err := service.CreateAgentInstanceRegistration(ctx, reviewer, "agent-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reviewer registration = %v, want forbidden", err)
	}
	if _, err := service.CreateAgentInstanceWithDelivery(ctx, reviewer, "agent-1", "runner", nil, InstanceRegistrationDelivery{RegistrationID: domain.ID(uuid.NewString()), URL: "https://harness.example.com"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reviewer completion = %v, want forbidden", err)
	}

	registration, err := service.CreateAgentInstanceRegistration(ctx, person, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAgentInstanceWithDelivery(ctx, person, "agent-1", "laptop", nil, InstanceRegistrationDelivery{RegistrationID: registration.RegistrationID, URL: "https://harness.example.com"}); err != nil {
		t.Fatal(err)
	}
	if repository.instanceInput.MembershipID != "member" {
		t.Fatalf("personal instance owner = %q, want the registering member", repository.instanceInput.MembershipID)
	}

	key := domain.Principal{ActorType: domain.ActorAPIKey, OrganizationID: "org", ActorID: "key", Auth: domain.AuthContext{Method: domain.AuthMethodAPIKey, CredentialID: "key", AuthenticatedAt: time.Unix(80_000, 0).UTC(), Assurance: "password"}}.
		WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionAgentCredentialIssue}))
	if _, err := service.CreateAgentInstanceRegistration(ctx, key, "agent-1"); !errors.Is(err, domain.ErrAgentOwnerRequired) {
		t.Fatalf("organization key on a personal agent = %v, want ErrAgentOwnerRequired", err)
	}

	unconfigured := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{time.Unix(80_000, 0).UTC()}, ServiceConfig{CredentialTTL: time.Hour})
	if _, err := unconfigured.CreateAgentInstanceRegistration(ctx, person, "agent-1"); !errors.Is(err, domain.ErrWebhookUnconfigured) {
		t.Fatalf("registration without a secrets key = %v, want unconfigured", err)
	}
}
