package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

func (s *Store) CreateSession(ctx context.Context, input application.CreateSessionInput) (domain.Principal, error) {
	if err := input.Validate(); err != nil {
		return domain.Principal{}, err
	}
	sessionID := domain.ID(uuid.NewString())
	client := input.Client.Normalized()
	_, err := s.pool.Exec(ctx, `INSERT INTO sessions
        (id, user_id, organization_id, secret_hash, auth_method, assurance, authenticated_at, issued_at, expires_at, client, client_context)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, sessionID, input.UserID, input.OrganizationID,
		input.SecretHash, input.Auth.Method, input.Auth.Assurance, input.Auth.AuthenticatedAt, input.IssuedAt, input.ExpiresAt,
		client.Kind, client.Context)
	if err != nil {
		return domain.Principal{}, err
	}
	principal, err := s.resolveHuman(ctx, `s.id = $1`, sessionID, input.IssuedAt)
	if err != nil {
		return domain.Principal{}, err
	}
	return principal, nil
}

func (s *Store) ResolveSession(ctx context.Context, input application.ResolveSessionInput) (domain.Principal, error) {
	if err := input.Validate(); err != nil {
		return domain.Principal{}, err
	}
	return s.resolveHuman(ctx, `s.secret_hash = $1`, input.SecretHash, input.Now)
}

func (s *Store) resolveHuman(ctx context.Context, predicate string, value any, now time.Time) (domain.Principal, error) {
	query := `SELECT u.id::text, s.organization_id::text, m.id::text, m.status,
        s.id::text, s.auth_method, s.assurance, s.authenticated_at,
        COALESCE((SELECT array_agg(DISTINCT team_id::text ORDER BY team_id::text) FROM active_membership_teams WHERE membership_id = m.id), '{}')
      FROM sessions s
      JOIN users u ON u.id = s.user_id
      JOIN organizations o ON o.id = s.organization_id
      JOIN memberships m ON m.user_id = s.user_id AND m.organization_id = s.organization_id
     WHERE ` + predicate + ` AND s.revoked_at IS NULL AND s.expires_at > $2
       AND u.status = 'active' AND o.status = 'active'`
	var actorID, organizationID, membershipID, membershipStatus, sessionID string
	var method, assurance string
	var authenticatedAt time.Time
	var teamIDs []string
	err := s.pool.QueryRow(ctx, query, value, now).Scan(
		&actorID, &organizationID, &membershipID, &membershipStatus, &sessionID,
		&method, &assurance, &authenticatedAt, &teamIDs,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.Principal{}, err
	}
	if domain.MembershipStatus(membershipStatus) != domain.MembershipActive {
		return domain.Principal{}, domain.ErrMembershipInactive
	}
	grants, err := loadGrants(ctx, s.pool, domain.ID(organizationID), domain.PrincipalMembership, domain.ID(membershipID))
	if err != nil {
		return domain.Principal{}, err
	}
	teams := make([]domain.ID, len(teamIDs))
	for index, id := range teamIDs {
		teams[index] = domain.ID(id)
	}
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: domain.ID(actorID), OrganizationID: domain.ID(organizationID),
		MembershipID: domain.ID(membershipID),
		TeamIDs:      teams,
		Auth:         domain.AuthContext{Method: method, Assurance: assurance, AuthenticatedAt: authenticatedAt, SessionID: domain.ID(sessionID)},
	}.WithGrants(grants), nil
}

func (s *Store) RevokeSession(ctx context.Context, input application.RevokeSessionInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	command, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, input.SessionID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) GetLocalCredential(ctx context.Context, input application.GetLocalCredentialInput) (application.LocalCredentialRecord, error) {
	if err := input.Validate(); err != nil {
		return application.LocalCredentialRecord{}, err
	}
	var record application.LocalCredentialRecord
	var userID string
	err := s.pool.QueryRow(ctx, `SELECT u.id::text, c.password_hash
        FROM users u JOIN local_credentials c ON c.user_id = u.id
        WHERE lower(u.primary_email) = lower($1) AND u.status = 'active'`, input.Email).Scan(&userID, &record.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.LocalCredentialRecord{}, domain.ErrNotFound
	}
	if err != nil {
		return application.LocalCredentialRecord{}, err
	}
	record.UserID = domain.ID(userID)
	return record, nil
}

func (s *Store) FindMembershipForLogin(ctx context.Context, input application.FindMembershipForLoginInput) (domain.Membership, error) {
	if err := input.Validate(); err != nil {
		return domain.Membership{}, err
	}
	var membership domain.Membership
	var id, organizationID, status, source string
	var teams []string
	err := s.pool.QueryRow(ctx, `SELECT m.id::text, m.organization_id::text, m.status, m.admission_source,
        m.directory_managed, m.created_at, m.updated_at,
        COALESCE((SELECT array_agg(DISTINCT team_id::text) FROM active_membership_teams WHERE membership_id = m.id), '{}')
      FROM memberships m JOIN organizations o ON o.id = m.organization_id
     WHERE m.user_id = $1 AND o.id = $2 AND o.status = 'active'`, input.UserID, input.OrganizationID).Scan(
		&id, &organizationID, &status, &source, &membership.DirectoryManaged,
		&membership.CreatedAt, &membership.UpdatedAt, &teams,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Membership{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Membership{}, err
	}
	membership.ID, membership.UserID, membership.OrganizationID = domain.ID(id), input.UserID, domain.ID(organizationID)
	membership.Status, membership.AdmissionSource = domain.MembershipStatus(status), domain.AdmissionSource(source)
	for _, team := range teams {
		membership.TeamIDs = append(membership.TeamIDs, domain.ID(team))
	}
	assignments, err := listAssignments(ctx, s.pool, membership.OrganizationID, domain.PrincipalMembership, membership.ID)
	if err != nil {
		return domain.Membership{}, err
	}
	membership.Assignments = assignments
	return membership, nil
}
