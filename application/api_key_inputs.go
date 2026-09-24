package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// APIKeyRepository stores both key kinds and resolves a presented key
// into a principal with a live lookup: revocation, expiry, the
// organization's policy switch, and the member's current status and
// grants all take effect on the next request.
type APIKeyRepository interface {
	RecordAPIKeyUse(context.Context, RecordAPIKeyUseInput) error
	ListAPIKeyScopes(context.Context, ListAPIKeyScopesInput) ([]domain.PermissionResource, error)
	CreateAPIKey(ctx context.Context, input CreateAPIKeyInput) (domain.APIKey, error)
	ListAPIKeys(ctx context.Context, input ListAPIKeysInput) ([]domain.APIKey, error)
	GetAPIKey(ctx context.Context, input GetAPIKeyInput) (domain.APIKey, error)
	RevokeAPIKey(ctx context.Context, input RevokeAPIKeyInput) error
	ResolveAPIKey(ctx context.Context, input ResolveAPIKeyInput) (domain.Principal, error)
}

// CreateAPIKeyInput writes a personal narrowing key or an organization key
// with explicit grants. Organization creation requires a live human session.
type CreateAPIKeyInput struct {
	Kind             domain.APIKeyKind
	Actor            domain.Principal
	Policies         []domain.PermissionPolicySpec
	IdempotencyKey   string
	Fingerprint      []byte
	KeyID            domain.ID
	OrganizationID   domain.ID
	MembershipID     domain.ID
	Name             string
	Permissions      []domain.Permission
	SecretHash       []byte
	AssuranceAtIssue string
	// OAuthClientID names the MCP client a personal key is minted for
	// through the OAuth consent page; empty for a hand-made key.
	OAuthClientID domain.ID
	Now           time.Time
	ExpiresAt     *time.Time
}

func (input CreateAPIKeyInput) Validate() error {
	if err := firstError([]error{
		requiredID("key ID", input.KeyID),
		requiredID("organization ID", input.OrganizationID),
		requiredID("membership ID", input.MembershipID),
		requiredString("API key name", input.Name),
		requiredTokenHash("API key secret hash", input.SecretHash),
		requiredString("assurance at issue", input.AssuranceAtIssue),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if input.Kind == domain.APIKeyOrganization {
		if input.Actor.Auth.SessionID == "" || input.Actor.ActorType != domain.ActorHuman || input.Actor.Auth.Method == domain.AuthMethodAPIKey || input.Actor.MembershipID != input.MembershipID || input.Actor.OrganizationID != input.OrganizationID {
			return domain.ErrForbidden
		}
		if input.Permissions != nil || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 200 || len(input.Fingerprint) != 32 {
			return domain.ErrInvalid
		}
		if err := domain.ValidateOrganizationKeyPolicies(input.Actor, input.Policies); err != nil {
			return err
		}
	}
	if input.Permissions != nil && len(input.Permissions) == 0 {
		return invalidField("API key permissions", "must name at least one permission when given")
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(input.Now) {
		return invalidField("API key expiry time", "must be in the future")
	}
	return nil
}

// ListAPIKeysInput lists an organization's keys, newest first, optionally
// only one member's.
type ListAPIKeysInput struct {
	OrganizationID domain.ID
	MembershipID   domain.ID
}

func (input ListAPIKeysInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}

type GetAPIKeyInput struct {
	OrganizationID domain.ID
	KeyID          domain.ID
}

func (input GetAPIKeyInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("key ID", input.KeyID),
	})
}

// RevokeAPIKeyInput revokes one key. MembershipID, when set, restricts the
// revocation to that member's own keys so a person without api_key.revoke
// cannot reach anyone else's.
type RevokeAPIKeyInput struct {
	OrganizationID    domain.ID
	KeyID             domain.ID
	MembershipID      domain.ID
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input RevokeAPIKeyInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("key ID", input.KeyID),
		requiredID("actor membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	})
}

type ResolveAPIKeyInput struct {
	Kind       domain.APIKeyKind
	KeyID      domain.ID
	SecretHash []byte
	Now        time.Time
}

func (input ResolveAPIKeyInput) Validate() error {
	return firstError([]error{
		requiredID("key ID", input.KeyID),
		requiredTokenHash("API key secret hash", input.SecretHash),
		requiredTime("current time", input.Now),
	})
}

type ListAPIKeyScopesInput struct{ OrganizationID domain.ID }

func (input ListAPIKeyScopesInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}

// RecordAPIKeyUseInput records authenticated use before dispatch. Route is the
// registered route template, never a raw URL, query, header, or request body.
type RecordAPIKeyUseInput struct {
	Principal domain.Principal
	Method    string
	Route     string
	Now       time.Time
}

func (input RecordAPIKeyUseInput) Validate() error {
	if !domain.ManagementPrincipal(input.Principal) || input.Principal.Auth.Method != domain.AuthMethodAPIKey || input.Principal.Auth.CredentialID == "" {
		return domain.ErrForbidden
	}
	return firstError([]error{requiredString("method", input.Method), requiredString("route", input.Route), requiredTime("current time", input.Now)})
}
