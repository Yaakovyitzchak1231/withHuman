package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/domain"
)

// managementActorID validates a provenance reference without resolving it to
// a user: resource foreign keys retain the member or key that performed it.
func managementActorID(ctx context.Context, q querier, orgID, id domain.ID) (domain.ID, error) {
	var found domain.ID
	err := q.QueryRow(ctx, `SELECT id FROM management_actors WHERE organization_id=$1 AND id=$2`, orgID, id).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	return found, err
}

// appendManagementAuditEvent resolves provenance into the actual audit actor.
// Callers pass a member/key reference, never infer a human from an empty ID.
func (s *Store) appendManagementAuditEvent(ctx context.Context, q querier, event domain.AuditEvent) error {
	if event.ActorType == domain.ActorHuman {
		err := q.QueryRow(ctx, `SELECT CASE WHEN a.api_key_id IS NULL THEN 'human' ELSE 'api_key' END, COALESCE(a.api_key_id,m.user_id)
    FROM management_actors a LEFT JOIN memberships m ON m.id=a.membership_id AND m.organization_id=a.organization_id
    WHERE a.organization_id=$1 AND a.id=$2`, event.OrganizationID, event.ActorID).Scan(&event.ActorType, &event.ActorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
	}
	return s.appendAuditEvent(ctx, q, event)
}
