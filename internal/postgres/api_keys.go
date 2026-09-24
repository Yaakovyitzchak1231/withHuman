package postgres

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// apiKeyColumns is every api_keys column except the secret hash, in the
// order scanAPIKey reads them.
const apiKeyColumns = `k.id::text, k.organization_id::text, k.kind, COALESCE(k.membership_id::text, ''), k.name,
	k.permissions, k.assurance_at_issue, k.created_at, k.expires_at, k.last_used_at, k.revoked_at,
	COALESCE(k.revoked_by_membership_id::text, ''), COALESCE(k.revocation_reason, ''), COALESCE(k.created_by_membership_id::text, k.membership_id::text, ''),
	COALESCE(k.oauth_client_id::text, ''), COALESCE((SELECT c.name FROM oauth_clients c WHERE c.id = k.oauth_client_id), ''),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('permission', g.permission, 'scope_kind', g.scope_kind, 'scope_id', COALESCE(g.scope_id, '')) ORDER BY g.permission, g.scope_kind, g.scope_id) FROM (SELECT DISTINCT p.permission, ra.scope_kind, ra.scope_id FROM role_assignments ra JOIN roles r ON r.id=ra.role_id CROSS JOIN LATERAL unnest(r.permissions) p(permission) WHERE ra.organization_id=k.organization_id AND ra.principal_kind='api_key' AND ra.principal_id=k.id) g), '[]'::jsonb),
 (SELECT CASE WHEN o.status <> 'active' THEN 'organization_inactive' WHEN (k.kind = 'personal' AND NOT p.personal_api_keys_allowed) OR (k.kind = 'organization' AND NOT p.organization_api_keys_allowed) THEN 'api_keys_disabled' ELSE '' END FROM organizations o JOIN organization_policies p ON p.organization_id=o.id WHERE o.id=k.organization_id)`

func scanAPIKey(row pgx.Row) (domain.APIKey, error) {
	var key domain.APIKey
	var id, organizationID, kind, membershipID, revokedBy, reason, oauthClientID string
	var permissions []string
	var grantsJSON []byte
	if err := row.Scan(&id, &organizationID, &kind, &membershipID, &key.Name,
		&permissions, &key.AssuranceAtIssue, &key.CreatedAt, &key.ExpiresAt, &key.LastUsedAt, &key.RevokedAt,
		&revokedBy, &reason, &key.CreatedByMembershipID, &oauthClientID, &key.OAuthClientName, &grantsJSON, &key.BlockedReason); err != nil {
		return domain.APIKey{}, err
	}
	key.OAuthClientID = domain.ID(oauthClientID)
	if err := json.Unmarshal(grantsJSON, &key.Grants); err != nil {
		return domain.APIKey{}, err
	}
	key.ID, key.OrganizationID, key.Kind = domain.ID(id), domain.ID(organizationID), domain.APIKeyKind(kind)
	key.MembershipID, key.RevokedByMembershipID = domain.ID(membershipID), domain.ID(revokedBy)
	key.RevocationReason = domain.APIKeyRevocationReason(reason)
	if permissions != nil {
		key.Permissions = make([]domain.Permission, 0, len(permissions))
		for _, permission := range permissions {
			key.Permissions = append(key.Permissions, domain.Permission(permission))
		}
	}
	return key, nil
}

// apiKeyPermissionStrings keeps nil as nil: NULL in the column means the
// key inherits everything, which an empty array must never be mistaken
// for.
func apiKeyPermissionStrings(permissions []domain.Permission) []string {
	if permissions == nil {
		return nil
	}
	return permissionStrings(permissions)
}

// insertAPIKeyAudit records one action on a key, attributed to the acting
// member's user like every other human event.
func (s *Store) insertAPIKeyAudit(ctx context.Context, tx pgx.Tx, organizationID domain.ID, eventType string, actorMembershipID, keyID domain.ID, data map[string]any, now time.Time) error {
	actorID, err := membershipActorID(ctx, tx, organizationID, actorMembershipID)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(data)
	return s.appendAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: organizationID, EventType: eventType,
		ActorType: domain.ActorHuman, ActorID: actorID,
		SubjectType: "api_key", SubjectID: keyID,
		Data: payload, OccurredAt: now,
	})
}

