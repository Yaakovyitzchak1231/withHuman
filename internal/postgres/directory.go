package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// ApplyDirectoryCommands applies one batch of an edition's normalized
// directory commands to users, memberships, teams and their links in a
// single transaction under the organization's seat lock. Every command
// is written as an upsert, so redelivery and retries cannot duplicate
// state; a batch that would remove the last owner or provision past the
// seat quota applies nothing and answers the typed error the edition
// records against its event.
func (s *Store) ApplyDirectoryCommands(ctx context.Context, input application.ApplyDirectoryCommandsInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	err := retrySerializable(func() error { return s.applyDirectoryCommands(ctx, input) })
	if isLastOwnerViolation(err) {
		return fmt.Errorf("%w: %w", application.ErrDirectoryLastOwner, err)
	}
	return err
}

func (s *Store) applyDirectoryCommands(ctx context.Context, input application.ApplyDirectoryCommandsInput) error {
	// Every membership this batch creates counts against the seat quota;
	// the seat lock is taken once, ahead of the transaction, so the counts
	// inside materializeUser are consistent across the batch's commands.
	alloc, err := s.beginAllocation(ctx, application.DimensionSeats, string(input.OrganizationID), input.SeatQuota)
	if err != nil {
		return err
	}
	defer alloc.Close(ctx)
	tx := alloc.Tx()
	// A suspended or pending-deletion organization applies nothing: the
	// edition keeps the batch and re-syncs directory state after a resume.
	var organizationActive bool
	if err := tx.QueryRow(ctx, `SELECT status = 'active' FROM organizations WHERE id = $1`,
		string(input.OrganizationID)).Scan(&organizationActive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return err
	}
	if !organizationActive {
		return application.ErrDirectoryOrganizationInactive
	}
	connectionID, err := ensureDirectoryConnection(ctx, tx, string(input.OrganizationID), input.AdapterKey, input.ConnectionExternalID, input)
	if err != nil {
		return err
	}
	applier := directoryApplier{store: s, tx: tx, organizationID: string(input.OrganizationID), connectionID: connectionID, input: input}
	for _, command := range input.Commands {
		if err := applier.apply(ctx, command); err != nil {
			return err
		}
	}
	return alloc.Commit(ctx)
}

