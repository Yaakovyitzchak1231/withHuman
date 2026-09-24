package postgres

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// RecordApprovalNotifications remembers that the request was brought to
// these memberships' attention. A membership already recorded keeps its
// first time, so a retried activity, a repeated path, or a later level
// naming the same team changes nothing. Memberships the organization does
// not hold are skipped rather than refused: the plan resolved them a
// moment ago, and a member deactivated since should not fail the page for
// everyone else.
func (s *Store) RecordApprovalNotifications(ctx context.Context, input application.RecordApprovalNotificationsInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	return recordApprovalNotifications(ctx, s.pool, input.OrganizationID, input.RequestID, input.MembershipIDs, input.Now)
}

func recordApprovalNotifications(ctx context.Context, q querier, organizationID, requestID domain.ID, membershipIDs []domain.ID, now time.Time) error {
	if len(membershipIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO approval_request_notifications (request_id, organization_id, membership_id, notified_at)
		SELECT $1, $2, m.id, $4 FROM memberships m
		WHERE m.organization_id = $2 AND m.id = ANY($3::uuid[])
		ON CONFLICT (request_id, membership_id) DO NOTHING`,
		requestID, organizationID, idStrings(membershipIDs), now)
	return err
}
