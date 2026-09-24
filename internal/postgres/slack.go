package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

const slackConnectionSelect = `SELECT installation_id::text, status, disconnected_at, COALESCE(disconnect_reason, ''), team_id, team_name, bot_user_id, scopes,
    installed_by_membership_id::text, installed_at,
    (SELECT count(*) FROM contact_methods cm JOIN memberships m ON m.user_id = cm.user_id AND m.organization_id = cm.organization_id
      WHERE cm.kind = 'slack' AND cm.organization_id = slack_connections.organization_id AND m.status = 'active' AND slack_connections.status = 'connected' AND cm.slack_installation_id = slack_connections.installation_id)
    FROM slack_connections `

func (s *Store) GetSlackConnection(ctx context.Context, input application.GetSlackConnectionInput) (domain.SlackConnection, error) {
	if err := input.Validate(); err != nil {
		return domain.SlackConnection{}, err
	}
	connection := domain.SlackConnection{OrganizationID: input.OrganizationID}
	var installedBy string
	err := s.pool.QueryRow(ctx, slackConnectionSelect+`WHERE organization_id = $1`, input.OrganizationID).Scan(
		&connection.InstallationID, &connection.Status, &connection.DisconnectedAt, &connection.DisconnectReason, &connection.TeamID, &connection.TeamName, &connection.BotUserID, &connection.Scopes, &installedBy, &connection.InstalledAt, &connection.LinkedMembers)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SlackConnection{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.SlackConnection{}, err
	}
	connection.InstalledByMembershipID = domain.ID(installedBy)
	return connection, nil
}

