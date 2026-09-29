package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// agentOwnerHandoffTopic carries a handoff to the request's workflow, which
// starts walking the pinned fallback when it receives the signal.
const agentOwnerHandoffTopic = "approval.agent_owner_handoff"

func agentOwnerHandoffEventID(requestID domain.ID) domain.ID {
	return domain.ID(uuid.NewSHA1(uuid.NameSpaceURL, []byte("withhuman:agent-owner-handoff:"+string(requestID))).String())
}

// handOffAgentOwnerRequests moves every pending request routed to this
// member as its agent's owner to the fallback path pinned beside it. It
// runs in the transaction that suspends or deprovisions the member, so a
// request is never left waiting on someone who can no longer decide it:
// the handoff row changes who current routing admits at once, and the
// outbox event tells the request's workflow to start walking the
// fallback. actorID may be empty for a directory-driven change.
func (s *Store) handOffAgentOwnerRequests(ctx context.Context, tx pgx.Tx, organizationID, membershipID domain.ID, actorType domain.ActorType, actorID domain.ID, now time.Time) error {
	rows, err := tx.Query(ctx, `INSERT INTO approval_request_agent_owner_handoffs (request_id, organization_id, created_at)
		SELECT arr.request_id, arr.organization_id, $3
		FROM approval_request_routing arr
		JOIN approval_requests r ON r.id = arr.request_id AND r.organization_id = arr.organization_id
		WHERE arr.organization_id = $1 AND arr.agent_owner_membership_id = $2
			AND arr.outcome = 'agent_owner' AND r.status = 'pending'
		ON CONFLICT (request_id) DO NOTHING
		RETURNING request_id::text`, organizationID, membershipID, now)
	if err != nil {
		return err
	}
	var requestIDs []string
	for rows.Next() {
		var requestID string
		if err := rows.Scan(&requestID); err != nil {
			rows.Close()
			return err
		}
		requestIDs = append(requestIDs, requestID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, requestID := range requestIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO outbox_events (id, organization_id, topic, payload, created_at)
			VALUES ($1, $2, $3, jsonb_build_object('request_id', $4::text), $5)
			ON CONFLICT (id) DO NOTHING`,
			agentOwnerHandoffEventID(domain.ID(requestID)), organizationID, agentOwnerHandoffTopic, requestID, now); err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{
			"agent_owner_membership_id": membershipID, "reason": "agent_owner_deactivated",
		})
		if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
			OrganizationID: organizationID, EventType: "routing.agent_owner_handoff",
			ActorType: actorType, ActorID: actorID,
			SubjectType: "approval_request", SubjectID: domain.ID(requestID),
			Data: data, OccurredAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ListAgentOwnerHandoffSignals lists the handoffs whose workflow signal has
// not been sent yet, oldest first.
func (s *Store) ListAgentOwnerHandoffSignals(ctx context.Context, input application.ListAgentOwnerHandoffSignalsInput) ([]application.AgentOwnerHandoffSignal, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	limit := input.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT organization_id::text, payload->>'request_id'
		FROM outbox_events
		WHERE topic = $1 AND published_at IS NULL
		ORDER BY sequence
		LIMIT $2`, agentOwnerHandoffTopic, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	signals := make([]application.AgentOwnerHandoffSignal, 0)
	for rows.Next() {
		var signal application.AgentOwnerHandoffSignal
		if err := rows.Scan(&signal.OrganizationID, &signal.RequestID); err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	return signals, rows.Err()
}

// MarkAgentOwnerHandoffSignaled records that the handoff reached the
// request's workflow. Marking twice is a no-op.
func (s *Store) MarkAgentOwnerHandoffSignaled(ctx context.Context, input application.MarkAgentOwnerHandoffSignaledInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	command, err := s.pool.Exec(ctx, `UPDATE outbox_events SET published_at = $3
		WHERE id = $1 AND organization_id = $2 AND topic = $4 AND published_at IS NULL`,
		agentOwnerHandoffEventID(input.RequestID), input.OrganizationID, input.SignaledAt, agentOwnerHandoffTopic)
	if err != nil {
		return err
	}
	if command.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM outbox_events WHERE id = $1 AND organization_id = $2 AND topic = $3)`,
		agentOwnerHandoffEventID(input.RequestID), input.OrganizationID, agentOwnerHandoffTopic).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return domain.ErrNotFound
	}
	return nil
}
