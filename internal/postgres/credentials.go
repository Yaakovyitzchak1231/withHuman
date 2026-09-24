package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// Password credentials and reset links. A person has at most one local
// credential (the primary key is the user); a reset is a single-use,
// hashed, expiring row that only an active user can redeem.

func (s *Store) SetLocalCredential(ctx context.Context, input application.SetLocalCredentialInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO local_credentials (user_id, password_hash, created_at, updated_at)
        VALUES ($1, $2, $3, $3)
        ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash, updated_at = EXCLUDED.updated_at`,
		input.UserID, input.PasswordHash, input.Now)
	return err
}

func (s *Store) RevokeUserSessions(ctx context.Context, input application.RevokeUserSessionsInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = $2
        WHERE user_id = $1 AND revoked_at IS NULL AND ($3::text = '' OR id::text <> $3::text)`,
		input.UserID, input.Now, string(input.ExceptSessionID))
	return err
}

func (s *Store) CreatePasswordReset(ctx context.Context, input application.CreatePasswordResetInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO password_resets (id, user_id, secret_hash, issued_at, expires_at)
        VALUES ($1, $2, $3, $4, $5)`,
		input.ResetID, input.UserID, input.SecretHash, input.IssuedAt, input.ExpiresAt)
	return err
}

func (s *Store) GetPasswordReset(ctx context.Context, input application.GetPasswordResetInput) (application.PasswordResetRecord, error) {
	if err := input.Validate(); err != nil {
		return application.PasswordResetRecord{}, err
	}
	var record application.PasswordResetRecord
	var userID string
	err := s.pool.QueryRow(ctx, `SELECT r.user_id::text, r.expires_at
        FROM password_resets r JOIN users u ON u.id = r.user_id AND u.status = 'active'
        WHERE r.id = $1 AND r.secret_hash = $2 AND r.consumed_at IS NULL AND r.expires_at > $3`,
		input.ResetID, input.SecretHash, input.Now).Scan(&userID, &record.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PasswordResetRecord{}, domain.ErrNotFound
	}
	if err != nil {
		return application.PasswordResetRecord{}, err
	}
	record.UserID = domain.ID(userID)
	return record, nil
}

func (s *Store) ConsumePasswordReset(ctx context.Context, input application.ConsumePasswordResetInput) (application.PasswordResetRecord, error) {
	if err := input.Validate(); err != nil {
		return application.PasswordResetRecord{}, err
	}
	var record application.PasswordResetRecord
	var userID string
	err := s.pool.QueryRow(ctx, `UPDATE password_resets r SET consumed_at = $3
        FROM users u
        WHERE r.id = $1 AND r.secret_hash = $2 AND r.consumed_at IS NULL AND r.expires_at > $3
          AND u.id = r.user_id AND u.status = 'active'
        RETURNING r.user_id::text, r.expires_at`,
		input.ResetID, input.SecretHash, input.Now).Scan(&userID, &record.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PasswordResetRecord{}, domain.ErrNotFound
	}
	if err != nil {
		return application.PasswordResetRecord{}, err
	}
	record.UserID = domain.ID(userID)
	return record, nil
}
