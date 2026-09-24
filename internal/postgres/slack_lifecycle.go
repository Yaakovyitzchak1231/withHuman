package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

func (s *Store) GetSlackDelivery(ctx context.Context, in application.GetSlackDeliveryInput) (application.SlackDelivery, error) {
	if err := in.Validate(); err != nil {
		return application.SlackDelivery{}, err
	}
	var sealed []byte
	err := s.pool.QueryRow(ctx, `SELECT sc.sealed_bot_token FROM slack_connections sc
 JOIN contact_methods cm ON cm.organization_id = sc.organization_id AND cm.slack_installation_id = sc.installation_id
 JOIN memberships m ON m.user_id = cm.user_id AND m.organization_id = cm.organization_id AND m.status = 'active'
 WHERE sc.organization_id = $1 AND sc.installation_id = $2 AND sc.status = 'connected'
 AND cm.user_id = $3 AND cm.kind = 'slack' AND cm.value = $4 AND cm.verified_at IS NOT NULL`,
		in.OrganizationID, in.InstallationID, in.UserID, in.SlackUserID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SlackDelivery{}, domain.ErrNotFound
	}
	if err != nil {
		return application.SlackDelivery{}, err
	}
	if s.sealer == nil {
		return application.SlackDelivery{}, domain.ErrInvalid
	}
	token, err := s.sealer.Open(slackTokenLabel, sealed)
	return application.SlackDelivery{BotToken: token, InstallationID: in.InstallationID}, err
}

// Session locks span Slack API calls as well as local commits. Use a dedicated
// connection so waiting installers cannot exhaust the pool needed by the holder.
func (s *Store) WithSlackLock(ctx context.Context, in application.SlackLockInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	keys := []string{"slack/org/" + string(in.OrganizationID), "slack/team/" + in.TeamID}
	sort.Strings(keys)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Closing the dedicated session releases its advisory locks even when
		// the request was cancelled while waiting for the second lock.
		_ = conn.Close(cleanup)
	}()
	for _, key := range keys {
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, key); err != nil {
			return err
		}
	}
	return in.Work(ctx)
}

func (s *Store) FindSlackWorkspace(ctx context.Context, in application.FindSlackWorkspaceInput) (domain.SlackConnection, error) {
	if err := in.Validate(); err != nil {
		return domain.SlackConnection{}, err
	}
	var orgID domain.ID
	err := s.pool.QueryRow(ctx, `SELECT organization_id::text FROM slack_connections WHERE team_id = $1 AND status = 'connected'`, in.TeamID).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SlackConnection{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.SlackConnection{}, err
	}
	connection, err := s.GetSlackConnection(ctx, application.GetSlackConnectionInput{OrganizationID: orgID})
	if err == nil && (connection.TeamID != in.TeamID || connection.Status != "connected") {
		return domain.SlackConnection{}, domain.ErrConflict
	}
	return connection, err
}

func (s *Store) DisconnectSlackConnection(ctx context.Context, in application.DisconnectSlackConnectionInput) (bool, error) {
	if err := in.Validate(); err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	changed, err := s.disconnectSlack(ctx, tx, in)
	if err != nil {
		return false, err
	}
	return changed, tx.Commit(ctx)
}

