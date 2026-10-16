package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/telemetry"
)

// ClaimProvisioningKey reserves an AAP provisioning key for one attempt.
// A new or expired key takes the attempt's IDs and salt; a released key
// or one whose lease lapsed is taken over with the IDs it already holds.
// A completed key answers a replay; a different intent is a conflict and
// a live lease means another attempt is still running.
func (s *Store) ClaimProvisioningKey(ctx context.Context, input application.ClaimProvisioningKeyInput) (application.ProvisioningKeyClaim, error) {
	if err := input.Validate(); err != nil {
		return application.ProvisioningKeyClaim{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return application.ProvisioningKeyClaim{}, err
	}
	defer tx.Rollback(ctx)
	expiresAt := input.Now.Add(application.AAPIdempotencyRetention)
	inserted, err := tx.Exec(ctx, `INSERT INTO agent_instance_provisioning_keys
		(provisioner_id, idempotency_key, organization_id, intent_hash, agent_instance_id, credential_id, credential_salt,
		 claimed_until, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (provisioner_id, idempotency_key) DO NOTHING`,
		input.ProvisionerID, input.IdempotencyKey, input.OrganizationID, input.IntentHash, input.InstanceID,
		input.CredentialID, input.CredentialSalt, input.LeaseUntil, input.Now, expiresAt)
	if err != nil {
		return application.ProvisioningKeyClaim{}, err
	}
	claim := application.ProvisioningKeyClaim{InstanceID: input.InstanceID, CredentialID: input.CredentialID, CredentialSalt: input.CredentialSalt}
	if inserted.RowsAffected() == 0 {
		var intent []byte
		var claimedUntil, completedAt *time.Time
		var keyExpiresAt time.Time
		if err := tx.QueryRow(ctx, `SELECT intent_hash, agent_instance_id::text, credential_id::text, credential_salt,
			claimed_until, completed_at, expires_at
			FROM agent_instance_provisioning_keys WHERE provisioner_id = $1 AND idempotency_key = $2 FOR UPDATE`,
			input.ProvisionerID, input.IdempotencyKey).Scan(&intent, &claim.InstanceID, &claim.CredentialID, &claim.CredentialSalt,
			&claimedUntil, &completedAt, &keyExpiresAt); err != nil {
			return application.ProvisioningKeyClaim{}, err
		}
		switch {
		case !keyExpiresAt.After(input.Now):
			// A key past retention is discarded: this attempt starts afresh.
			claim = application.ProvisioningKeyClaim{InstanceID: input.InstanceID, CredentialID: input.CredentialID, CredentialSalt: input.CredentialSalt}
			if _, err := tx.Exec(ctx, `UPDATE agent_instance_provisioning_keys SET intent_hash = $3, agent_instance_id = $4,
				credential_id = $5, credential_salt = $6, claimed_until = $7, completed_at = NULL, created_at = $8, expires_at = $9
				WHERE provisioner_id = $1 AND idempotency_key = $2`, input.ProvisionerID, input.IdempotencyKey, input.IntentHash,
				input.InstanceID, input.CredentialID, input.CredentialSalt, input.LeaseUntil, input.Now, expiresAt); err != nil {
				return application.ProvisioningKeyClaim{}, err
			}
		case !bytes.Equal(intent, input.IntentHash):
			return application.ProvisioningKeyClaim{}, domain.ErrIdempotencyConflict
		case completedAt != nil:
			claim.Completed = true
		case claimedUntil != nil && claimedUntil.After(input.Now):
			return application.ProvisioningKeyClaim{}, domain.ErrIdempotencyInProgress
		default:
			if _, err := tx.Exec(ctx, `UPDATE agent_instance_provisioning_keys SET claimed_until = $3
				WHERE provisioner_id = $1 AND idempotency_key = $2`, input.ProvisionerID, input.IdempotencyKey, input.LeaseUntil); err != nil {
				return application.ProvisioningKeyClaim{}, err
			}
		}
	}
	return claim, tx.Commit(ctx)
}

func (s *Store) ReleaseProvisioningKey(ctx context.Context, input application.ReleaseProvisioningKeyInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE agent_instance_provisioning_keys SET claimed_until = NULL
		WHERE provisioner_id = $1 AND idempotency_key = $2 AND completed_at IS NULL`, input.ProvisionerID, input.IdempotencyKey)
	return err
}

// completeProvisioningKey marks the key whose lease minted the instance.
// It fails when the lease was lost to another attempt that completed.
func completeProvisioningKey(ctx context.Context, tx pgx.Tx, provisionerID domain.ID, key string, instanceID domain.ID, now time.Time) error {
	command, err := tx.Exec(ctx, `UPDATE agent_instance_provisioning_keys SET completed_at = $4, claimed_until = NULL
		WHERE provisioner_id = $1 AND idempotency_key = $2 AND agent_instance_id = $3 AND completed_at IS NULL`,
		provisionerID, key, instanceID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("provisioning key is not held by this attempt: %w", domain.ErrIdempotencyInProgress)
	}
	return nil
}

const provisionedInstanceSelect = `SELECT ai.id::text, ai.organization_id::text, ai.agent_id::text, a.slug, ai.name, ai.status,
	ai.metadata, ai.created_at, ai.last_seen_at, ai.deleted_at IS NOT NULL, ad.url, ad.registration_id::text
	FROM agent_instances ai
	JOIN agents a ON a.id = ai.agent_id AND a.organization_id = ai.organization_id
	LEFT JOIN agent_instance_deliveries ad ON ad.agent_instance_id = ai.id AND ad.organization_id = ai.organization_id
	WHERE ai.organization_id = $1 AND ai.id = $2 AND ai.agent_id = $3`

func scanProvisionedInstance(row pgx.Row) (application.ProvisionedInstance, error) {
	var view application.ProvisionedInstance
	var id, orgID, agentID, status string
	var url, registrationID *string
	err := row.Scan(&id, &orgID, &agentID, &view.Instance.AgentSlug, &view.Instance.Name, &status, &view.Instance.Metadata,
		&view.Instance.CreatedAt, &view.Instance.LastSeenAt, &view.Deleted, &url, &registrationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ProvisionedInstance{}, domain.ErrNotFound
	}
	if err != nil {
		return application.ProvisionedInstance{}, err
	}
	view.Instance.ID, view.Instance.OrganizationID, view.Instance.AgentID = domain.ID(id), domain.ID(orgID), domain.ID(agentID)
	view.Instance.Status = domain.AgentStatus(status)
	if url != nil && registrationID != nil {
		view.Instance.Delivery = &domain.AgentInstanceDelivery{Type: application.InstanceDeliveryWebhook, URL: *url, RegistrationID: domain.ID(*registrationID)}
	}
	return view, nil
}

// GetProvisionedInstance reads an instance of the provisioner's agent.
// An instance of another agent reads as absent.
func (s *Store) GetProvisionedInstance(ctx context.Context, input application.GetProvisionedInstanceInput) (application.ProvisionedInstance, error) {
	if err := input.Validate(); err != nil {
		return application.ProvisionedInstance{}, err
	}
	view, err := scanProvisionedInstance(s.pool.QueryRow(ctx, provisionedInstanceSelect, input.OrganizationID, input.InstanceID, input.AgentID))
	if err != nil || input.CredentialID == "" {
		return view, err
	}
	err = s.pool.QueryRow(ctx, `SELECT expires_at FROM agent_credentials WHERE organization_id = $1 AND id = $2 AND agent_instance_id = $3`,
		input.OrganizationID, input.CredentialID, input.InstanceID).Scan(&view.CredentialExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ProvisionedInstance{}, domain.ErrNotFound
	}
	return view, err
}

// ClaimUpdateKey follows ClaimProvisioningKey's rules for one instance's
// update keys; a completed key carries the response it answered.
func (s *Store) ClaimUpdateKey(ctx context.Context, input application.ClaimUpdateKeyInput) (application.UpdateKeyClaim, error) {
	if err := input.Validate(); err != nil {
		return application.UpdateKeyClaim{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return application.UpdateKeyClaim{}, err
	}
	defer tx.Rollback(ctx)
	expiresAt := input.Now.Add(application.AAPIdempotencyRetention)
	inserted, err := tx.Exec(ctx, `INSERT INTO agent_instance_update_keys
		(provisioner_id, agent_instance_id, idempotency_key, organization_id, intent_hash, claimed_until, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (provisioner_id, agent_instance_id, idempotency_key) DO NOTHING`,
		input.ProvisionerID, input.InstanceID, input.IdempotencyKey, input.OrganizationID, input.IntentHash, input.LeaseUntil, input.Now, expiresAt)
	if err != nil {
		return application.UpdateKeyClaim{}, err
	}
	var claim application.UpdateKeyClaim
	if inserted.RowsAffected() == 0 {
		var intent []byte
		var claimedUntil, completedAt *time.Time
		var keyExpiresAt time.Time
		if err := tx.QueryRow(ctx, `SELECT intent_hash, claimed_until, completed_at, expires_at, response
			FROM agent_instance_update_keys WHERE provisioner_id = $1 AND agent_instance_id = $2 AND idempotency_key = $3 FOR UPDATE`,
			input.ProvisionerID, input.InstanceID, input.IdempotencyKey).Scan(&intent, &claimedUntil, &completedAt, &keyExpiresAt, &claim.Response); err != nil {
			return application.UpdateKeyClaim{}, err
		}
		switch {
		case !keyExpiresAt.After(input.Now):
			claim = application.UpdateKeyClaim{}
			if _, err := tx.Exec(ctx, `UPDATE agent_instance_update_keys SET intent_hash = $4, claimed_until = $5, response = NULL,
				completed_at = NULL, created_at = $6, expires_at = $7
				WHERE provisioner_id = $1 AND agent_instance_id = $2 AND idempotency_key = $3`, input.ProvisionerID, input.InstanceID,
				input.IdempotencyKey, input.IntentHash, input.LeaseUntil, input.Now, expiresAt); err != nil {
				return application.UpdateKeyClaim{}, err
			}
		case !bytes.Equal(intent, input.IntentHash):
			return application.UpdateKeyClaim{}, domain.ErrIdempotencyConflict
		case completedAt != nil:
			claim.Completed = true
		case claimedUntil != nil && claimedUntil.After(input.Now):
			return application.UpdateKeyClaim{}, domain.ErrIdempotencyInProgress
		default:
			if _, err := tx.Exec(ctx, `UPDATE agent_instance_update_keys SET claimed_until = $4
				WHERE provisioner_id = $1 AND agent_instance_id = $2 AND idempotency_key = $3`,
				input.ProvisionerID, input.InstanceID, input.IdempotencyKey, input.LeaseUntil); err != nil {
				return application.UpdateKeyClaim{}, err
			}
		}
	}
	return claim, tx.Commit(ctx)
}

func (s *Store) ReleaseUpdateKey(ctx context.Context, input application.ReleaseUpdateKeyInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE agent_instance_update_keys SET claimed_until = NULL
		WHERE provisioner_id = $1 AND agent_instance_id = $2 AND idempotency_key = $3 AND completed_at IS NULL`,
		input.ProvisionerID, input.InstanceID, input.IdempotencyKey)
	return err
}

// UpdateProvisionedInstance renames the instance and replaces or removes
// its receiver. Removing it abandons the events still queued for it; a
// replacement leaves them queued, so they go to the new receiver with the
// new secret. The key completes with the response in the same
// transaction. A deleted instance reads as absent.
func (s *Store) UpdateProvisionedInstance(ctx context.Context, input application.UpdateProvisionedInstanceInput) (domain.AgentInstance, error) {
	if err := input.Validate(); err != nil {
		return domain.AgentInstance{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AgentInstance{}, err
	}
	defer tx.Rollback(ctx)
	var previousName, agentStatus string
	var deleted bool
	err = tx.QueryRow(ctx, `SELECT ai.name, ai.deleted_at IS NOT NULL, a.status
		FROM agent_instances ai JOIN agents a ON a.id = ai.agent_id AND a.organization_id = ai.organization_id
		WHERE ai.organization_id = $1 AND ai.id = $2 AND ai.agent_id = $3 FOR UPDATE OF ai`,
		input.OrganizationID, input.InstanceID, input.AgentID).Scan(&previousName, &deleted, &agentStatus)
	if errors.Is(err, pgx.ErrNoRows) || deleted {
		return domain.AgentInstance{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.AgentInstance{}, err
	}
	if domain.AgentStatus(agentStatus) == domain.AgentArchived {
		return domain.AgentInstance{}, domain.ErrAgentArchived
	}
	audit := func(eventType string, data map[string]any) error {
		encoded, _ := json.Marshal(data)
		return s.appendAuditEvent(ctx, tx, domain.AuditEvent{
			OrganizationID: input.OrganizationID, EventType: eventType, ActorType: domain.ActorAgent, ActorID: input.AgentID,
			SubjectType: "agent_instance", SubjectID: input.InstanceID, Data: encoded, OccurredAt: input.Now,
		})
	}
	if input.Name != nil && *input.Name != previousName {
		if _, err := tx.Exec(ctx, `UPDATE agent_instances SET name = $3 WHERE organization_id = $1 AND id = $2`,
			input.OrganizationID, input.InstanceID, *input.Name); err != nil {
			return domain.AgentInstance{}, agentIdentityConflict(err)
		}
		if err := audit("agent_instance.renamed", map[string]any{"previous_name": previousName, "name": *input.Name, "provisioner_id": input.ProvisionerID}); err != nil {
			return domain.AgentInstance{}, err
		}
	}
	switch input.DeliveryChange {
	case application.DeliveryReplaced:
		delivery := input.Delivery
		if _, err := tx.Exec(ctx, `INSERT INTO agent_instance_deliveries
			(agent_instance_id, organization_id, type, url, registration_id, sealed_signing_secret, verified_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (agent_instance_id) DO UPDATE SET type = EXCLUDED.type, url = EXCLUDED.url,
				registration_id = EXCLUDED.registration_id, sealed_signing_secret = EXCLUDED.sealed_signing_secret,
				verified_at = EXCLUDED.verified_at`, input.InstanceID, input.OrganizationID, application.InstanceDeliveryWebhook,
			delivery.URL, delivery.RegistrationID, delivery.SealedSigningSecret, delivery.VerifiedAt); err != nil {
			return domain.AgentInstance{}, err
		}
		if err := audit("agent_instance.delivery_configured", map[string]any{"url": delivery.URL, "registration_id": delivery.RegistrationID, "provisioner_id": input.ProvisionerID}); err != nil {
			return domain.AgentInstance{}, err
		}
	case application.DeliveryRemoved:
		removed, err := removeInstanceDelivery(ctx, tx, input.OrganizationID, input.InstanceID, input.Now)
		if err != nil {
			return domain.AgentInstance{}, err
		}
		if removed {
			if err := audit("agent_instance.delivery_removed", map[string]any{"provisioner_id": input.ProvisionerID}); err != nil {
				return domain.AgentInstance{}, err
			}
		}
	}
	view, err := scanProvisionedInstance(tx.QueryRow(ctx, provisionedInstanceSelect, input.OrganizationID, input.InstanceID, input.AgentID))
	if err != nil {
		return domain.AgentInstance{}, err
	}
	response, err := json.Marshal(view.Instance)
	if err != nil {
		return domain.AgentInstance{}, err
	}
	command, err := tx.Exec(ctx, `UPDATE agent_instance_update_keys SET completed_at = $4, claimed_until = NULL, response = $5
		WHERE provisioner_id = $1 AND agent_instance_id = $2 AND idempotency_key = $3 AND completed_at IS NULL`,
		input.ProvisionerID, input.InstanceID, input.IdempotencyKey, input.Now, response)
	if err != nil {
		return domain.AgentInstance{}, err
	}
	if command.RowsAffected() != 1 {
		return domain.AgentInstance{}, fmt.Errorf("update key is not held by this attempt: %w", domain.ErrIdempotencyInProgress)
	}
	return view.Instance, tx.Commit(ctx)
}

// removeInstanceDelivery drops the receiver and abandons the events still
// queued for it. It reports whether there was a receiver.
func removeInstanceDelivery(ctx context.Context, tx pgx.Tx, organizationID, instanceID domain.ID, now time.Time) (bool, error) {
	command, err := tx.Exec(ctx, `DELETE FROM agent_instance_deliveries WHERE organization_id = $1 AND agent_instance_id = $2`, organizationID, instanceID)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE request_resolved_events SET abandoned_at = $3
		WHERE organization_id = $1 AND agent_instance_id = $2 AND delivered_at IS NULL AND abandoned_at IS NULL`, organizationID, instanceID, now)
	return command.RowsAffected() > 0, err
}

// DeleteProvisionedInstance retires an instance of the provisioner's
// agent. The receiver goes first, so the cancellations that follow queue
// no notifications. The row stays disabled with deleted_at as the
// deletion record; deleting again only sweeps any request still pending
// and succeeds. It answers the requests it cancelled.
func (s *Store) DeleteProvisionedInstance(ctx context.Context, input application.DeleteProvisionedInstanceInput) ([]domain.ID, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var name string
	var deleted bool
	err = tx.QueryRow(ctx, `SELECT name, deleted_at IS NOT NULL FROM agent_instances
		WHERE organization_id = $1 AND id = $2 AND agent_id = $3 FOR UPDATE`, input.OrganizationID, input.InstanceID, input.AgentID).Scan(&name, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !deleted {
		if _, err := tx.Exec(ctx, `UPDATE agent_instances SET deleted_at = $3, status = 'disabled' WHERE organization_id = $1 AND id = $2`,
			input.OrganizationID, input.InstanceID, input.Now); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_credentials SET revoked_at = $3
			WHERE organization_id = $1 AND agent_instance_id = $2 AND revoked_at IS NULL`, input.OrganizationID, input.InstanceID, input.Now); err != nil {
			return nil, err
		}
		if _, err := removeInstanceDelivery(ctx, tx, input.OrganizationID, input.InstanceID, input.Now); err != nil {
			return nil, err
		}
		data, _ := json.Marshal(map[string]any{"name": name})
		if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
			OrganizationID: input.OrganizationID, EventType: "agent_instance.deleted", ActorType: domain.ActorAgent, ActorID: input.AgentID,
			SubjectType: "agent_instance", SubjectID: input.InstanceID, Data: data, OccurredAt: input.Now,
		}); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM approval_requests
		WHERE organization_id = $1 AND agent_instance_id = $2 AND status = 'pending' ORDER BY created_at FOR UPDATE`,
		input.OrganizationID, input.InstanceID)
	if err != nil {
		return nil, err
	}
	pending, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	auth := domain.AuthContext{Method: "provisioner", Assurance: "machine", AuthenticatedAt: input.Now}
	cancelled := make([]domain.ID, 0, len(pending))
	created := make([]time.Time, 0, len(pending))
	for _, requestID := range pending {
		createdAt, err := s.cancelLockedApproval(ctx, tx, input.OrganizationID, domain.ID(requestID), input.AgentID, string(input.InstanceID), name, auth, input.Now)
		if err != nil {
			return nil, err
		}
		cancelled = append(cancelled, domain.ID(requestID))
		created = append(created, createdAt)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	for _, createdAt := range created {
		telemetry.ApprovalResolved(ctx, string(domain.ApprovalCancelled), string(domain.DecisionSourceAgent), createdAt, input.Now)
	}
	return cancelled, nil
}