func (s *Store) CreateAPIKey(ctx context.Context, input application.CreateAPIKeyInput) (domain.APIKey, error) {
	if err := input.Validate(); err != nil {
		return domain.APIKey{}, err
	}
	if input.Kind == domain.APIKeyOrganization {
		return s.createOrganizationAPIKey(ctx, input)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.APIKey{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO api_keys
		(id, organization_id, kind, membership_id, name, permissions, secret_hash, assurance_at_issue, created_at, expires_at, oauth_client_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, '')::uuid)`,
		input.KeyID, input.OrganizationID, string(domain.APIKeyPersonal), input.MembershipID, input.Name,
		apiKeyPermissionStrings(input.Permissions), input.SecretHash, input.AssuranceAtIssue, input.Now, input.ExpiresAt, string(input.OAuthClientID)); err != nil {
		return domain.APIKey{}, err
	}
	data := map[string]any{"kind": domain.APIKeyPersonal, "name": input.Name, "assurance_at_issue": input.AssuranceAtIssue}
	if input.OAuthClientID != "" {
		data["oauth_client_id"] = input.OAuthClientID
	}
	if input.Permissions != nil {
		data["permissions"] = apiKeyPermissionStrings(input.Permissions)
	}
	if input.ExpiresAt != nil {
		data["expires_at"] = input.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if err := s.insertAPIKeyAudit(ctx, tx, input.OrganizationID, "api_key.created", input.MembershipID, input.KeyID, data, input.Now); err != nil {
		return domain.APIKey{}, err
	}
	key, err := scanAPIKey(tx.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_keys k WHERE k.id = $1`, input.KeyID))
	if err != nil {
		return domain.APIKey{}, err
	}
	return key, tx.Commit(ctx)
}

