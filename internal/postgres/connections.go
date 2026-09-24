package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

func (s *Store) UpsertDirectoryConnection(ctx context.Context, input application.UpsertDirectoryConnectionInput) (domain.ID, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO directory_connections
        (id, organization_id, adapter_key, provider_resource_id, state, reconciliation_status, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, 'pending', $6, $6)
        ON CONFLICT (adapter_key, provider_resource_id)
        DO UPDATE SET state = EXCLUDED.state, updated_at = EXCLUDED.updated_at`,
		uuid.NewString(), input.OrganizationID, input.AdapterKey, input.ProviderResourceID,
		input.State, input.Now); err != nil {
		return "", err
	}
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id::text FROM directory_connections
        WHERE adapter_key = $1 AND provider_resource_id = $2`,
		input.AdapterKey, input.ProviderResourceID).Scan(&id)
	return domain.ID(id), err
}

// ListDirectoryOrganizations lists organizations with at least one
// directory connection; the reconciliation sweep iterates them.
func (s *Store) ListDirectoryOrganizations(ctx context.Context) ([]domain.ID, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT organization_id::text FROM directory_connections
        WHERE state = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	organizations := make([]domain.ID, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		organizations = append(organizations, domain.ID(id))
	}
	return organizations, rows.Err()
}

func (s *Store) UpdateDirectoryReconciliation(ctx context.Context, input application.UpdateDirectoryReconciliationInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	var lastSuccess any
	if input.Status == "healthy" {
		lastSuccess = input.Now
	}
	_, err := s.pool.Exec(ctx, `UPDATE directory_connections
        SET reconciliation_status = $3,
            last_successful_sync_at = COALESCE($4, last_successful_sync_at),
            updated_at = $5
        WHERE organization_id = $1 AND adapter_key = $2`,
		input.OrganizationID, input.AdapterKey, input.Status, lastSuccess, input.Now)
	return err
}

// GetDirectorySyncState reads the most recently synced active directory
// connection. The pending row a setup link mints is skipped: it carries
// no directory of its own.
func (s *Store) GetDirectorySyncState(ctx context.Context, input application.GetDirectorySyncStateInput) (application.DirectorySyncState, error) {
	if err := input.Validate(); err != nil {
		return application.DirectorySyncState{}, err
	}
	var state application.DirectorySyncState
	err := s.pool.QueryRow(ctx, `SELECT reconciliation_status, last_successful_sync_at FROM directory_connections
        WHERE organization_id = $1 AND adapter_key = $2 AND state = 'active'
        ORDER BY last_successful_sync_at DESC NULLS LAST, updated_at DESC
        LIMIT 1`, input.OrganizationID, input.AdapterKey).Scan(&state.ReconciliationStatus, &state.LastSuccessfulSyncAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.DirectorySyncState{}, nil
	}
	return state, err
}
