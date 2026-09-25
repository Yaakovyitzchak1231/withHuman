package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// recordRequestResolvedEvent queues the AAP request.resolved event for a
// request whose decision the caller's transaction has just inserted. Only
// requests of an instance with a verified receiver get one; the event's
// created_at is the decision's decided_at, read back so both match to the
// stored precision. A request has at most one event.
func recordRequestResolvedEvent(ctx context.Context, tx execer, organizationID, requestID domain.ID) error {
	_, err := tx.Exec(ctx, `INSERT INTO request_resolved_events
		(id, organization_id, agent_instance_id, request_id, created_at, retry_until, next_attempt_at)
		SELECT $3, r.organization_id, r.agent_instance_id, r.id, d.decided_at, d.decided_at + make_interval(secs => $4), d.decided_at
		  FROM approval_requests r
		  JOIN decisions d ON d.request_id = r.id AND d.organization_id = r.organization_id
		  JOIN agent_instance_deliveries ad ON ad.agent_instance_id = r.agent_instance_id AND ad.organization_id = r.organization_id
		 WHERE r.organization_id = $1 AND r.id = $2
		ON CONFLICT (request_id) DO NOTHING`, organizationID, requestID, uuid.NewString(),
		application.RequestResolvedRetryWindow.Seconds())
	return err
}

// ClaimDueRequestResolvedEvents leases up to Limit due events by moving
// their next attempt to LeaseUntil, skipping rows another worker holds.
// An event whose instance has no receiver is never claimed, and one whose
// instance is disabled waits until it is enabled again.
func (s *Store) ClaimDueRequestResolvedEvents(ctx context.Context, input application.ClaimDueRequestResolvedEventsInput) ([]application.RequestResolvedDelivery, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `WITH due AS (
		SELECT e.id FROM request_resolved_events e
		 WHERE e.delivered_at IS NULL AND e.abandoned_at IS NULL AND e.next_attempt_at <= $1
		 ORDER BY e.next_attempt_at
		 LIMIT $3
		 FOR UPDATE SKIP LOCKED)
		UPDATE request_resolved_events e SET next_attempt_at = $2
		  FROM due, agent_instance_deliveries ad, agent_instances ai
		 WHERE e.id = due.id AND ad.agent_instance_id = e.agent_instance_id AND ad.organization_id = e.organization_id
		   AND ai.id = e.agent_instance_id AND ai.organization_id = e.organization_id AND ai.status = 'active'
		RETURNING e.id::text, e.organization_id::text, e.agent_instance_id::text, e.request_id::text,
		          e.created_at, e.retry_until, e.attempt_count, ad.url, ad.sealed_signing_secret`,
		input.Now, input.LeaseUntil, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := make([]application.RequestResolvedDelivery, 0)
	for rows.Next() {
		var delivery application.RequestResolvedDelivery
		if err := rows.Scan(&delivery.EventID, &delivery.OrganizationID, &delivery.AgentInstanceID, &delivery.RequestID,
			&delivery.CreatedAt, &delivery.RetryUntil, &delivery.AttemptCount, &delivery.URL, &delivery.SealedSigningSecret); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

// RecordRequestResolvedAttempt stores one attempt: delivered, rescheduled,
// or abandoned once the retry window has no room for another attempt.
func (s *Store) RecordRequestResolvedAttempt(ctx context.Context, input application.RecordRequestResolvedAttemptInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	var statusCode *int
	if input.StatusCode != 0 {
		statusCode = &input.StatusCode
	}
	_, err := s.pool.Exec(ctx, `UPDATE request_resolved_events SET
		attempt_count = attempt_count + 1,
		last_attempted_at = $3::timestamptz,
		last_status_code = $4::integer,
		delivered_at = CASE WHEN $5::boolean THEN $3::timestamptz END,
		next_attempt_at = COALESCE($6::timestamptz, next_attempt_at),
		abandoned_at = CASE WHEN NOT $5::boolean AND $6::timestamptz IS NULL THEN $3::timestamptz END
		WHERE organization_id = $1 AND id = $2 AND delivered_at IS NULL AND abandoned_at IS NULL`,
		input.OrganizationID, input.EventID, input.AttemptedAt, statusCode, input.Delivered, input.NextAttemptAt)
	return err
}

// PruneAAPState drops idempotency keys past retention, taking the
// credential salts with them so an old credential can no longer be
// derived, and abandons undelivered events whose retry window closed
// while their instance was disabled and nothing attempted them.
func (s *Store) PruneAAPState(ctx context.Context, input application.PruneAAPStateInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM agent_instance_provisioning_keys WHERE expires_at <= $1`,
		`DELETE FROM agent_instance_update_keys WHERE expires_at <= $1`,
		`UPDATE request_resolved_events SET abandoned_at = $1
		  WHERE delivered_at IS NULL AND abandoned_at IS NULL AND retry_until <= $1`,
	} {
		if _, err := s.pool.Exec(ctx, statement, input.Now); err != nil {
			return err
		}
	}
	return nil
}
