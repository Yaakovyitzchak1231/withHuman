package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// ListLoginOrganizations resolves an email to the user, whether a local
// password exists, and every active membership in an active organization.
// The email-first login flow partitions the organizations by SSO
// requirement; suspended organizations and memberships never appear.
func (s *Store) ListLoginOrganizations(ctx context.Context, input application.ListLoginOrganizationsInput) (application.LoginOrganizations, error) {
	if err := input.Validate(); err != nil {
		return application.LoginOrganizations{}, err
	}
	var result application.LoginOrganizations
	var userID string
	err := s.pool.QueryRow(ctx, `SELECT u.id::text,
        EXISTS (SELECT 1 FROM local_credentials c WHERE c.user_id = u.id)
        FROM users u WHERE lower(u.primary_email) = lower($1) AND u.status = 'active'`,
		input.Email).Scan(&userID, &result.HasPassword)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.LoginOrganizations{}, nil
	}
	if err != nil {
		return application.LoginOrganizations{}, err
	}
	result.UserID = domain.ID(userID)
	rows, err := s.pool.Query(ctx, `SELECT o.id::text, o.slug, o.name, o.created_at, o.updated_at
        FROM memberships m JOIN organizations o ON o.id = m.organization_id
        WHERE m.user_id = $1 AND m.status = 'active' AND o.status = 'active'
        ORDER BY o.name, o.slug`, userID)
	if err != nil {
		return application.LoginOrganizations{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var organization domain.Organization
		var id string
		if err := rows.Scan(&id, &organization.Slug, &organization.Name,
			&organization.CreatedAt, &organization.UpdatedAt); err != nil {
			return application.LoginOrganizations{}, err
		}
		organization.ID, organization.Status = domain.ID(id), domain.OrganizationActive
		result.Organizations = append(result.Organizations, organization)
	}
	return result, rows.Err()
}

func (s *Store) CreateLoginTicket(ctx context.Context, input application.CreateLoginTicketInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	client := input.Client.Normalized()
	_, err := s.pool.Exec(ctx, `INSERT INTO login_tickets
        (id, user_id, secret_hash, auth_method, assurance, issued_at, expires_at, client, client_context)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		input.TicketID, input.UserID, input.SecretHash, input.AuthMethod, input.Assurance, input.IssuedAt, input.ExpiresAt, client.Kind, client.Context)
	return err
}

// ConsumeLoginTicket redeems a ticket exactly once: the UPDATE only
// matches an unconsumed, unexpired ticket, so a replay or an expired
// ticket surfaces as unauthenticated.
func (s *Store) ConsumeLoginTicket(ctx context.Context, input application.ConsumeLoginTicketInput) (application.LoginTicketRecord, error) {
	if err := input.Validate(); err != nil {
		return application.LoginTicketRecord{}, err
	}
	var record application.LoginTicketRecord
	var userID string
	err := s.pool.QueryRow(ctx, `UPDATE login_tickets t SET consumed_at = $3
        FROM users u
        WHERE t.id = $1 AND t.secret_hash = $2 AND t.consumed_at IS NULL AND t.expires_at > $3
          AND u.id = t.user_id AND u.status = 'active'
        RETURNING t.user_id::text, t.auth_method, t.assurance, t.issued_at, t.client, t.client_context`,
		input.TicketID, input.SecretHash, input.Now).Scan(&userID, &record.AuthMethod, &record.Assurance, &record.IssuedAt, &record.Client.Kind, &record.Client.Context)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.LoginTicketRecord{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return application.LoginTicketRecord{}, err
	}
	record.UserID = domain.ID(userID)
	return record, nil
}
