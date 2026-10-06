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

// eligibleHolds selects the held requests a digest may still name: pending
// and with no live claim, the ladder's own stop rule applied at send time.
// $1 organization, $2 user, $3 kind, $4 now.
const eligibleHolds = `SELECT h.request_id::text FROM notification_holds h
	JOIN approval_requests r ON r.id = h.request_id AND r.organization_id = h.organization_id
	WHERE h.organization_id = $1 AND h.user_id = $2 AND h.kind = $3 AND r.status = 'pending'
	AND NOT EXISTS (SELECT 1 FROM approval_request_claims c
		WHERE c.request_id = r.id AND c.organization_id = r.organization_id
		AND c.released_at IS NULL AND c.expires_at > $4)
	ORDER BY r.created_at DESC, r.id`

// AdmitNotification decides one prompt under a lock on its window. A key
// with no window opens the first one and sends now; an open window holds.
// A window whose close is overdue holds too while anything eligible is
// still waiting in it, so the prompt's own workflow closes it at once;
// otherwise it was abandoned (everything in it was decided or claimed) and
// restarts at the first window, sending now.
func (s *Store) AdmitNotification(ctx context.Context, input application.AdmitNotificationInput) (application.NotificationAdmission, error) {
	if err := input.Validate(); err != nil {
		return application.NotificationAdmission{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return application.NotificationAdmission{}, err
	}
	defer tx.Rollback(ctx)
	key := []any{input.OrganizationID, input.UserID, string(input.Kind)}
	first := input.Now.Add(input.Windows[0])
	tag, err := tx.Exec(ctx, `INSERT INTO notification_windows (organization_id, user_id, kind, level, opened_at, closes_at)
		VALUES ($1, $2, $3, 0, $4, $5) ON CONFLICT DO NOTHING`, append(key, input.Now, first)...)
	if err != nil {
		return application.NotificationAdmission{}, err
	}
	if tag.RowsAffected() == 1 {
		return application.NotificationAdmission{}, tx.Commit(ctx)
	}
	var closesAt time.Time
	if err := tx.QueryRow(ctx, `SELECT closes_at FROM notification_windows
		WHERE organization_id = $1 AND user_id = $2 AND kind = $3 FOR UPDATE`, key...).Scan(&closesAt); err != nil {
		return application.NotificationAdmission{}, err
	}
	hold := closesAt.After(input.Now)
	if !hold {
		eligible, err := queryIDs(ctx, tx, eligibleHolds, append(key, input.Now)...)
		if err != nil {
			return application.NotificationAdmission{}, err
		}
		hold = len(eligible) > 0
	}
	if !hold {
		if _, err := tx.Exec(ctx, `DELETE FROM notification_holds WHERE organization_id = $1 AND user_id = $2 AND kind = $3`, key...); err != nil {
			return application.NotificationAdmission{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE notification_windows SET level = 0, opened_at = $4, closes_at = $5
			WHERE organization_id = $1 AND user_id = $2 AND kind = $3`, append(key, input.Now, first)...); err != nil {
			return application.NotificationAdmission{}, err
		}
		return application.NotificationAdmission{}, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notification_holds (organization_id, user_id, kind, request_id, held_at)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, append(key, input.RequestID, input.Now)...); err != nil {
		return application.NotificationAdmission{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return application.NotificationAdmission{}, err
	}
	return application.NotificationAdmission{Held: true, ClosesAt: closesAt}, nil
}

// CloseNotificationWindow closes the window a hold was answered with, once.
// A window that no longer closes at that instant was closed by another
// holder, and nothing happens. Otherwise every hold goes; with nothing
// eligible the window goes too, so the next prompt sends at once, and with
// something eligible the next, wider window opens now and each request the
// digest names is audited before the caller sends it.
func (s *Store) CloseNotificationWindow(ctx context.Context, input application.CloseNotificationWindowInput) (application.ClosedNotificationWindow, error) {
	if err := input.Validate(); err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	defer tx.Rollback(ctx)
	key := []any{input.OrganizationID, input.UserID, string(input.Kind)}
	var level int
	var openedAt, closesAt time.Time
	err = tx.QueryRow(ctx, `SELECT level, opened_at, closes_at FROM notification_windows
		WHERE organization_id = $1 AND user_id = $2 AND kind = $3 FOR UPDATE`, key...).Scan(&level, &openedAt, &closesAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ClosedNotificationWindow{}, nil
	}
	if err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	if !closesAt.Equal(input.ClosesAt) {
		return application.ClosedNotificationWindow{}, tx.Commit(ctx)
	}
	eligible, err := queryIDs(ctx, tx, eligibleHolds, append(key, input.Now)...)
	if err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	if len(eligible) == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM notification_windows WHERE organization_id = $1 AND user_id = $2 AND kind = $3`, key...); err != nil {
			return application.ClosedNotificationWindow{}, err
		}
		return application.ClosedNotificationWindow{}, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM notification_holds WHERE organization_id = $1 AND user_id = $2 AND kind = $3`, key...); err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	next := min(level+1, len(input.Windows)-1)
	if _, err := tx.Exec(ctx, `UPDATE notification_windows SET level = $4, opened_at = $5, closes_at = $6
		WHERE organization_id = $1 AND user_id = $2 AND kind = $3`, append(key, next, input.Now, input.Now.Add(input.Windows[next]))...); err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	// The name is a snapshot, so the request's history reads the same
	// after the person renames themselves or leaves.
	var displayName string
	if err := tx.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, input.UserID).Scan(&displayName); err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	data, err := json.Marshal(map[string]any{
		"membership_id": input.MembershipID, "user_id": input.UserID, "display_name": displayName, "kind": input.Kind,
		"window_opened_at": openedAt, "window_closed_at": closesAt, "request_count": len(eligible),
	})
	if err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	for _, requestID := range eligible {
		if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
			OrganizationID: input.OrganizationID, EventType: "notification.digested", ActorType: domain.ActorSystem,
			SubjectType: "approval_request", SubjectID: requestID, Data: data, OccurredAt: input.Now,
		}); err != nil {
			return application.ClosedNotificationWindow{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	rows, err := s.pool.Query(ctx, approvalSelect+`WHERE r.organization_id = $1 AND r.id = ANY($2::uuid[]) ORDER BY r.created_at DESC, r.id`,
		input.OrganizationID, idStrings(eligible))
	if err != nil {
		return application.ClosedNotificationWindow{}, err
	}
	defer rows.Close()
	var requests []domain.ApprovalRequest
	for rows.Next() {
		request, err := scanApproval(rows)
		if err != nil {
			return application.ClosedNotificationWindow{}, err
		}
		requests = append(requests, request)
	}
	return application.ClosedNotificationWindow{Requests: requests}, rows.Err()
}

func queryIDs(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]domain.ID, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.ID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, domain.ID(id))
	}
	return ids, rows.Err()
}