func ensureDirectoryConnection(ctx context.Context, tx pgx.Tx, organizationID, adapterKey, externalID string, input application.ApplyDirectoryCommandsInput) (string, error) {
	if externalID == "" {
		externalID = "default"
	}
	var connectionID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM directory_connections
        WHERE adapter_key = $1 AND provider_resource_id = $2`, adapterKey, externalID).Scan(&connectionID)
	if err == nil {
		return connectionID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	connectionID = uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO directory_connections
        (id, organization_id, adapter_key, provider_resource_id, state, reconciliation_status, created_at, updated_at)
        VALUES ($1, $2, $3, $4, 'active', 'pending', $5, $5)
        ON CONFLICT (adapter_key, provider_resource_id) DO NOTHING`,
		connectionID, organizationID, adapterKey, externalID, input.Now); err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM directory_connections
        WHERE adapter_key = $1 AND provider_resource_id = $2`, adapterKey, externalID).Scan(&connectionID)
	return connectionID, err
}

type directoryApplier struct {
	store          *Store
	tx             pgx.Tx
	organizationID string
	connectionID   string
	input          application.ApplyDirectoryCommandsInput
}

type directoryUserData struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Active      bool   `json:"active"`
	Removed     bool   `json:"removed"`
}

func (a directoryApplier) apply(ctx context.Context, command application.DirectoryCommandRecord) error {
	switch command.Type {
	case "upsert_user", "deactivate_user":
		var data directoryUserData
		if err := json.Unmarshal(command.Data, &data); err != nil {
			return fmt.Errorf("directory user command is invalid: %w", err)
		}
		if command.Type == "deactivate_user" {
			data.Active = false
		}
		return a.applyUser(ctx, command.ExternalID, data)
	case "upsert_group":
		var data struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(command.Data, &data); err != nil {
			return fmt.Errorf("directory group command is invalid: %w", err)
		}
		return a.upsertGroup(ctx, command.ExternalID, data.Name)
	case "remove_group":
		return a.removeGroup(ctx, command.ExternalID)
	case "replace_group_membership":
		var data struct {
			MemberExternalID string `json:"member_external_id"`
			MemberEmail      string `json:"member_email"`
			Operation        string `json:"operation"`
		}
		if err := json.Unmarshal(command.Data, &data); err != nil {
			return fmt.Errorf("directory group membership command is invalid: %w", err)
		}
		return a.replaceGroupMembership(ctx, command.ExternalID, data.MemberExternalID, data.MemberEmail, data.Operation)
	}
	return fmt.Errorf("unsupported directory command %q", command.Type)
}

func (a directoryApplier) membershipForUserLink(ctx context.Context, externalID string) (string, bool, error) {
	var membershipID string
	err := a.tx.QueryRow(ctx, `SELECT membership_id::text FROM directory_object_links
        WHERE directory_connection_id = $1 AND provider_object_type = 'user' AND provider_object_id = $2`,
		a.connectionID, externalID).Scan(&membershipID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return membershipID, err == nil, err
}

func (a directoryApplier) applyUser(ctx context.Context, externalID string, data directoryUserData) error {
	membershipID, linked, err := a.membershipForUserLink(ctx, externalID)
	if err != nil {
		return err
	}
	if !linked {
		membershipID, err = a.materializeUser(ctx, externalID, data)
		if err != nil || membershipID == "" {
			return err
		}
	}
	targetStatus := domain.MembershipSuspended
	if data.Active {
		targetStatus = domain.MembershipActive
	}
	if data.Removed {
		targetStatus = domain.MembershipDeprovisioned
	}
	var userID string
	err = a.tx.QueryRow(ctx, `UPDATE memberships
        SET status = $3, directory_managed = true, admission_source = 'directory', updated_at = $4
        WHERE id = $1 AND organization_id = $2
        RETURNING user_id::text`,
		membershipID, a.organizationID, string(targetStatus), a.input.Now).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if data.DisplayName != "" {
		if _, err := a.tx.Exec(ctx, `UPDATE users SET display_name = $2, updated_at = $3 WHERE id = $1`,
			userID, data.DisplayName, a.input.Now); err != nil {
			return err
		}
	}
	if targetStatus != domain.MembershipActive {
		if _, err := a.tx.Exec(ctx, `UPDATE sessions SET revoked_at = $3
            WHERE organization_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
			a.organizationID, userID, a.input.Now); err != nil {
			return err
		}
		if err := a.store.handOffAgentOwnerRequests(ctx, a.tx, domain.ID(a.organizationID), domain.ID(membershipID), domain.ActorSystem, "", a.input.Now); err != nil {
			return err
		}
	}
	if targetStatus == domain.MembershipDeprovisioned {
		if err := a.store.revokeMembershipAPIKeys(ctx, a.tx, domain.ID(a.organizationID), domain.ID(membershipID), domain.ActorSystem, "", a.input.Now); err != nil {
			return err
		}
		if err := a.store.disableMembershipAgentInstances(ctx, a.tx, domain.ID(a.organizationID), domain.ID(membershipID), domain.ActorSystem, "", a.input.Now); err != nil {
			return err
		}
	}
	return a.audit(ctx, "membership."+auditVerb(targetStatus), membershipID, map[string]any{
		"external_id": externalID, "email": data.Email,
	})
}

func auditVerb(status domain.MembershipStatus) string {
	switch status {
	case domain.MembershipActive:
		return "directory_synced"
	case domain.MembershipSuspended:
		return "suspended"
	default:
		return "deprovisioned"
	}
}

