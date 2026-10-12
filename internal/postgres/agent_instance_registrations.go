package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// CreateAgentInstanceRegistration stores a pending webhook registration
// started in the app. Its secret arrives sealed.
func (s *Store) CreateAgentInstanceRegistration(ctx context.Context, input application.CreateAgentInstanceRegistrationInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	registration := input.Registration
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_instance_registrations
		(id, organization_id, agent_id, agent_instance_id, actor_type, actor_id, sealed_signing_secret, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		registration.ID, registration.OrganizationID, registration.AgentID, registration.InstanceID,
		registration.ActorType, registration.ActorID, registration.SealedSigningSecret, registration.CreatedAt, registration.ExpiresAt)
	return err
}

// GetAgentInstanceRegistration reads a registration of the agent that the
// same actor started, expired or not; the caller compares the expiry.
func (s *Store) GetAgentInstanceRegistration(ctx context.Context, input application.GetAgentInstanceRegistrationInput) (application.AgentInstanceRegistration, error) {
	if err := input.Validate(); err != nil {
		return application.AgentInstanceRegistration{}, err
	}
	var registration application.AgentInstanceRegistration
	var id, organizationID, agentID, instanceID, actorType, actorID string
	err := s.pool.QueryRow(ctx, `SELECT id::text, organization_id::text, agent_id::text, agent_instance_id::text,
		actor_type, actor_id::text, sealed_signing_secret, created_at, expires_at
		FROM agent_instance_registrations
		WHERE organization_id = $1 AND agent_id = $2 AND id = $3 AND actor_type = $4 AND actor_id = $5`,
		input.OrganizationID, input.AgentID, input.RegistrationID, input.ActorType, input.ActorID).
		Scan(&id, &organizationID, &agentID, &instanceID, &actorType, &actorID, &registration.SealedSigningSecret,
			&registration.CreatedAt, &registration.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.AgentInstanceRegistration{}, fmt.Errorf("instance registration not found: %w", domain.ErrNotFound)
	}
	if err != nil {
		return application.AgentInstanceRegistration{}, err
	}
	registration.ID, registration.OrganizationID, registration.AgentID = domain.ID(id), domain.ID(organizationID), domain.ID(agentID)
	registration.InstanceID, registration.ActorType, registration.ActorID = domain.ID(instanceID), domain.ActorType(actorType), domain.ID(actorID)
	return registration, nil
}

// consumeAgentInstanceRegistration deletes the live registration that
// reserved the instance, inside the transaction that creates it. A
// registration that expired or was used meanwhile refuses the creation.
func consumeAgentInstanceRegistration(ctx context.Context, tx pgx.Tx, input application.CreateAgentInstanceWithCredentialInput, now time.Time) error {
	command, err := tx.Exec(ctx, `DELETE FROM agent_instance_registrations
		WHERE organization_id = $1 AND agent_id = $2 AND id = $3 AND agent_instance_id = $4
			AND actor_type = $5 AND actor_id = $6 AND expires_at > $7`,
		input.OrganizationID, input.AgentID, input.Delivery.RegistrationID, input.InstanceID, input.ActorType, input.ActorID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrInstanceRegistrationExpired
	}
	return nil
}
