package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// consumptionRequest is the locked request row a consumption write
// checks: who opened it, its state and the database's current time.
type consumptionRequest struct {
	status       domain.ApprovalStatus
	agentID      domain.ID
	instanceID   domain.ID
	instanceName string
	expiresAt    sql.NullTime
	now          time.Time
}

// lockConsumptionRequest locks the request row and refuses another
// instance's request the same way a read does.
func lockConsumptionRequest(ctx context.Context, tx pgx.Tx, organizationID, requestID, instanceID domain.ID) (consumptionRequest, error) {
	var locked consumptionRequest
	var status, agentID, lockedInstanceID string
	err := tx.QueryRow(ctx, `SELECT r.status, r.agent_id::text, r.agent_instance_id::text, r.agent_instance_name, d.expires_at, now()
		FROM approval_requests r
		LEFT JOIN decisions d ON d.request_id = r.id AND d.organization_id = r.organization_id
		WHERE r.organization_id = $1 AND r.id = $2
		FOR UPDATE OF r`, organizationID, requestID).
		Scan(&status, &agentID, &lockedInstanceID, &locked.instanceName, &locked.expiresAt, &locked.now)
	if errors.Is(err, pgx.ErrNoRows) {
		return consumptionRequest{}, domain.ErrNotFound
	}
	if err != nil {
		return consumptionRequest{}, err
	}
	if domain.ID(lockedInstanceID) != instanceID {
		return consumptionRequest{}, fmt.Errorf("only the instance that opened the request may use it: %w", domain.ErrForbidden)
	}
	locked.status, locked.agentID, locked.instanceID = domain.ApprovalStatus(status), domain.ID(agentID), domain.ID(lockedInstanceID)
	return locked, nil
}

// isConsumptionRace reports a lost race between two writers of the same
// consumption: a serialization failure, or the primary key refusing a
// row the losing snapshot could not see. Both rerun the transaction,
// which then sees the winner's row.
func isConsumptionRace(err error) bool {
	var pgError *pgconn.PgError
	return isSerializationFailure(err) ||
		(errors.As(err, &pgError) && pgError.Code == "23505" && pgError.ConstraintName == "approval_consumptions_pkey")
}

func retryConsumption(attempt func() error) error {
	var err error
	for try := 0; try < serializableAttempts; try++ {
		if err = attempt(); !isConsumptionRace(err) {
			return err
		}
	}
	return fmt.Errorf("something else changed this at the same time. Try again: %w", domain.ErrConflict)
}

// ConsumeApproval marks an approved request used by the instance that
// opened it, right before the adapter runs the call. Under the request's
// row lock it checks the instance, an existing consumption, the status
// and the decision expiry against the database clock, then records the
// consumption and approval.consumed. Repeating the key that consumed it
// returns the request as it is now, even after expiry.
func (s *Store) ConsumeApproval(ctx context.Context, input application.ConsumeApprovalInput) (domain.ApprovalRequest, error) {
	if err := input.Validate(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if err := retryConsumption(func() error { return s.consumeApproval(ctx, input) }); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return s.GetApproval(ctx, application.GetApprovalInput{OrganizationID: input.OrganizationID, RequestID: input.RequestID})
}

func (s *Store) consumeApproval(ctx context.Context, input application.ConsumeApprovalInput) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	request, err := lockConsumptionRequest(ctx, tx, input.OrganizationID, input.RequestID, input.AgentInstanceID)
	if err != nil {
		return err
	}
	var key string
	var consumedAt time.Time
	err = tx.QueryRow(ctx, `SELECT idempotency_key, consumed_at FROM approval_consumptions
		WHERE organization_id = $1 AND request_id = $2`, input.OrganizationID, input.RequestID).Scan(&key, &consumedAt)
	switch {
	case err == nil && key == input.IdempotencyKey:
		return tx.Commit(ctx)
	case err == nil:
		return domain.ApprovalAlreadyConsumedError{ConsumedAt: consumedAt}
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	if request.status != domain.ApprovalApproved {
		return domain.ErrApprovalNotApproved
	}
	if !request.expiresAt.Valid || !request.now.Before(request.expiresAt.Time) {
		return domain.ErrApprovalExpired
	}
	if _, err := tx.Exec(ctx, `INSERT INTO approval_consumptions (request_id, organization_id, idempotency_key, consumed_at)
		VALUES ($1, $2, $3, $4)`, input.RequestID, input.OrganizationID, input.IdempotencyKey, request.now); err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]any{"agent_instance_id": request.instanceID, "agent_instance_name": request.instanceName})
	if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: input.OrganizationID, EventType: "approval.consumed",
		ActorType: domain.ActorAgent, ActorID: request.agentID,
		SubjectType: "approval_request", SubjectID: input.RequestID,
		Data: data, OccurredAt: request.now,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReportApprovalOutcome records how a consumed approval's call went,
// once, with approval.outcome_reported. The identical outcome again
// returns the request unchanged. It never touches the request's status
// or decision.
func (s *Store) ReportApprovalOutcome(ctx context.Context, input application.ReportApprovalOutcomeInput) (domain.ApprovalRequest, error) {
	if err := input.Validate(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if err := retrySerializable(func() error { return s.reportApprovalOutcome(ctx, input) }); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return s.GetApproval(ctx, application.GetApprovalInput{OrganizationID: input.OrganizationID, RequestID: input.RequestID})
}

func (s *Store) reportApprovalOutcome(ctx context.Context, input application.ReportApprovalOutcomeInput) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	request, err := lockConsumptionRequest(ctx, tx, input.OrganizationID, input.RequestID, input.AgentInstanceID)
	if err != nil {
		return err
	}
	var status, note sql.NullString
	err = tx.QueryRow(ctx, `SELECT outcome_status, outcome_note FROM approval_consumptions
		WHERE organization_id = $1 AND request_id = $2`, input.OrganizationID, input.RequestID).Scan(&status, &note)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrApprovalNotConsumed
	}
	if err != nil {
		return err
	}
	if status.Valid {
		if domain.ConsumptionOutcomeStatus(status.String) == input.Status && note.String == input.Note {
			return tx.Commit(ctx)
		}
		return domain.ErrOutcomeConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE approval_consumptions
		SET outcome_status = $3, outcome_note = NULLIF($4, ''), outcome_reported_at = $5
		WHERE organization_id = $1 AND request_id = $2 AND outcome_status IS NULL`,
		input.OrganizationID, input.RequestID, input.Status, input.Note, request.now); err != nil {
		return err
	}
	payload := map[string]any{"agent_instance_id": request.instanceID, "agent_instance_name": request.instanceName, "status": input.Status}
	if input.Note != "" {
		payload["note"] = input.Note
	}
	data, _ := json.Marshal(payload)
	if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: input.OrganizationID, EventType: "approval.outcome_reported",
		ActorType: domain.ActorAgent, ActorID: request.agentID,
		SubjectType: "approval_request", SubjectID: input.RequestID,
		Data: data, OccurredAt: request.now,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