// OpenSlackBotToken hands back the unsealed token. It is the one read of
// the sealed column, kept separate from GetSlackConnection so the product
// read model never carries the credential by accident.
func (s *Store) OpenSlackBotToken(ctx context.Context, input application.GetSlackConnectionInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	if s.sealer == nil {
		return "", fmt.Errorf("no secrets key is configured to open the Slack token: %w", domain.ErrInvalid)
	}
	var sealed []byte
	err := s.pool.QueryRow(ctx, `SELECT sealed_bot_token FROM slack_connections WHERE organization_id = $1 AND status = 'connected' AND ($2 = '' OR installation_id::text = $2)`,
		input.OrganizationID, input.InstallationID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.sealer.Open(slackTokenLabel, sealed)
}

// UpsertSlackConnection records or replaces the organization's workspace.
// Reinstalling into the same workspace refreshes the token; claiming a
// workspace another organization already holds is a conflict, because
// it would let one tenant receive the other's approvals.
func (s *Store) UpsertSlackConnection(ctx context.Context, input application.UpsertSlackConnectionInput) (domain.SlackConnection, error) {
	if err := input.Validate(); err != nil {
		return domain.SlackConnection{}, err
	}
	installation := input.Installation
	if input.InstallationID == "" {
		input.InstallationID = domain.ID(uuid.NewString())
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.SlackConnection{}, err
	}
	defer tx.Rollback(ctx)
	// A replacement invalidates the previous generation's member matches and
	// queued Slack deliveries, including when the workspace itself is unchanged.
	var replaced domain.ID
	err = tx.QueryRow(ctx, `SELECT installation_id::text FROM slack_connections
 WHERE organization_id=$1 AND status='connected' FOR UPDATE`, input.OrganizationID).Scan(&replaced)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.SlackConnection{}, err
	}
	if replaced != "" {
		if err := queueSlackDisconnection(ctx, tx, input.OrganizationID, replaced, input.Now); err != nil {
			return domain.SlackConnection{}, err
		}
	}
	if input.PreviousOwner != nil {
		if err := input.PreviousOwner.Validate(); err != nil {
			return domain.SlackConnection{}, err
		}
		changed, err := s.disconnectSlack(ctx, tx, application.DisconnectSlackConnectionInput{
			SlackConnectionVersion: *input.PreviousOwner, Reason: "token_revoked", Now: input.Now,
		})
		if err != nil {
			return domain.SlackConnection{}, err
		}
		if !changed {
			return domain.SlackConnection{}, domain.ErrConflict
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO slack_connections
        (organization_id, team_id, team_name, bot_user_id, scopes, sealed_bot_token, installed_by_membership_id, installed_at, installation_id)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT (organization_id) DO UPDATE SET
            status = 'connected', disconnected_at = NULL, disconnect_reason = NULL, installation_id = EXCLUDED.installation_id,
            team_id = EXCLUDED.team_id, team_name = EXCLUDED.team_name, bot_user_id = EXCLUDED.bot_user_id,
            scopes = EXCLUDED.scopes, sealed_bot_token = EXCLUDED.sealed_bot_token,
            installed_by_membership_id = EXCLUDED.installed_by_membership_id, installed_at = EXCLUDED.installed_at`,
		input.OrganizationID, installation.TeamID, installation.TeamName, installation.BotUserID,
		installation.Scopes, input.SealedBotToken, input.InstalledByMembershipID, input.Now, input.InstallationID)
	if isUniqueViolation(err) {
		return domain.SlackConnection{}, fmt.Errorf("that Slack workspace is already connected to another organization: %w", domain.ErrConflict)
	}
	if err != nil {
		return domain.SlackConnection{}, err
	}
	// The audit row never carries the token: the workspace identity and
	// the granted scopes are the whole story.
	if err := s.insertConnectionAudit(ctx, tx, input.OrganizationID, "slack_connection.created", input.InstalledByMembershipID,
		map[string]any{"team_id": installation.TeamID, "team_name": installation.TeamName,
			"bot_user_id": installation.BotUserID, "scopes": installation.Scopes}, input.Now); err != nil {
		return domain.SlackConnection{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SlackConnection{}, err
	}
	return s.GetSlackConnection(ctx, application.GetSlackConnectionInput{OrganizationID: input.OrganizationID})
}

// DeleteSlackConnection disconnects the expected generation and returns its
// unsealed token for revocation while the caller still holds the workspace lock.
func (s *Store) DeleteSlackConnection(ctx context.Context, input application.DeleteSlackConnectionInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var sealed []byte
	var teamID, teamName string
	err = tx.QueryRow(ctx, `SELECT sealed_bot_token, team_id, team_name FROM slack_connections WHERE organization_id = $1 AND installation_id = $2 AND status = 'connected' FOR UPDATE`, input.OrganizationID, input.InstallationID).Scan(&sealed, &teamID, &teamName)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE slack_connections SET status = 'disconnected', sealed_bot_token = NULL,
        disconnected_at = $3, disconnect_reason = 'user_disconnected' WHERE organization_id = $1 AND installation_id = $2`,
		input.OrganizationID, input.InstallationID, input.Now); err != nil {
		return "", err
	}
	if err := s.insertConnectionAudit(ctx, tx, input.OrganizationID, "slack_connection.removed", input.ActorMembershipID,
		map[string]any{"team_id": teamID, "team_name": teamName}, input.Now); err != nil {
		return "", err
	}
	if err := queueSlackDisconnection(ctx, tx, input.OrganizationID, input.InstallationID, input.Now); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if s.sealer == nil {
		return "", nil
	}
	token, err := s.sealer.Open(slackTokenLabel, sealed)
	if err != nil {
		// The connection is inactive either way; an unopenable token cannot be
		// revoked, but it also cannot be used.
		return "", nil
	}
	return token, nil
}

func (s *Store) CreateSlackInstallState(ctx context.Context, input application.CreateSlackInstallStateInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	// Abandoned installs leave states nothing will ever redeem; sweep the
	// expired ones here so the table stays the size of what is in flight.
	if _, err := s.pool.Exec(ctx, `DELETE FROM slack_install_states WHERE expires_at <= $1`, input.Now); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO slack_install_states
        (state_hash, organization_id, membership_id, expires_at, created_at) VALUES ($1, $2, $3, $4, $5)`,
		input.StateHash, input.OrganizationID, input.MembershipID, input.ExpiresAt, input.Now)
	return err
}

// ConsumeSlackInstallState redeems a state once. Deleting and reading in
// one statement means two callbacks racing on the same state cannot both
// complete an install.
func (s *Store) ConsumeSlackInstallState(ctx context.Context, input application.ConsumeSlackInstallStateInput) (application.SlackInstallState, error) {
	if err := input.Validate(); err != nil {
		return application.SlackInstallState{}, err
	}
	var organizationID, membershipID string
	err := s.pool.QueryRow(ctx, `DELETE FROM slack_install_states
        WHERE state_hash = $1 AND expires_at > $2
        RETURNING organization_id::text, membership_id::text`, input.StateHash, input.Now).Scan(&organizationID, &membershipID)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SlackInstallState{}, domain.ErrNotFound
	}
	if err != nil {
		return application.SlackInstallState{}, err
	}
	return application.SlackInstallState{OrganizationID: domain.ID(organizationID), MembershipID: domain.ID(membershipID)}, nil
}

const slackTokenLabel = "slack_bot_token"

// insertConnectionAudit records a chat connection lifecycle event (Slack or
// Discord) on the organization, attributed to the member who acted.
func (s *Store) insertConnectionAudit(ctx context.Context, tx pgx.Tx, organizationID domain.ID, eventType string, actorMembershipID domain.ID, data map[string]any, now time.Time) error {
	actorID, err := membershipActorID(ctx, tx, organizationID, actorMembershipID)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(data)
	return s.appendAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: organizationID, EventType: eventType,
		ActorType: domain.ActorHuman, ActorID: actorID,
		SubjectType: "organization", SubjectID: organizationID,
		Data: encoded, OccurredAt: now,
	})
}