// materializeUser finds or creates the user and membership behind an
// unlinked directory user and links them. A removal event for an unknown
// user is a no-op and returns an empty membership ID.
func (a directoryApplier) materializeUser(ctx context.Context, externalID string, data directoryUserData) (string, error) {
	if data.Email == "" {
		return "", fmt.Errorf("directory user %s carries no email", externalID)
	}
	if data.Removed && !data.Active {
		var exists bool
		if err := a.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(primary_email) = lower($1))`,
			data.Email).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return "", nil
		}
	}
	var userID string
	err := a.tx.QueryRow(ctx, `SELECT id::text FROM users WHERE lower(primary_email) = lower($1)`, data.Email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		userID = uuid.NewString()
		displayName := data.DisplayName
		if displayName == "" {
			displayName = data.Email
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO users (id, display_name, primary_email, status, created_at, updated_at)
            VALUES ($1, $2, $3, 'active', $4, $4)`, userID, displayName, strings.ToLower(data.Email), a.input.Now); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	var membershipID string
	err = a.tx.QueryRow(ctx, `SELECT id::text FROM memberships WHERE organization_id = $1 AND user_id = $2`,
		a.organizationID, userID).Scan(&membershipID)
	if errors.Is(err, pgx.ErrNoRows) {
		if !a.input.SeatQuota.Unlimited() {
			used, err := countSeats(ctx, a.tx, a.organizationID, a.input.Now, "")
			if err != nil {
				return "", err
			}
			if err := a.input.SeatQuota.Check(application.DimensionSeats, used); err != nil {
				return "", err
			}
		}
		membershipID = uuid.NewString()
		if _, err := a.tx.Exec(ctx, `INSERT INTO memberships
            (id, user_id, organization_id, status, admission_source, directory_managed, created_at, updated_at)
            VALUES ($1, $2, $3, 'active', 'directory', true, $4, $4)`,
			membershipID, userID, a.organizationID, a.input.Now); err != nil {
			return "", err
		}
		if err := a.audit(ctx, "membership.created", membershipID, map[string]any{
			"admission_source": "directory", "email": data.Email,
		}); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if _, err := a.tx.Exec(ctx, `INSERT INTO directory_object_links
        (directory_connection_id, organization_id, provider_object_type, provider_object_id, membership_id, updated_at)
        VALUES ($1, $2, 'user', $3, $4, $5)
        ON CONFLICT (directory_connection_id, provider_object_type, provider_object_id)
        DO UPDATE SET membership_id = EXCLUDED.membership_id, updated_at = EXCLUDED.updated_at`,
		a.connectionID, a.organizationID, externalID, membershipID, a.input.Now); err != nil {
		return "", err
	}
	return membershipID, nil
}