func (s *Store) disconnectSlack(ctx context.Context, tx pgx.Tx, in application.DisconnectSlackConnectionInput) (bool, error) {
	var teamID string
	err := tx.QueryRow(ctx, `UPDATE slack_connections SET status = 'disconnected', sealed_bot_token = NULL,
     disconnected_at = $3, disconnect_reason = $4
     WHERE organization_id = $1 AND installation_id = $2 AND status = 'connected' RETURNING team_id`,
		in.OrganizationID, in.InstallationID, in.Now, in.Reason).Scan(&teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, _ := json.Marshal(map[string]any{"team_id": teamID, "reason": in.Reason})
	err = s.appendAuditEvent(ctx, tx, domain.AuditEvent{OrganizationID: in.OrganizationID,
		EventType: "slack_connection.disconnected", ActorType: domain.ActorSystem,
		SubjectType: "organization", SubjectID: in.OrganizationID, Data: data, OccurredAt: in.Now})
	if err == nil {
		err = queueSlackDisconnection(ctx, tx, in.OrganizationID, in.InstallationID, in.Now)
	}
	return true, err
}

func queueSlackDisconnection(ctx context.Context, tx pgx.Tx, orgID, installationID domain.ID, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO outbox_events (id, organization_id, topic, payload, created_at)
 SELECT gen_random_uuid(), organization_id, 'slack_connection.disconnected',
 jsonb_build_object('request_id',id::text,'installation_id',$2::text),$3
 FROM approval_requests WHERE organization_id=$1 AND status='pending' AND deadline_at>$3`, orgID, installationID, now)
	return err
}

func (s *Store) ListSlackDisconnections(ctx context.Context, in application.ListPendingSlackEventsInput) ([]application.SlackDisconnection, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text,organization_id::text,payload->>'request_id',payload->>'installation_id'
 FROM outbox_events WHERE topic='slack_connection.disconnected' AND published_at IS NULL ORDER BY sequence LIMIT $1`, in.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []application.SlackDisconnection{}
	for rows.Next() {
		var item application.SlackDisconnection
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.RequestID, &item.InstallationID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s *Store) CompleteSlackDisconnection(ctx context.Context, in application.CompleteSlackEventInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE outbox_events SET published_at=$2 WHERE id=$1 AND topic='slack_connection.disconnected' AND published_at IS NULL`, in.ID, in.Now)
	return err
}

func (s *Store) InsertSlackEvent(ctx context.Context, in application.SlackEvent) error {
	if err := in.Validate(); err != nil {
		return err
	}
	if in.BotUserIDs == nil {
		in.BotUserIDs = []string{}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO slack_event_inbox
 (id, app_id, external_event_id, team_id, event_type, bot_user_ids, occurred_at, received_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (app_id, external_event_id) DO NOTHING`,
		uuid.NewString(), in.AppID, in.ExternalEventID, in.TeamID, in.Type, in.BotUserIDs, in.OccurredAt, in.ReceivedAt)
	return err
}
func (s *Store) ListPendingSlackEvents(ctx context.Context, in application.ListPendingSlackEventsInput) ([]application.SlackEvent, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, app_id, external_event_id, team_id, event_type, bot_user_ids, occurred_at, received_at
 FROM slack_event_inbox WHERE applied_at IS NULL ORDER BY received_at LIMIT $1`, in.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []application.SlackEvent{}
	for rows.Next() {
		var e application.SlackEvent
		if err := rows.Scan(&e.ID, &e.AppID, &e.ExternalEventID, &e.TeamID, &e.Type, &e.BotUserIDs, &e.OccurredAt, &e.ReceivedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
func (s *Store) CompleteSlackEvent(ctx context.Context, in application.CompleteSlackEventInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET applied_at = $2 WHERE id = $1 AND applied_at IS NULL`, in.ID, in.Now)
	return err
}
func (s *Store) ClaimSlackEmailFallback(ctx context.Context, in application.ClaimSlackEmailFallbackInput) (bool, error) {
	if err := in.Validate(); err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO slack_email_fallbacks (organization_id, request_id, user_id, created_at)
 SELECT $1,$2,$3,$4 FROM approval_requests r
 WHERE r.organization_id=$1 AND r.id=$2 AND r.status='pending' AND r.deadline_at>$4
 AND EXISTS (SELECT 1 FROM memberships m WHERE m.organization_id=$1 AND m.user_id=$3 AND m.status='active')
 ON CONFLICT DO NOTHING`, in.OrganizationID, in.RequestID, in.UserID, in.Now)
	return tag.RowsAffected() == 1, err
}
