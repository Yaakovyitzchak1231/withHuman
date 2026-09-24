package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

// Creation takes short-lived table locks before reading authority. They
// conflict with every membership, role and scope writer, including directory
// sync, so delegation cannot race with removal or scope deletion.
func (s *Store) createOrganizationAPIKey(ctx context.Context, input application.CreateAPIKeyInput) (domain.APIKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.APIKey{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, string(input.OrganizationID)+":"+string(input.MembershipID)+":"+input.IdempotencyKey); err != nil {
		return domain.APIKey{}, err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE organizations, organization_policies, memberships, users, sessions, roles, role_assignments, membership_teams, teams, agents IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return domain.APIKey{}, err
	}
	// Locks can wait behind a membership or policy update. Recheck session
	// freshness and expiry against issuance time after those writers finish.
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&input.Now); err != nil {
		return domain.APIKey{}, err
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(input.Now) {
		return domain.APIKey{}, domain.ErrInvalid
	}
	var userID domain.ID
	err = tx.QueryRow(ctx, `SELECT m.user_id FROM memberships m JOIN users u ON u.id = m.user_id JOIN organizations o ON o.id = m.organization_id WHERE m.organization_id = $1 AND m.id = $2 AND m.status = 'active' AND u.status = 'active' AND o.status = 'active'`, input.OrganizationID, input.MembershipID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.APIKey{}, domain.ErrForbidden
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	actor := input.Actor
	err = tx.QueryRow(ctx, `SELECT auth_method, assurance, authenticated_at FROM sessions WHERE id=$1 AND organization_id=$2 AND user_id=$3 AND revoked_at IS NULL AND expires_at > $4`, actor.Auth.SessionID, input.OrganizationID, userID, input.Now).Scan(&actor.Auth.Method, &actor.Auth.Assurance, &actor.Auth.AuthenticatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.APIKey{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	input.AssuranceAtIssue = actor.Auth.Assurance

	actor.ActorID = userID
	actor.Grants, err = loadGrants(ctx, tx, input.OrganizationID, domain.PrincipalMembership, input.MembershipID)
	if err != nil {
		return domain.APIKey{}, err
	}
	if err = authorization.Check(actor, domain.PermissionAPIKeyOrganizationIssue, domain.OrganizationResource()); err != nil {
		return domain.APIKey{}, err
	}
	if err = domain.ValidateOrganizationKeyPolicies(actor, input.Policies); err != nil {
		return domain.APIKey{}, err
	}
	policy, err := readOrganizationPolicy(ctx, tx, input.OrganizationID, "")
	if err != nil {
		return domain.APIKey{}, err
	}
	if !policy.OrganizationAPIKeysAllowed {
		return domain.APIKey{}, domain.ErrAPIKeysDisabled
	}
	if rejected := authorization.RequireFresh(actor.Auth, policy.ApprovalReauthWindow, policy.MFAMode, input.Now); rejected != nil {
		return domain.APIKey{}, rejected.Err
	}
	var existingID domain.ID
	var fingerprint []byte
	err = tx.QueryRow(ctx, `SELECT api_key_id, fingerprint FROM api_key_creations WHERE organization_id = $1 AND membership_id = $2 AND idempotency_key = $3`, input.OrganizationID, input.MembershipID, input.IdempotencyKey).Scan(&existingID, &fingerprint)
	if err == nil {
		if !bytes.Equal(fingerprint, input.Fingerprint) {
			return domain.APIKey{}, domain.ErrIdempotencyConflict
		}
		return scanAPIKey(tx.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_keys k WHERE k.organization_id = $1 AND k.id = $2`, input.OrganizationID, existingID))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.APIKey{}, err
	}
	for _, policy := range input.Policies {
		for _, scope := range policy.Scopes() {
			if err := validatePolicyResource(ctx, tx, input.OrganizationID, scope); err != nil {
				return domain.APIKey{}, err
			}
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO api_keys (id, organization_id, kind, created_by_membership_id, name, secret_hash, assurance_at_issue, created_at, expires_at) VALUES ($1,$2,'organization',$3,$4,$5,$6,$7,$8)`, input.KeyID, input.OrganizationID, input.MembershipID, input.Name, input.SecretHash, input.AssuranceAtIssue, input.Now, input.ExpiresAt)
	if err != nil {
		return domain.APIKey{}, err
	}
	for _, policy := range input.Policies {
		policyID := domain.ID(uuid.NewString())
		if _, err := tx.Exec(ctx, `INSERT INTO permission_policies(id,organization_id) VALUES($1,$2)`, policyID, input.OrganizationID); err != nil {
			return domain.APIKey{}, err
		}
		if err := insertPolicyBindings(ctx, tx, application.ReadPermissionPoliciesInput{Actor: actor, PrincipalKind: domain.PrincipalAPIKey, PrincipalID: input.KeyID}, policyID, policy, input.Now); err != nil {
			return domain.APIKey{}, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO api_key_creations (organization_id,membership_id,idempotency_key,fingerprint,api_key_id) VALUES ($1,$2,$3,$4,$5)`, input.OrganizationID, input.MembershipID, input.IdempotencyKey, input.Fingerprint, input.KeyID)
	if err != nil {
		return domain.APIKey{}, err
	}
	if err = s.insertAPIKeyAudit(ctx, tx, input.OrganizationID, "api_key.created", input.MembershipID, input.KeyID, map[string]any{"kind": domain.APIKeyOrganization, "name": input.Name, "permission_policies": input.Policies, "expires_at": input.ExpiresAt}, input.Now); err != nil {
		return domain.APIKey{}, err
	}
	key, err := scanAPIKey(tx.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_keys k WHERE k.organization_id = $1 AND k.id = $2`, input.OrganizationID, input.KeyID))
	if err != nil {
		return domain.APIKey{}, err
	}
	return key, tx.Commit(ctx)
}

func (s *Store) ListAPIKeyScopes(ctx context.Context, input application.ListAPIKeyScopesInput) ([]domain.PermissionResource, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT 'team',id::text,name FROM teams WHERE organization_id=$1 AND archived_at IS NULL UNION SELECT 'agent',slug,name FROM agents WHERE organization_id=$1 AND status <> 'archived' ORDER BY 1,2`, input.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scopes := []domain.PermissionResource{}
	for rows.Next() {
		var scope domain.PermissionResource
		if err := rows.Scan(&scope.Kind, &scope.ID, &scope.Name); err != nil {
			return nil, err
		}
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}

func (s *Store) RecordAPIKeyUse(ctx context.Context, input application.RecordAPIKeyUseInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	p := input.Principal
	data, err := json.Marshal(map[string]any{"credential_id": p.Auth.CredentialID, "method": input.Method, "route": input.Route})
	if err != nil {
		return err
	}
	return s.appendAuditEvent(ctx, s.pool, domain.AuditEvent{OrganizationID: p.OrganizationID, ActorType: p.ActorType, ActorID: p.ActorID, EventType: "api_key.used", SubjectType: "api_key", SubjectID: p.Auth.CredentialID, Data: data, OccurredAt: input.Now})
}
