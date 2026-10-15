package postgres

import (
	"context"

	"github.com/google/uuid"
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
