package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/toolpresentation"
)

const toolPresentationSelect = `SELECT id::text, organization_id::text, COALESCE(runtime, ''), COALESCE(server, ''), tool, spec, created_at, updated_at
    FROM tool_presentations `

func scanToolPresentation(row rowScanner) (domain.ToolPresentation, error) {
	var record domain.ToolPresentation
	var id, orgID string
	var spec []byte
	if err := row.Scan(&id, &orgID, &record.Entry.Runtime, &record.Entry.Server, &record.Entry.Tool, &spec, &record.CreatedAt, &record.UpdatedAt); err != nil {
		return domain.ToolPresentation{}, err
	}
	if err := json.Unmarshal(spec, &record.Entry.Spec); err != nil {
		return domain.ToolPresentation{}, err
	}
	record.ID, record.OrganizationID = domain.ID(id), domain.ID(orgID)
	return record, nil
}

func (s *Store) ListToolPresentations(ctx context.Context, input application.ListToolPresentationsInput) ([]domain.ToolPresentation, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, toolPresentationSelect+`WHERE organization_id = $1 ORDER BY tool, COALESCE(server, ''), COALESCE(runtime, ''), created_at`, input.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]domain.ToolPresentation, 0)
	for rows.Next() {
		record, err := scanToolPresentation(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) GetToolPresentation(ctx context.Context, input application.GetToolPresentationInput) (domain.ToolPresentation, error) {
	if err := input.Validate(); err != nil {
		return domain.ToolPresentation{}, err
	}
	record, err := scanToolPresentation(s.pool.QueryRow(ctx, toolPresentationSelect+`WHERE organization_id = $1 AND id = $2`, input.OrganizationID, input.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ToolPresentation{}, domain.ErrNotFound
	}
	return record, err
}

// bindingColumns maps the entry's empty runtime and server to NULL, the
// column's spelling of "any".
func bindingColumns(entry toolpresentation.Entry) (runtime, server *string) {
	if entry.Runtime != "" {
		runtime = &entry.Runtime
	}
	if entry.Server != "" {
		server = &entry.Server
	}
	return runtime, server
}

func (s *Store) CreateToolPresentation(ctx context.Context, input application.CreateToolPresentationInput) (domain.ToolPresentation, error) {
	if err := input.Validate(); err != nil {
		return domain.ToolPresentation{}, err
	}
	spec, err := json.Marshal(input.Entry.Spec.Normalize())
	if err != nil {
		return domain.ToolPresentation{}, err
	}
	runtime, server := bindingColumns(input.Entry)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ToolPresentation{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO tool_presentations (id, organization_id, runtime, server, tool, spec, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
		input.ID, input.OrganizationID, runtime, server, input.Entry.Tool, spec, input.Now)
	if isUniqueViolation(err) {
		return domain.ToolPresentation{}, domain.ErrToolPresentationExists
	}
	if err != nil {
		return domain.ToolPresentation{}, err
	}
	if err := s.auditToolPresentation(ctx, tx, "tool_presentation.created", input.OrganizationID, input.ActorMembershipID, input.ID, input.Entry, input.Now); err != nil {
		return domain.ToolPresentation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ToolPresentation{}, err
	}
	return s.GetToolPresentation(ctx, application.GetToolPresentationInput{OrganizationID: input.OrganizationID, ID: input.ID})
}

func (s *Store) UpdateToolPresentation(ctx context.Context, input application.UpdateToolPresentationInput) (domain.ToolPresentation, error) {
	if err := input.Validate(); err != nil {
		return domain.ToolPresentation{}, err
	}
	spec, err := json.Marshal(input.Entry.Spec.Normalize())
	if err != nil {
		return domain.ToolPresentation{}, err
	}
	runtime, server := bindingColumns(input.Entry)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ToolPresentation{}, err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `UPDATE tool_presentations SET runtime = $3, server = $4, tool = $5, spec = $6, updated_at = $7
		WHERE organization_id = $1 AND id = $2`,
		input.OrganizationID, input.ID, runtime, server, input.Entry.Tool, spec, input.Now)
	if isUniqueViolation(err) {
		return domain.ToolPresentation{}, domain.ErrToolPresentationExists
	}
	if err != nil {
		return domain.ToolPresentation{}, err
	}
	if command.RowsAffected() == 0 {
		return domain.ToolPresentation{}, domain.ErrNotFound
	}
	if err := s.auditToolPresentation(ctx, tx, "tool_presentation.updated", input.OrganizationID, input.ActorMembershipID, input.ID, input.Entry, input.Now); err != nil {
		return domain.ToolPresentation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ToolPresentation{}, err
	}
	return s.GetToolPresentation(ctx, application.GetToolPresentationInput{OrganizationID: input.OrganizationID, ID: input.ID})
}

func (s *Store) DeleteToolPresentation(ctx context.Context, input application.DeleteToolPresentationInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var entry toolpresentation.Entry
	err = tx.QueryRow(ctx, `DELETE FROM tool_presentations WHERE organization_id = $1 AND id = $2
		RETURNING COALESCE(runtime, ''), COALESCE(server, ''), tool`, input.OrganizationID, input.ID).Scan(&entry.Runtime, &entry.Server, &entry.Tool)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := s.auditToolPresentation(ctx, tx, "tool_presentation.deleted", input.OrganizationID, input.ActorMembershipID, input.ID, entry, input.Now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// auditToolPresentation records the binding, never the specification:
// the specification is configuration rather than customer data, but it is
// long, and the row itself is the record of it.
func (s *Store) auditToolPresentation(ctx context.Context, tx execer, eventType string, orgID, actorID, subjectID domain.ID, entry toolpresentation.Entry, now time.Time) error {
	data, err := json.Marshal(map[string]any{"runtime": entry.Runtime, "server": entry.Server, "tool": entry.Tool})
	if err != nil {
		return err
	}
	return s.appendAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: orgID, EventType: eventType,
		ActorType: domain.ActorHuman, ActorID: actorID,
		SubjectType: "tool_presentation", SubjectID: subjectID,
		Data: data, OccurredAt: now,
	})
}
