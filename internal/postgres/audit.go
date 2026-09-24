package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
)

// execer is what a write runs its statements through: the transaction it
// is inside, or the pool for the few writes made outside one.
type execer = db.Execer

// appendAuditEvent hands one audit event to the edition's sink inside the
// caller's transaction. An empty ID gets a fresh UUID. Core keeps no
// audit table of its own: the sink decides whether the event is stored.
func (s *Store) appendAuditEvent(ctx context.Context, tx execer, event domain.AuditEvent) error {
	if event.ID == "" {
		event.ID = domain.ID(uuid.NewString())
	}
	return s.audit.Append(ctx, tx, event)
}

func (s *Store) OperationalMetrics(ctx context.Context) (application.ApprovalMetrics, error) {
	var metrics application.ApprovalMetrics
	err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE r.status = 'pending'),
		count(*) FILTER (WHERE r.status = 'approved'),
		count(*) FILTER (WHERE r.status = 'denied'),
		count(*) FILTER (WHERE r.status = 'expired'),
		count(*) FILTER (WHERE r.status = 'cancelled'),
		COALESCE(avg(EXTRACT(EPOCH FROM (d.decided_at - r.created_at)))
			FILTER (WHERE d.status IN ('approved', 'denied')), 0)
	FROM approval_requests r
	LEFT JOIN decisions d ON d.request_id = r.id`).Scan(
		&metrics.Pending,
		&metrics.Approved,
		&metrics.Denied,
		&metrics.Expired,
		&metrics.Cancelled,
		&metrics.AverageDecisionLatencySecs,
	)
	stats := s.pool.Stat()
	metrics.DatabaseAcquiredConns = stats.AcquiredConns()
	metrics.DatabaseIdleConns = stats.IdleConns()
	metrics.DatabaseMaxConns = stats.MaxConns()
	return metrics, err
}
