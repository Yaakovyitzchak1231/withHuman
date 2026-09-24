package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

const discordConnectionSelect = `SELECT guild_id, guild_name, installed_by_membership_id::text, installed_at,
    (SELECT count(*) FROM contact_methods cm JOIN memberships m ON m.user_id = cm.user_id AND m.organization_id = cm.organization_id
      WHERE cm.kind = 'discord' AND cm.organization_id = discord_connections.organization_id AND m.status = 'active')
    FROM discord_connections `

func (s *Store) GetDiscordConnection(ctx context.Context, input application.GetDiscordConnectionInput) (domain.DiscordConnection, error) {
	if err := input.Validate(); err != nil {
		return domain.DiscordConnection{}, err
	}
	connection := domain.DiscordConnection{OrganizationID: input.OrganizationID}
	var installedBy string
	err := s.pool.QueryRow(ctx, discordConnectionSelect+`WHERE organization_id = $1`, input.OrganizationID).Scan(
		&connection.GuildID, &connection.GuildName, &installedBy, &connection.InstalledAt, &connection.LinkedMembers)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DiscordConnection{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.DiscordConnection{}, err
	}
	connection.InstalledByMembershipID = domain.ID(installedBy)
	return connection, nil
}

// UpsertDiscordConnection records or replaces the organization's guild.
// Claiming a guild another organization already holds is a conflict,
// because it would let one tenant receive the other's approvals.
func (s *Store) UpsertDiscordConnection(ctx context.Context, input application.UpsertDiscordConnectionInput) (domain.DiscordConnection, error) {
	if err := input.Validate(); err != nil {
		return domain.DiscordConnection{}, err
	}
	installation := input.Installation
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DiscordConnection{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO discord_connections
        (organization_id, guild_id, guild_name, installed_by_membership_id, installed_at)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (organization_id) DO UPDATE SET
            guild_id = EXCLUDED.guild_id, guild_name = EXCLUDED.guild_name,
            installed_by_membership_id = EXCLUDED.installed_by_membership_id, installed_at = EXCLUDED.installed_at`,
		input.OrganizationID, installation.GuildID, installation.GuildName, input.InstalledByMembershipID, input.Now)
	if isUniqueViolation(err) {
		return domain.DiscordConnection{}, fmt.Errorf("that Discord server is already connected to another organization: %w", domain.ErrConflict)
	}
	if err != nil {
		return domain.DiscordConnection{}, err
	}
	if err := s.insertConnectionAudit(ctx, tx, input.OrganizationID, "discord_connection.created", input.InstalledByMembershipID,
		map[string]any{"guild_id": installation.GuildID, "guild_name": installation.GuildName}, input.Now); err != nil {
		return domain.DiscordConnection{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DiscordConnection{}, err
	}
	return s.GetDiscordConnection(ctx, application.GetDiscordConnectionInput{OrganizationID: input.OrganizationID})
}

// DeleteDiscordConnection removes the guild and every member's Discord
// link with it: each link was verified against this guild's membership,
// so a later connection to a different server must start from nothing.
// Notification rungs naming the removed links cascade away. The guild id
// is returned so the caller can make the bot leave.
func (s *Store) DeleteDiscordConnection(ctx context.Context, input application.DeleteDiscordConnectionInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var guildID, guildName string
	err = tx.QueryRow(ctx, `DELETE FROM discord_connections WHERE organization_id = $1
        RETURNING guild_id, guild_name`, input.OrganizationID).Scan(&guildID, &guildName)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM contact_methods WHERE organization_id = $1 AND kind = 'discord'`, input.OrganizationID); err != nil {
		return "", err
	}
	if err := s.insertConnectionAudit(ctx, tx, input.OrganizationID, "discord_connection.removed", input.ActorMembershipID,
		map[string]any{"guild_id": guildID, "guild_name": guildName}, input.Now); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return guildID, nil
}

func (s *Store) CreateDiscordOAuthState(ctx context.Context, input application.CreateDiscordOAuthStateInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	// Abandoned round trips leave states nothing will ever redeem; sweep
	// the expired ones here so the table stays the size of what is in
	// flight.
	if _, err := s.pool.Exec(ctx, `DELETE FROM discord_oauth_states WHERE expires_at <= $1`, input.Now); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO discord_oauth_states
        (state_hash, purpose, organization_id, membership_id, user_id, expires_at, created_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		input.StateHash, string(input.Purpose), input.OrganizationID, input.MembershipID, input.UserID, input.ExpiresAt, input.Now)
	return err
}

// ConsumeDiscordOAuthState redeems a state once, for its own purpose.
// Deleting and reading in one statement means two callbacks racing on
// the same state cannot both complete.
func (s *Store) ConsumeDiscordOAuthState(ctx context.Context, input application.ConsumeDiscordOAuthStateInput) (application.DiscordOAuthState, error) {
	if err := input.Validate(); err != nil {
		return application.DiscordOAuthState{}, err
	}
	var organizationID, membershipID, userID string
	err := s.pool.QueryRow(ctx, `DELETE FROM discord_oauth_states
        WHERE state_hash = $1 AND purpose = $2 AND expires_at > $3
        RETURNING organization_id::text, membership_id::text, user_id::text`,
		input.StateHash, string(input.Purpose), input.Now).Scan(&organizationID, &membershipID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.DiscordOAuthState{}, domain.ErrNotFound
	}
	if err != nil {
		return application.DiscordOAuthState{}, err
	}
	return application.DiscordOAuthState{
		OrganizationID: domain.ID(organizationID), MembershipID: domain.ID(membershipID), UserID: domain.ID(userID),
	}, nil
}
