package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// InstanceRegistration is the first step of registering an instance with
// AAP webhook delivery in the app: the registration ID and signing secret
// the person configures their receiver with. The secret appears only in
// this response.
type InstanceRegistration struct {
	RegistrationID domain.ID `json:"registration_id"`
	SigningSecret  string    `json:"signing_secret"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// InstanceRegistrationDelivery completes a registration: the receiver URL
// to verify with the registration's secret.
type InstanceRegistrationDelivery struct {
	RegistrationID domain.ID
	URL            string
}

// CreateAgentInstanceRegistration starts registering an instance with
// webhook delivery. It generates the registration ID and signing secret,
// reserves the instance ID, and keeps the secret sealed for
// application.InstanceRegistrationTTL, bound to the agent and the
// caller. It needs what registering an instance needs.
func (s *Service) CreateAgentInstanceRegistration(ctx context.Context, principal domain.Principal, agentID domain.ID) (InstanceRegistration, error) {
	if _, err := s.authorizeInstanceRegistration(ctx, principal, agentID); err != nil {
		return InstanceRegistration{}, err
	}
	if s.deliveries == nil {
		return InstanceRegistration{}, domain.ErrWebhookUnconfigured
	}
	registrationID := domain.ID(uuid.NewString())
	secret, sealed, err := s.deliveries.IssueRegistrationSecret(principal.OrganizationID, registrationID)
	if err != nil {
		return InstanceRegistration{}, err
	}
	now := s.clock.Now()
	registration := application.AgentInstanceRegistration{
		ID: registrationID, OrganizationID: principal.OrganizationID, AgentID: agentID, InstanceID: domain.ID(uuid.NewString()),
		ActorType: principal.ActorType, ActorID: principal.ActorID, SealedSigningSecret: sealed,
		CreatedAt: now, ExpiresAt: now.Add(application.InstanceRegistrationTTL),
	}
	if err := s.agents.CreateAgentInstanceRegistration(ctx, application.CreateAgentInstanceRegistrationInput{Registration: registration}); err != nil {
		return InstanceRegistration{}, err
	}
	return InstanceRegistration{RegistrationID: registrationID, SigningSecret: secret, ExpiresAt: registration.ExpiresAt}, nil
}

// CreateAgentInstanceWithDelivery completes a registration the caller
// started for this agent. The receiver must answer the same signed
// verification a provisioner's receiver does before the instance and its
// credential exist. A failure keeps the registration, so the person can
// fix the receiver and try again until it expires.
func (s *Service) CreateAgentInstanceWithDelivery(ctx context.Context, principal domain.Principal, agentID domain.ID, name string, metadata json.RawMessage, delivery InstanceRegistrationDelivery) (domain.AgentInstanceCredential, error) {
	ownerID, err := s.authorizeInstanceRegistration(ctx, principal, agentID)
	if err != nil {
		return domain.AgentInstanceCredential{}, err
	}
	if strings.TrimSpace(name) == "" {
		return domain.AgentInstanceCredential{}, fmt.Errorf("agent instance name is required: %w", domain.ErrInvalid)
	}
	if _, err := uuid.Parse(string(delivery.RegistrationID)); err != nil {
		return domain.AgentInstanceCredential{}, fmt.Errorf("delivery registration_id must be a UUID: %w", domain.ErrInvalid)
	}
	if s.deliveries == nil {
		return domain.AgentInstanceCredential{}, domain.ErrWebhookUnconfigured
	}
	registration, err := s.agents.GetAgentInstanceRegistration(ctx, application.GetAgentInstanceRegistrationInput{
		OrganizationID: principal.OrganizationID, AgentID: agentID, RegistrationID: delivery.RegistrationID,
		ActorType: principal.ActorType, ActorID: principal.ActorID,
	})
	if err != nil {
		return domain.AgentInstanceCredential{}, err
	}
	if !registration.ExpiresAt.After(s.clock.Now()) {
		return domain.AgentInstanceCredential{}, domain.ErrInstanceRegistrationExpired
	}
	secret, err := s.deliveries.OpenRegistrationSecret(principal.OrganizationID, registration.ID, registration.SealedSigningSecret)
	if err != nil {
		return domain.AgentInstanceCredential{}, err
	}
	verified, err := s.verifyDelivery(ctx, principal.OrganizationID, registration.InstanceID, application.ProposedInstanceDelivery{
		Type: application.InstanceDeliveryWebhook, URL: strings.TrimSpace(delivery.URL), RegistrationID: registration.ID, SigningSecret: secret,
	})
	if err != nil {
		return domain.AgentInstanceCredential{}, err
	}
	return s.registerAgentInstance(ctx, principal, agentID, ownerID, name, metadata, registration.InstanceID, &verified)
}
