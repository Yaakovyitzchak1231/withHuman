package postgres

import (
	"context"
	"testing"

	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
)

// The open edition keeps no audit log, so these tests attach a sink of
// their own: a table in the test database, written inside the same
// transaction as the record it describes, which is exactly what a
// keeping edition's sink does. A rolled-back write leaves no event, so
// the assertions see what such an edition would have stored and nothing
// more. The table mirrors the columns every keeping edition needs.
type tableAudit struct{}

func (tableAudit) Append(ctx context.Context, tx db.Execer, event domain.AuditEvent) error {
	_, err := tx.Exec(ctx, `INSERT INTO test_audit_events
		(id, organization_id, event_type, actor_type, actor_id, subject_type, subject_id, data, occurred_at)
		VALUES ($1, $2, $3, $4, NULLIF($5::text, '')::uuid, $6, $7, $8, $9)`,
		event.ID, event.OrganizationID, event.EventType, event.ActorType, event.ActorID,
		event.SubjectType, event.SubjectID, event.Data, event.OccurredAt)
	return err
}

// recordAudit gives the store the table sink, creating the table in the
// test database because no migration knows it.
func recordAudit(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.Pool().Exec(context.Background(), `CREATE TABLE IF NOT EXISTS test_audit_events (
		sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		id uuid NOT NULL UNIQUE,
		organization_id uuid NOT NULL,
		event_type text NOT NULL,
		actor_type text NOT NULL,
		actor_id uuid,
		subject_type text NOT NULL,
		subject_id uuid NOT NULL,
		data jsonb NOT NULL,
		occurred_at timestamptz NOT NULL
	)`); err != nil {
		t.Fatalf("create the test audit table: %v", err)
	}
	store.WithAuditSink(tableAudit{})
}

// auditEvents lists the recorded events matching the WHERE clause, newest
// first, the order a keeping edition's log shows them in.
func auditEvents(t *testing.T, ctx context.Context, store *Store, where string, args ...any) []domain.AuditEvent {
	t.Helper()
	rows, err := store.Pool().Query(ctx, `SELECT sequence, id::text, organization_id::text, event_type, actor_type,
		COALESCE(actor_id::text, ''), subject_type, subject_id::text, data, occurred_at
		FROM test_audit_events WHERE `+where+` ORDER BY sequence DESC`, args...)
	if err != nil {
		t.Fatalf("list recorded audit events: %v", err)
	}
	defer rows.Close()
	var events []domain.AuditEvent
	for rows.Next() {
		var event domain.AuditEvent
		var id, organizationID, actorID, subjectID string
		if err := rows.Scan(&event.Sequence, &id, &organizationID, &event.EventType, &event.ActorType,
			&actorID, &event.SubjectType, &subjectID, &event.Data, &event.OccurredAt); err != nil {
			t.Fatalf("scan recorded audit event: %v", err)
		}
		event.ID, event.OrganizationID, event.ActorID, event.SubjectID = domain.ID(id), domain.ID(organizationID), domain.ID(actorID), domain.ID(subjectID)
		events = append(events, event)
	}
	return events
}
