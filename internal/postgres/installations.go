package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// ClaimHeartbeat stamps the installation's claim when the last one is a
// full interval old or reported another version, answering the
// installation ID when this caller won the send. A concurrent replica's
// update waits on the row lock and then sees the fresh claim.
func (s *Store) ClaimHeartbeat(ctx context.Context, input application.ClaimHeartbeatInput) (domain.ID, bool, error) {
	if err := input.Validate(); err != nil {
		return "", false, err
	}
	rows, err := s.pool.Query(ctx, `UPDATE installations
		   SET heartbeat_claimed_at = $1::timestamptz, heartbeat_version = $2
		 WHERE heartbeat_claimed_at IS NULL
		    OR heartbeat_claimed_at <= $1::timestamptz - make_interval(secs => $3)
		    OR heartbeat_version IS DISTINCT FROM $2
		RETURNING id::text`, input.Now, input.Version, application.HeartbeatInterval.Seconds())
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var id domain.ID
	claimed := false
	for rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return "", false, err
		}
		claimed = true
	}
	return id, claimed, rows.Err()
}

// RecordHeartbeatAnswer keeps the release service's latest answer.
func (s *Store) RecordHeartbeatAnswer(ctx context.Context, input application.RecordHeartbeatAnswerInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	advisories := input.Advisories
	if advisories == nil {
		advisories = []domain.SecurityAdvisory{}
	}
	encoded, err := json.Marshal(advisories)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE installations SET checked_at = $1, latest_version = $2, advisories = $3`,
		input.CheckedAt, input.LatestVersion, encoded)
	return err
}

// GetInstallation reads the deployment's installation row.
func (s *Store) GetInstallation(ctx context.Context) (domain.Installation, error) {
	var installation domain.Installation
	var checkedAt *time.Time
	var latestVersion *string
	var advisories []byte
	if err := s.pool.QueryRow(ctx, `SELECT id::text, created_at, checked_at, latest_version, advisories FROM installations`).
		Scan(&installation.ID, &installation.CreatedAt, &checkedAt, &latestVersion, &advisories); err != nil {
		return domain.Installation{}, err
	}
	if checkedAt != nil {
		installation.CheckedAt = *checkedAt
	}
	if latestVersion != nil {
		installation.LatestVersion = *latestVersion
	}
	if err := json.Unmarshal(advisories, &installation.Advisories); err != nil {
		return domain.Installation{}, err
	}
	return installation, nil
}
