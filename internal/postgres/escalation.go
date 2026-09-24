package postgres

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/routing"
)

// LoadEscalationProgram reads the pinned path revision and the pinned
// team policy revisions the executor runs for one request. Only the
// organization half decides whether the request is escalated at all: a
// default queue snapshot never carries team pins.
func (s *Store) LoadEscalationProgram(ctx context.Context, input application.LoadEscalationProgramInput) (application.EscalationProgramRecord, error) {
	if err := input.Validate(); err != nil {
		return application.EscalationProgramRecord{}, err
	}
	pinned, escalated, err := readPinnedRouting(ctx, s.pool, input.OrganizationID, input.RequestID)
	if err != nil {
		return application.EscalationProgramRecord{}, err
	}
	if !escalated {
		return application.EscalationProgramRecord{Escalated: false}, nil
	}
	record := application.EscalationProgramRecord{
		Escalated: true, Urgency: pinned.urgency,
		PathDocument: pinned.document, PathDocumentVersion: pinned.documentVersion,
		TeamPolicies: make([]application.PinnedTeamPolicyDocument, 0),
	}
	if record.Facts, err = decodeRoutingFacts(pinned.facts); err != nil {
		return application.EscalationProgramRecord{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT tp.team_id::text, p.document, p.document_version
		FROM approval_request_routing_team_policies tp
		JOIN team_escalation_policies p ON p.id = tp.policy_revision_id AND p.organization_id = tp.organization_id
		WHERE tp.organization_id = $1 AND tp.request_id = $2
		ORDER BY tp.team_id`, input.OrganizationID, input.RequestID)
	if err != nil {
		return application.EscalationProgramRecord{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var policy application.PinnedTeamPolicyDocument
		var teamID string
		if err := rows.Scan(&teamID, &policy.Document, &policy.DocumentVersion); err != nil {
			return application.EscalationProgramRecord{}, err
		}
		policy.TeamID = domain.ID(teamID)
		record.TeamPolicies = append(record.TeamPolicies, policy)
	}
	return record, rows.Err()
}

// RecordEscalationProgress rewrites the request's escalation position in
// one transaction. The state upsert carries both guards (a newer sequence
// and a still-pending request), so a stale retry or a tick that raced a
// decision changes nothing and writes no audit event either.
func (s *Store) RecordEscalationProgress(ctx context.Context, input application.RecordEscalationProgressInput) (application.EscalationProgressResult, error) {
	if err := input.Validate(); err != nil {
		return application.EscalationProgressResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return application.EscalationProgressResult{}, err
	}
	defer tx.Rollback(ctx)
	// Locking the request row, as a decision does, makes the pending check
	// and the writes below atomic against a decision committing at the
	// same moment, and serializes two ticks racing for the same request
	// instead of letting them deadlock on the updated_at touch.
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM approval_requests
		WHERE organization_id = $1 AND id = $2 FOR UPDATE`, input.OrganizationID, input.RequestID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.EscalationProgressResult{}, domain.ErrNotFound
	}
	if err != nil {
		return application.EscalationProgressResult{}, err
	}
	if domain.ApprovalStatus(status) != domain.ApprovalPending {
		return application.EscalationProgressResult{Applied: false}, nil
	}
	progress := input.Progress
	organization := progress.Organization
	tag, err := tx.Exec(ctx, `INSERT INTO approval_request_escalation_state
		(request_id, organization_id, sequence, phase, urgency, current_node_id,
		 level_ordinal, level_count, repeat, next_at, updated_at)
		SELECT $1, $2, $3, $4, $5, NULLIF($6, '')::uuid, $7, $8, $9, $10, $11
		WHERE EXISTS (SELECT 1 FROM approval_requests r WHERE r.id = $1 AND r.status = 'pending')
		ON CONFLICT (request_id) DO UPDATE SET
		    sequence = EXCLUDED.sequence, phase = EXCLUDED.phase, urgency = EXCLUDED.urgency,
		    current_node_id = EXCLUDED.current_node_id, level_ordinal = EXCLUDED.level_ordinal,
		    level_count = EXCLUDED.level_count, repeat = EXCLUDED.repeat,
		    next_at = EXCLUDED.next_at, updated_at = EXCLUDED.updated_at
		WHERE approval_request_escalation_state.sequence < EXCLUDED.sequence
		  AND EXISTS (SELECT 1 FROM approval_requests r WHERE r.id = EXCLUDED.request_id AND r.status = 'pending')`,
		input.RequestID, input.OrganizationID, progress.Sequence, string(organization.Phase),
		string(progress.Urgency), organization.CurrentNodeID, organization.LevelOrdinal,
		organization.LevelCount, organization.Repeat, nullableTime(organization.NextAt), input.Now)
	if err != nil {
		return application.EscalationProgressResult{}, err
	}
	if tag.RowsAffected() == 0 {
		return application.EscalationProgressResult{Applied: false}, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM approval_request_escalation_team_state
		WHERE organization_id = $1 AND request_id = $2`, input.OrganizationID, input.RequestID); err != nil {
		return application.EscalationProgressResult{}, err
	}
	for _, team := range progress.Teams {
		if _, err := tx.Exec(ctx, `INSERT INTO approval_request_escalation_team_state
			(request_id, organization_id, team_id, phase, current_node_id,
			 level_ordinal, level_count, repeat, next_at, updated_at)
			VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid, $6, $7, $8, $9, $10)`,
			input.RequestID, input.OrganizationID, team.TeamID, string(team.Phase), team.CurrentNodeID,
			team.LevelOrdinal, team.LevelCount, team.Repeat, nullableTime(team.NextAt), input.Now); err != nil {
			return application.EscalationProgressResult{}, err
		}
	}
	// Touching the request row is what the product change stream watches,
	// so the request page refreshes its escalation panel after a tick.
	if _, err := tx.Exec(ctx, `UPDATE approval_requests SET updated_at = $3
		WHERE organization_id = $1 AND id = $2`, input.OrganizationID, input.RequestID, input.Now); err != nil {
		return application.EscalationProgressResult{}, err
	}
	for _, event := range input.Events {
		if event.OrganizationID == "" {
			event.OrganizationID = input.OrganizationID
		}
		if event.SubjectType == "" {
			event.SubjectType = "approval_request"
		}
		if event.SubjectID == "" {
			event.SubjectID = input.RequestID
		}
		if event.OccurredAt.IsZero() {
			event.OccurredAt = input.Now
		}
		if err := s.appendAuditEvent(ctx, tx, event); err != nil {
			return application.EscalationProgressResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return application.EscalationProgressResult{}, err
	}
	return application.EscalationProgressResult{Applied: true}, nil
}

// readEscalationProgress reads the executor's position for one request;
// nil before the first tick or for a request that never escalated.
func readEscalationProgress(ctx context.Context, q querier, organizationID, requestID domain.ID) (*application.EscalationProgress, error) {
	var progress application.EscalationProgress
	var phase, urgency string
	var nodeID *string
	var nextAt *time.Time
	err := q.QueryRow(ctx, `SELECT sequence, phase, urgency, current_node_id::text, level_ordinal, level_count, repeat, next_at, updated_at
		FROM approval_request_escalation_state WHERE organization_id = $1 AND request_id = $2`,
		organizationID, requestID).Scan(&progress.Sequence, &phase, &urgency, &nodeID,
		&progress.Organization.LevelOrdinal, &progress.Organization.LevelCount, &progress.Organization.Repeat,
		&nextAt, &progress.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	progress.Urgency = routing.Urgency(urgency)
	progress.Organization.Phase = application.EscalationPhase(phase)
	progress.Organization.CurrentNodeID = stringValue(nodeID)
	progress.Organization.NextAt = timeValue(nextAt)
	progress.Teams = make([]application.EscalationWalkerProgress, 0)
	rows, err := q.Query(ctx, `SELECT team_id::text, phase, current_node_id::text, level_ordinal, level_count, repeat, next_at
		FROM approval_request_escalation_team_state WHERE organization_id = $1 AND request_id = $2
		ORDER BY team_id`, organizationID, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var walker application.EscalationWalkerProgress
		var teamID, phase string
		var nodeID *string
		var nextAt *time.Time
		if err := rows.Scan(&teamID, &phase, &nodeID, &walker.LevelOrdinal, &walker.LevelCount, &walker.Repeat, &nextAt); err != nil {
			return nil, err
		}
		walker.TeamID = domain.ID(teamID)
		walker.Phase = application.EscalationPhase(phase)
		walker.CurrentNodeID = stringValue(nodeID)
		walker.NextAt = timeValue(nextAt)
		progress.Teams = append(progress.Teams, walker)
	}
	return &progress, rows.Err()
}

// ResolveLevelRecipients expands the levels one executor tick entered into
// people. An organization level reaches its user targets and every current
// member of its team targets. A team policy level reaches its user targets
// only while they are still on that team, and its broadcast reaches every
// current member: leaving the team is how someone stops being paged by
// its policy, so a stale user target is dropped rather than honoured.
func (s *Store) ResolveLevelRecipients(ctx context.Context, input application.ResolveLevelRecipientsInput) (application.ApprovalRecipients, error) {
	if err := input.Validate(); err != nil {
		return application.ApprovalRecipients{}, err
	}
	recipients := application.ApprovalRecipients{
		Outcome: domain.RoutingEscalated, Urgency: input.Urgency,
		Recipients: make([]application.NotificationRecipient, 0),
	}
	userIDs, teamIDs := make([]string, 0), make([]string, 0)
	scopedUserIDs, scopedTeamIDs := make([]string, 0), make([]string, 0)
	for _, entry := range input.Entries {
		for _, target := range entry.Targets {
			switch {
			case entry.TeamID == "" && target.Type == routing.TargetUser:
				userIDs = append(userIDs, target.ID)
			case entry.TeamID == "" && target.Type == routing.TargetTeam:
				teamIDs = append(teamIDs, target.ID)
			case target.Type == routing.TargetUser:
				scopedUserIDs = append(scopedUserIDs, target.ID)
				scopedTeamIDs = append(scopedTeamIDs, string(entry.TeamID))
			case target.Type == routing.TargetBroadcast:
				teamIDs = append(teamIDs, string(entry.TeamID))
			}
		}
	}
	if len(scopedUserIDs) > 0 {
		kept, err := s.currentTeamMembersAmong(ctx, input.OrganizationID, scopedUserIDs, scopedTeamIDs)
		if err != nil {
			return application.ApprovalRecipients{}, err
		}
		userIDs = append(userIDs, kept...)
	}
	if len(userIDs) == 0 && len(teamIDs) == 0 {
		return recipients, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT m.id::text, u.id::text, u.display_name, `+recipientAddressesSQL+`, `+recipientSlackSQL+`, `+recipientDiscordSQL+`
        FROM memberships m JOIN users u ON u.id = m.user_id
        WHERE m.organization_id = $1 AND m.status = 'active' AND u.status = 'active'
        AND (m.id = ANY($2::uuid[]) OR m.id IN (
            SELECT mt.membership_id FROM active_membership_teams mt
            WHERE mt.organization_id = $1 AND mt.team_id = ANY($3::uuid[])))
        ORDER BY u.display_name`, input.OrganizationID, userIDs, teamIDs)
	if err != nil {
		return application.ApprovalRecipients{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var membershipID, userID, displayName, slackUserID, discordUserID string
		var addresses []string
		if err := rows.Scan(&membershipID, &userID, &displayName, &addresses, &slackUserID, &discordUserID); err != nil {
			return application.ApprovalRecipients{}, err
		}
		recipients.Recipients = append(recipients.Recipients, application.NotificationRecipient{
			MembershipID: domain.ID(membershipID), UserID: domain.ID(userID), DisplayName: displayName,
			Emails: addresses, SlackUserID: slackUserID, DiscordUserID: discordUserID,
		})
	}
	if err := rows.Err(); err != nil {
		return application.ApprovalRecipients{}, err
	}
	return recipients, s.attachRecipientMethods(ctx, input.OrganizationID, recipients.Recipients)
}

// currentTeamMembersAmong keeps the (membership, team) pairs whose
// membership currently sits on that team and reports the rest, which a
// team policy still names but may no longer reach.
func (s *Store) currentTeamMembersAmong(ctx context.Context, organizationID domain.ID, membershipIDs, teamIDs []string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT scoped.membership_id::text, scoped.team_id::text,
		EXISTS (SELECT 1 FROM active_membership_teams mt
		    WHERE mt.organization_id = $1 AND mt.team_id = scoped.team_id AND mt.membership_id = scoped.membership_id)
		FROM unnest($2::uuid[], $3::uuid[]) AS scoped(membership_id, team_id)`,
		organizationID, membershipIDs, teamIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	kept := make([]string, 0, len(membershipIDs))
	for rows.Next() {
		var membershipID, teamID string
		var member bool
		if err := rows.Scan(&membershipID, &teamID, &member); err != nil {
			return nil, err
		}
		if !member {
			// The store carries no logger of its own; the process default
			// is the one the composition root configured.
			slog.Default().WarnContext(ctx, "team policy level names a membership no longer on the team; skipped",
				"membership_id", membershipID, "team_id", teamID)
			continue
		}
		kept = append(kept, membershipID)
	}
	return kept, rows.Err()
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
