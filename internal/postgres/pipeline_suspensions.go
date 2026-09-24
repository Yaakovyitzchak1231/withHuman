package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// insertPipelineSuspension writes the open suspension inside the
// initialization transaction.
func insertPipelineSuspension(ctx context.Context, tx execer, suspension domain.PipelineSuspension) error {
	metadata, err := json.Marshal(suspension.PreviousMetadata)
	if err != nil {
		return err
	}
	if suspension.PreviousMetadata == nil {
		metadata = []byte(`{}`)
	}
	_, err = tx.Exec(ctx, `INSERT INTO approval_request_pipeline_suspensions
		(request_id, organization_id, pipeline_evaluation_id, organization_pipeline_id, agent_pipeline_id,
		 resume_position, previous_metadata, attempted_block_count, total_attempts, total_cost, started_at, suspended_at)
		VALUES ($1, $2, $3, NULLIF($4::text, '')::uuid, NULLIF($5::text, '')::uuid, $6, $7, $8, $9, $10, $11, $12)`,
		suspension.RequestID, suspension.OrganizationID, suspension.EvaluationID,
		suspension.OrganizationPipelineID, suspension.AgentPipelineID, suspension.ResumePosition, metadata,
		suspension.AttemptedBlockCount, suspension.TotalAttempts, int64(suspension.TotalCost),
		suspension.StartedAt, suspension.SuspendedAt)
	return err
}

func (s *Store) GetPipelineSuspension(ctx context.Context, input application.GetPipelineSuspensionInput) (domain.PipelineSuspension, error) {
	if err := input.Validate(); err != nil {
		return domain.PipelineSuspension{}, err
	}
	var (
		suspension                                  domain.PipelineSuspension
		organizationPipelineID, agentTypePipelineID *string
		metadata                                    []byte
		totalCost                                   int64
		resumedAt                                   *time.Time
	)
	err := s.pool.QueryRow(ctx, `SELECT request_id::text, organization_id::text, pipeline_evaluation_id::text,
		organization_pipeline_id::text, agent_pipeline_id::text, resume_position, previous_metadata,
		attempted_block_count, total_attempts, total_cost, started_at, suspended_at, resumed_at
		FROM approval_request_pipeline_suspensions WHERE organization_id=$1 AND request_id=$2`,
		input.OrganizationID, input.RequestID).Scan(
		&suspension.RequestID, &suspension.OrganizationID, &suspension.EvaluationID,
		&organizationPipelineID, &agentTypePipelineID, &suspension.ResumePosition, &metadata,
		&suspension.AttemptedBlockCount, &suspension.TotalAttempts, &totalCost,
		&suspension.StartedAt, &suspension.SuspendedAt, &resumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PipelineSuspension{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.PipelineSuspension{}, err
	}
	if organizationPipelineID != nil {
		suspension.OrganizationPipelineID = domain.ID(*organizationPipelineID)
	}
	if agentTypePipelineID != nil {
		suspension.AgentPipelineID = domain.ID(*agentTypePipelineID)
	}
	suspension.TotalCost = uint64(totalCost)
	suspension.ResumedAt = resumedAt
	if err := json.Unmarshal(metadata, &suspension.PreviousMetadata); err != nil {
		return domain.PipelineSuspension{}, err
	}
	return suspension, nil
}

// ResumeApproval closes the suspension and applies the asynchronous
// segment's outcome. The first statement is the compare-and-set: a
// suspension already resumed changes nothing. A request a human decided in
// the meantime keeps its decision; the segment's evaluation evidence is
// still appended, but its automatic decision and routing are dropped.
func (s *Store) ResumeApproval(ctx context.Context, input application.ResumeApprovalInput) (domain.ApprovalRequest, error) {
	if err := input.Validate(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	organizationID, requestID, now := input.OrganizationID, input.RequestID, input.Now
	current := func() (domain.ApprovalRequest, error) {
		return s.GetApproval(ctx, application.GetApprovalInput{OrganizationID: organizationID, RequestID: requestID})
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `UPDATE approval_request_pipeline_suspensions SET resumed_at=$3
		WHERE organization_id=$1 AND request_id=$2 AND pipeline_evaluation_id=$4 AND resumed_at IS NULL`,
		organizationID, requestID, now, input.EvaluationID)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if command.RowsAffected() == 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.ApprovalRequest{}, err
		}
		return current()
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM approval_requests WHERE organization_id=$1 AND id=$2 FOR UPDATE`, organizationID, requestID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ApprovalRequest{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	pending := domain.ApprovalStatus(status) == domain.ApprovalPending
	for _, event := range input.AuditEvents {
		if !pending && (event.EventType == "approval.decided" || event.EventType == "routing.evaluated") {
			continue
		}
		if err := s.appendAuditEvent(ctx, tx, event); err != nil {
			return domain.ApprovalRequest{}, err
		}
	}
	if !pending {
		if err := tx.Commit(ctx); err != nil {
			return domain.ApprovalRequest{}, err
		}
		return current()
	}
	if decision := input.Decision; decision != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO decisions
			(id, request_id, organization_id, source, status, note, channel, auth_method, auth_assurance,
			 authenticated_at, idempotency_key, decided_at)
			VALUES ($1, $2, $3, 'pipeline', $4, $5, $6, $7, $8, $9, $10, $11)`, decision.ID, decision.RequestID,
			decision.OrganizationID, decision.Status, decision.Note, decision.Channel, decision.Auth.Method,
			decision.Auth.Assurance, decision.Auth.AuthenticatedAt, decision.IdempotencyKey,
			decision.DecidedAt); err != nil {
			return domain.ApprovalRequest{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE approval_requests SET status=$3, updated_at=$4 WHERE organization_id=$1 AND id=$2`,
			organizationID, requestID, decision.Status, decision.DecidedAt); err != nil {
			return domain.ApprovalRequest{}, err
		}
		if err := closeOpenClaims(ctx, tx, organizationID, requestID, domain.ClaimDecided, now); err != nil {
			return domain.ApprovalRequest{}, err
		}
	}
	if routing := input.Routing; routing != nil {
		if err := insertApprovalRouting(ctx, tx, *routing); err != nil {
			return domain.ApprovalRequest{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return current()
}