func (a directoryApplier) teamForGroupLink(ctx context.Context, externalID string) (string, bool, error) {
	var teamID string
	err := a.tx.QueryRow(ctx, `SELECT team_id::text FROM directory_object_links
        WHERE directory_connection_id = $1 AND provider_object_type = 'group' AND provider_object_id = $2`,
		a.connectionID, externalID).Scan(&teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return teamID, err == nil, err
}

func (a directoryApplier) upsertGroup(ctx context.Context, externalID, name string) error {
	teamID, linked, err := a.teamForGroupLink(ctx, externalID)
	if err != nil {
		return err
	}
	if linked {
		_, err := a.tx.Exec(ctx, `UPDATE teams SET name = $3 WHERE id = $1 AND organization_id = $2`,
			teamID, a.organizationID, name)
		return err
	}
	err = a.tx.QueryRow(ctx, `SELECT id::text FROM teams WHERE organization_id = $1 AND name = $2 AND archived_at IS NULL`,
		a.organizationID, name).Scan(&teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		teamID = uuid.NewString()
		if _, err := a.tx.Exec(ctx, `INSERT INTO teams (id, organization_id, name) VALUES ($1, $2, $3)`,
			teamID, a.organizationID, name); err != nil {
			return err
		}
		if err := a.auditTeam(ctx, "team.created", teamID, map[string]any{
			"name": name, "admission_source": "directory",
		}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = a.tx.Exec(ctx, `INSERT INTO directory_object_links
        (directory_connection_id, organization_id, provider_object_type, provider_object_id, team_id, updated_at)
        VALUES ($1, $2, 'group', $3, $4, $5)
        ON CONFLICT (directory_connection_id, provider_object_type, provider_object_id)
        DO UPDATE SET team_id = EXCLUDED.team_id, updated_at = EXCLUDED.updated_at`,
		a.connectionID, a.organizationID, externalID, teamID, a.input.Now)
	return err
}

func (a directoryApplier) removeGroup(ctx context.Context, externalID string) error {
	teamID, linked, err := a.teamForGroupLink(ctx, externalID)
	if err != nil || !linked {
		return err
	}
	if err := requireLiveTeam(ctx, a.tx, domain.ID(a.organizationID), domain.ID(teamID)); err != nil {
		return err
	}
	// Referenced teams return to manual control; otherwise archive the
	// identity and preserve its complete roster for the record.
	targeted, err := teamTargetedByActivePath(ctx, a.tx, domain.ID(a.organizationID), domain.ID(teamID))
	if err != nil {
		return err
	}
	pending, err := teamTargetedByPendingRequest(ctx, a.tx, domain.ID(a.organizationID), domain.ID(teamID))
	if err != nil {
		return err
	}
	var manual bool
	if err := a.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership_teams
        WHERE organization_id = $1 AND team_id = $2 AND source = 'manual')`, a.organizationID, teamID).Scan(&manual); err != nil {
		return err
	}
	retain := targeted || pending || manual
	if retain {
		if _, err := a.tx.Exec(ctx, `DELETE FROM membership_teams
            WHERE organization_id = $1 AND team_id = $2 AND source = 'directory'`, a.organizationID, teamID); err != nil {
			return err
		}
	}
	// The grant the group mapped to goes with the group.
	if err := a.store.revokeAssignmentsWhere(ctx, a.tx, domain.ID(a.organizationID), domain.ActorSystem, "", a.input.Now,
		`ra.source = 'directory' AND ra.source_key = $2`, externalID); err != nil {
		return err
	}
	if _, err := a.tx.Exec(ctx, `DELETE FROM directory_object_links
        WHERE directory_connection_id = $1 AND provider_object_type = 'group' AND provider_object_id = $2`,
		a.connectionID, externalID); err != nil {
		return err
	}
	if retain {
		return nil
	}
	// Archive unused directory teams, revoking both their own grants and
	// every grant scoped to them.
	if err := a.store.revokeAssignmentsWhere(ctx, a.tx, domain.ID(a.organizationID), domain.ActorSystem, "", a.input.Now,
		`((ra.principal_kind = 'team' AND ra.principal_id = $2) OR (ra.scope_kind = 'team' AND ra.scope_id = $2::text))`,
		teamID); err != nil {
		return err
	}
	if _, err := a.tx.Exec(ctx, `UPDATE teams SET archived_at = $3 WHERE id = $1 AND organization_id = $2`,
		teamID, a.organizationID, a.input.Now); err != nil {
		return err
	}
	if err := a.store.deactivateTeamEscalationPolicyForArchive(ctx, a.tx, domain.ID(a.organizationID), domain.ID(teamID), domain.ActorSystem, "", a.input.Now); err != nil {
		return err
	}
	return a.auditTeam(ctx, "team.archived", teamID, map[string]any{"admission_source": "directory"})
}

func (a directoryApplier) replaceGroupMembership(ctx context.Context, groupExternalID, memberExternalID, memberEmail, operation string) error {
	teamID, linked, err := a.teamForGroupLink(ctx, groupExternalID)
	if err != nil {
		return err
	}
	if !linked {
		if err := a.upsertGroup(ctx, groupExternalID, "directory-"+groupExternalID); err != nil {
			return err
		}
		teamID, _, err = a.teamForGroupLink(ctx, groupExternalID)
		if err != nil {
			return err
		}
	}
	membershipID, memberLinked, err := a.membershipForUserLink(ctx, memberExternalID)
	if err != nil {
		return err
	}
	if !memberLinked && memberEmail != "" {
		err = a.tx.QueryRow(ctx, `SELECT m.id::text FROM memberships m JOIN users u ON u.id = m.user_id
            WHERE m.organization_id = $1 AND lower(u.primary_email) = lower($2)`,
			a.organizationID, memberEmail).Scan(&membershipID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if membershipID == "" {
		return nil
	}
	if operation == "remove" {
		_, err = a.tx.Exec(ctx, `DELETE FROM membership_teams
            WHERE membership_id = $1 AND team_id = $2 AND source = 'directory' AND source_key = $3`,
			membershipID, teamID, groupExternalID)
		return err
	}
	_, err = a.tx.Exec(ctx, `INSERT INTO membership_teams
        (membership_id, team_id, organization_id, source, source_key)
        VALUES ($1, $2, $3, 'directory', $4)
        ON CONFLICT DO NOTHING`, membershipID, teamID, a.organizationID, groupExternalID)
	return err
}

func (a directoryApplier) audit(ctx context.Context, eventType, subjectID string, data map[string]any) error {
	payload, _ := json.Marshal(data)
	return a.store.appendAuditEvent(ctx, a.tx, domain.AuditEvent{
		OrganizationID: domain.ID(a.organizationID), EventType: eventType, ActorType: domain.ActorSystem,
		SubjectType: "membership", SubjectID: domain.ID(subjectID),
		Data: payload, OccurredAt: a.input.Now,
	})
}

func (a directoryApplier) auditTeam(ctx context.Context, eventType, teamID string, data map[string]any) error {
	payload, _ := json.Marshal(data)
	return a.store.appendAuditEvent(ctx, a.tx, domain.AuditEvent{
		OrganizationID: domain.ID(a.organizationID), EventType: eventType, ActorType: domain.ActorSystem,
		SubjectType: "team", SubjectID: domain.ID(teamID),
		Data: payload, OccurredAt: a.input.Now,
	})
}