func (s *Store) ListAPIKeys(ctx context.Context, input application.ListAPIKeysInput) ([]domain.APIKey, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+apiKeyColumns+` FROM api_keys k
		WHERE k.organization_id = $1 AND ($2 = '' OR k.membership_id::text = $2)
		ORDER BY k.created_at DESC, k.id`, input.OrganizationID, string(input.MembershipID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]domain.APIKey, 0)
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) GetAPIKey(ctx context.Context, input application.GetAPIKeyInput) (domain.APIKey, error) {
	if err := input.Validate(); err != nil {
		return domain.APIKey{}, err
	}
	key, err := scanAPIKey(s.pool.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_keys k
		WHERE k.organization_id = $1 AND k.id = $2`, input.OrganizationID, input.KeyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.APIKey{}, domain.ErrNotFound
	}
	return key, err
}

// RevokeAPIKey stamps one key. A key that is already revoked answers
// success, so a retried revoke is idempotent; a key outside the
// organization, or outside the member's own keys when MembershipID is
// set, is not found.
func (s *Store) RevokeAPIKey(ctx context.Context, input application.RevokeAPIKeyInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var revokedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT revoked_at FROM api_keys
		WHERE organization_id = $1 AND id = $2 AND ($3 = '' OR membership_id::text = $3) FOR UPDATE`,
		input.OrganizationID, input.KeyID, string(input.MembershipID)).Scan(&revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if revokedAt != nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at = $3, revoked_by_membership_id = $4, revocation_reason = $5
		WHERE organization_id = $1 AND id = $2`,
		input.OrganizationID, input.KeyID, input.Now, input.ActorMembershipID, string(domain.APIKeyRevokedManually)); err != nil {
		return err
	}
	if err := s.insertAPIKeyAudit(ctx, tx, input.OrganizationID, "api_key.revoked", input.ActorMembershipID, input.KeyID,
		map[string]any{"revocation_reason": domain.APIKeyRevokedManually}, input.Now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// revokeMembershipAPIKeys is the cascade a membership deprovision runs:
// every live key of the member is revoked in the same transaction, one
// audit event per key. actorID may be empty for a directory-driven
// removal, which the audit row then attributes to the system.
func (s *Store) revokeMembershipAPIKeys(ctx context.Context, tx pgx.Tx, organizationID, membershipID domain.ID, actorType domain.ActorType, actorID domain.ID, now time.Time) error {
	rows, err := tx.Query(ctx, `UPDATE api_keys SET revoked_at = $3, revocation_reason = $4
		WHERE organization_id = $1 AND kind = 'personal' AND membership_id = $2 AND revoked_at IS NULL
		RETURNING id::text, name`,
		organizationID, membershipID, now, string(domain.APIKeyRevokedMembershipDeprovisioned))
	if err != nil {
		return err
	}
	type revoked struct{ id, name string }
	var keys []revoked
	for rows.Next() {
		var key revoked
		if err := rows.Scan(&key.id, &key.name); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, key := range keys {
		payload, _ := json.Marshal(map[string]any{
			"revocation_reason": domain.APIKeyRevokedMembershipDeprovisioned, "name": key.name, "membership_id": membershipID,
		})
		if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
			OrganizationID: organizationID, EventType: "api_key.revoked",
			ActorType: actorType, ActorID: actorID,
			SubjectType: "api_key", SubjectID: domain.ID(key.id),
			Data: payload, OccurredAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ResolveAPIKey authenticates a presented whk_ bearer with a live lookup.
// The key row, the organization, its policy switch, and the member's
// status and grants are all read now, so revocation, expiry, disabling
// keys for the organization, suspension, and role changes take effect on
// the next request. The principal is the member's own, with grants
// narrowed to the key's list and the auth context naming the key.
func (s *Store) ResolveAPIKey(ctx context.Context, input application.ResolveAPIKeyInput) (domain.Principal, error) {
	if err := input.Validate(); err != nil {
		return domain.Principal{}, err
	}
	var storedHash []byte
	var kind, organizationID, membershipID, userID, membershipStatus, assurance string
	var permissions []string
	var allowed, organizationAllowed bool
	var teamIDs []string
	err := s.pool.QueryRow(ctx, `SELECT k.secret_hash, k.kind, k.organization_id::text, COALESCE(k.membership_id::text, ''),
		COALESCE(u.id::text, ''), COALESCE(m.status, ''), k.permissions, k.assurance_at_issue, p.personal_api_keys_allowed, p.organization_api_keys_allowed,
		COALESCE((SELECT array_agg(DISTINCT team_id::text ORDER BY team_id::text) FROM active_membership_teams WHERE membership_id = m.id), '{}')
		FROM api_keys k
		JOIN organizations o ON o.id = k.organization_id
		JOIN organization_policies p ON p.organization_id = k.organization_id
		LEFT JOIN memberships m ON m.id = k.membership_id
		LEFT JOIN users u ON u.id = m.user_id AND u.status = 'active'
		WHERE k.id = $1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > $2) AND o.status = 'active'`,
		input.KeyID, input.Now).Scan(&storedHash, &kind, &organizationID, &membershipID, &userID, &membershipStatus,
		&permissions, &assurance, &allowed, &organizationAllowed, &teamIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.Principal{}, err
	}
	if (input.Kind != "" && string(input.Kind) != kind) || subtle.ConstantTimeCompare(storedHash, input.SecretHash) != 1 {
		return domain.Principal{}, domain.ErrUnauthenticated
	}

	if domain.APIKeyKind(kind) == domain.APIKeyOrganization {
		if !organizationAllowed {
			return domain.Principal{}, domain.ErrAPIKeysDisabled
		}
		grants, err := loadGrants(ctx, s.pool, domain.ID(organizationID), domain.PrincipalAPIKey, input.KeyID)
		if err != nil {
			return domain.Principal{}, err
		}
		p := domain.Principal{ActorType: domain.ActorAPIKey, ActorID: input.KeyID, OrganizationID: domain.ID(organizationID), Auth: domain.AuthContext{Method: domain.AuthMethodAPIKey, Assurance: assurance, AuthenticatedAt: input.Now, CredentialID: input.KeyID}}
		_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = $2 WHERE id = $1`, input.KeyID, input.Now)
		return p.WithGrants(domain.FilterPrincipalGrants(p, grants)), nil
	}
	if domain.APIKeyKind(kind) != domain.APIKeyPersonal {
		return domain.Principal{}, domain.ErrUnauthenticated
	}

	if !allowed {
		return domain.Principal{}, domain.ErrAPIKeysDisabled
	}
	if userID == "" {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	if domain.MembershipStatus(membershipStatus) != domain.MembershipActive {
		return domain.Principal{}, domain.ErrMembershipInactive
	}
	grants, err := loadGrants(ctx, s.pool, domain.ID(organizationID), domain.PrincipalMembership, domain.ID(membershipID))
	if err != nil {
		return domain.Principal{}, err
	}
	var narrowing []domain.Permission
	if permissions != nil {
		narrowing = make([]domain.Permission, 0, len(permissions))
		for _, permission := range permissions {
			narrowing = append(narrowing, domain.Permission(permission))
		}
	}
	teams := make([]domain.ID, len(teamIDs))
	for index, id := range teamIDs {
		teams[index] = domain.ID(id)
	}
	_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = $2 WHERE id = $1`, input.KeyID, input.Now)
	principal := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: domain.ID(userID), OrganizationID: domain.ID(organizationID),
		MembershipID: domain.ID(membershipID),
		TeamIDs:      teams,
		Auth: domain.AuthContext{
			Method: domain.AuthMethodAPIKey, Assurance: assurance, AuthenticatedAt: input.Now, CredentialID: input.KeyID,
		},
	}
	return principal.WithGrants(domain.FilterPrincipalGrants(principal, domain.NarrowGrants(grants, narrowing))), nil
}
