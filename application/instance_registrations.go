package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// InstanceRegistrationTTL is how long a pending webhook registration
// started in the app waits for its receiver to be verified.
const InstanceRegistrationTTL = time.Hour

// InstanceRegistrationSigningSecretLabel is the associated data a pending
// registration's sealed secret is bound to, so it opens only as that
// registration's.
func InstanceRegistrationSigningSecretLabel(organizationID, registrationID domain.ID) string {
	return "agent_instance_registration:" + string(organizationID) + ":" + string(registrationID)
}

// AgentInstanceRegistration is the pending half of creating an instance
// with webhook delivery from the app: the registration ID and the sealed
// signing secret the person configures their receiver with, bound to the
// agent and the actor who started it. The instance ID is reserved so
// every verification attempt names the instance it would create.
type AgentInstanceRegistration struct {
	ID                  domain.ID
	OrganizationID      domain.ID
	AgentID             domain.ID
	InstanceID          domain.ID
	ActorType           domain.ActorType
	ActorID             domain.ID
	SealedSigningSecret []byte
	CreatedAt           time.Time
	ExpiresAt           time.Time
}

func (registration AgentInstanceRegistration) Validate() error {
	if registration.ActorType != domain.ActorHuman && registration.ActorType != domain.ActorAPIKey {
		return invalidField("registration actor type", "must be human or api_key")
	}
	if err := firstError([]error{
		requiredID("registration ID", registration.ID),
		requiredID("organization ID", registration.OrganizationID),
		requiredID("agent ID", registration.AgentID),
		requiredID("agent instance ID", registration.InstanceID),
		requiredID("actor ID", registration.ActorID),
		requiredTime("registration creation time", registration.CreatedAt),
		requiredTime("registration expiry time", registration.ExpiresAt),
	}); err != nil {
		return err
	}
	if len(registration.SealedSigningSecret) == 0 {
		return invalidField("sealed registration signing secret", "is required")
	}
	if !registration.ExpiresAt.After(registration.CreatedAt) {
		return invalidField("registration expiry time", "must be after the creation time")
	}
	return nil
}

type CreateAgentInstanceRegistrationInput struct {
	Registration AgentInstanceRegistration
}

func (input CreateAgentInstanceRegistrationInput) Validate() error {
	return input.Registration.Validate()
}

// GetAgentInstanceRegistrationInput reads one pending registration of the
// agent that the same actor started. Any other registration reads as
// absent.
type GetAgentInstanceRegistrationInput struct {
	OrganizationID domain.ID
	AgentID        domain.ID
	RegistrationID domain.ID
	ActorType      domain.ActorType
	ActorID        domain.ID
}

func (input GetAgentInstanceRegistrationInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredID("registration ID", input.RegistrationID),
		requiredString("actor type", string(input.ActorType)),
		requiredID("actor ID", input.ActorID),
	})
}

// AgentInstanceRegistrationRepository keeps pending registrations until
// CreateAgentInstanceWithCredential consumes one with the instance it
// creates. Expired rows are pruned with the AAP state.
type AgentInstanceRegistrationRepository interface {
	CreateAgentInstanceRegistration(ctx context.Context, input CreateAgentInstanceRegistrationInput) error
	GetAgentInstanceRegistration(ctx context.Context, input GetAgentInstanceRegistrationInput) (AgentInstanceRegistration, error)
}
