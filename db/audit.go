package db

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/withHumanAI/withHuman/domain"
)

// Execer is what a sink or a hook may run statements through: the
// store's transaction, or its pool for the few writes core makes outside
// one.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// AuditSink receives every audit event core builds, inside the
// transaction that commits the action the event describes, so an adapter
// that keeps events keeps them atomic with the record. Core stores none
// itself: the open edition discards them, the hosted edition writes its
// own table and serves the audit log from it.
type AuditSink interface {
	Append(ctx context.Context, tx Execer, event domain.AuditEvent) error
}

// DiscardAudit is the open edition's sink.
type DiscardAudit struct{}

func (DiscardAudit) Append(context.Context, Execer, domain.AuditEvent) error { return nil }

// RecordingAudit keeps every event in memory, for tests that assert what
// an operation recorded. An event appended inside a transaction that
// later rolls back stays recorded, so assert on successful operations.
type RecordingAudit struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (r *RecordingAudit) Append(_ context.Context, _ Execer, event domain.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

// Events is what was recorded, oldest first.
func (r *RecordingAudit) Events() []domain.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.AuditEvent(nil), r.events...)
}

// OfType is the recorded events of one type, oldest first.
func (r *RecordingAudit) OfType(eventType string) []domain.AuditEvent {
	var matching []domain.AuditEvent
	for _, event := range r.Events() {
		if event.EventType == eventType {
			matching = append(matching, event)
		}
	}
	return matching
}

// Reset forgets everything recorded so far.
func (r *RecordingAudit) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// PurgeHook removes an edition's own rows for an organization inside
// core's purge transaction, after core disabled its protective triggers
// and before it deletes the organization row. The hook disables and
// re-enables any protective trigger of its own.
type PurgeHook interface {
	PurgeOrganization(ctx context.Context, tx Execer, organizationID domain.ID) error
}
