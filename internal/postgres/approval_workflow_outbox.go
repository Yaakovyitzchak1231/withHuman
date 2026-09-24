package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

const approvalWorkflowStartTopic = "approval.human_review_requested"

func approvalWorkflowEventID(requestID domain.ID) domain.ID {
	return domain.ID(uuid.NewSHA1(uuid.NameSpaceURL, []byte("withhuman:approval-workflow:"+string(requestID))).String())
}

func (s *Store) ListApprovalWorkflowStarts(ctx context.Context, input application.ListApprovalWorkflowStartsInput) ([]application.ApprovalWorkflowStart, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	limit := input.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, organization_id::text, payload->>'request_id', created_at
		FROM outbox_events
		WHERE topic=$1 AND published_at IS NULL
		ORDER BY sequence
		LIMIT $2`, approvalWorkflowStartTopic, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	starts := make([]application.ApprovalWorkflowStart, 0)
	for rows.Next() {
		var start application.ApprovalWorkflowStart
		if err := rows.Scan(&start.EventID, &start.OrganizationID, &start.RequestID, &start.CreatedAt); err != nil {
			return nil, err
		}
		starts = append(starts, start)
	}
	return starts, rows.Err()
}

func (s *Store) MarkApprovalWorkflowStarted(ctx context.Context, input application.MarkApprovalWorkflowStartedInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	eventID := approvalWorkflowEventID(input.RequestID)
	command, err := s.pool.Exec(ctx, `UPDATE outbox_events SET published_at=$3
		WHERE id=$1 AND organization_id=$2 AND topic=$4 AND published_at IS NULL`, eventID,
		input.OrganizationID, input.PublishedAt, approvalWorkflowStartTopic)
	if err != nil {
		return err
	}
	if command.RowsAffected() > 0 {
		return nil
	}
	var published bool
	err = s.pool.QueryRow(ctx, `SELECT published_at IS NOT NULL FROM outbox_events
		WHERE id=$1 AND organization_id=$2 AND topic=$3`, eventID, input.OrganizationID, approvalWorkflowStartTopic).Scan(&published)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !published {
		return domain.ErrConflict
	}
	return nil
}
