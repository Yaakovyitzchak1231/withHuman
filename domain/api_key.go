package domain

import (
	"fmt"
	"slices"
	"time"
)

// APIKeyKind separates the two key classes that share the api_keys table.
// A personal key acts as the member who minted it, narrowed to a subset
// of what they may do; an organization key is a principal in its own right
// with explicit grants that outlive its creator.
type APIKeyKind string

const (
	APIKeyPersonal     APIKeyKind = "personal"
	APIKeyOrganization APIKeyKind = "organization"
)

// APIKeyPrefix identifies personal credentials; organization keys use their own prefix.
const APIKeyPrefix = "whk"
const OrganizationAPIKeyPrefix = "who"

// AuthMethodAPIKey is the AuthContext.Method of a principal that
// authenticated with an API key. The decision kernel and the same-origin
// guard branch on it: a key never signs in, so it has no re-authentication
// window, and a bearer header is never ambient, so it needs no CSRF guard.
const AuthMethodAPIKey = "api_key"

// APIKeyStatus is the read-time state of a key.
type APIKeyStatus string

const (
	APIKeyActive  APIKeyStatus = "active"
	APIKeyExpired APIKeyStatus = "expired"
	APIKeyRevoked APIKeyStatus = "revoked"
)

// APIKeyRevocationReason says why a key stopped.
type APIKeyRevocationReason string

const (
	// APIKeyRevokedManually: a person revoked it from the settings page or
	// the API.
	APIKeyRevokedManually APIKeyRevocationReason = "manual"
	// APIKeyRevokedMembershipDeprovisioned: the member was removed from the
	// organization and their keys went with them.
	APIKeyRevokedMembershipDeprovisioned APIKeyRevocationReason = "membership_deprovisioned"
)

// APIKey is one row of api_keys without its secret hash.
type APIKey struct {
	BlockedReason  string     `json:"blocked_reason,omitempty"`
	ID             ID         `json:"id"`
	OrganizationID ID         `json:"organization_id"`
	Kind           APIKeyKind `json:"kind"`
	// MembershipID is the member a personal key acts as; empty for an
	// organization key.
	MembershipID          ID      `json:"membership_id,omitempty"`
	CreatedByMembershipID ID      `json:"created_by_membership_id,omitempty"`
	Grants                []Grant `json:"grants"`
	Name                  string  `json:"name"`
	// Permissions is the narrowing list. Nil means the key inherits
	// everything its member holds (the open edition's only shape); a list
	// means the member's grants are filtered to these keys on every
	// request. The list never grants anything the member lacks.
	Permissions []Permission `json:"permissions"`
	// AssuranceAtIssue is the assurance of the session that minted the
	// key, presented as the key's own assurance so the MFA rule applies
	// through lineage.
	AssuranceAtIssue string `json:"assurance_at_issue"`
	// OAuthClientID and OAuthClientName say which MCP client a personal
	// key was minted for through the consent page; empty for a key the
	// person created by hand.
	OAuthClientID   ID         `json:"oauth_client_id,omitempty"`
	OAuthClientName string     `json:"oauth_client_name,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	// RevokedByMembershipID is who revoked it; empty for a cascade.
	RevokedByMembershipID ID                     `json:"revoked_by_membership_id,omitempty"`
	RevocationReason      APIKeyRevocationReason `json:"revocation_reason,omitempty"`
}

// Status derives the read-time state: revoked wins over expired, and a
// key with no expiry is active until revoked.
func (k APIKey) Status(now time.Time) APIKeyStatus {
	if k.RevokedAt != nil {
		return APIKeyRevoked
	}
	if k.ExpiresAt != nil && !k.ExpiresAt.After(now) {
		return APIKeyExpired
	}
	return APIKeyActive
}

// Prefix is the public, non-secret start of the token, whk_<id>, which is
// what a person sees at the head of the value in their configuration.
func (k APIKey) Prefix() string {
	prefix := APIKeyPrefix
	if k.Kind == APIKeyOrganization {
		prefix = OrganizationAPIKeyPrefix
	}
	return prefix + "_" + string(k.ID)
}

// NarrowGrants keeps the grants whose permission the key lists. A nil
// list means no narrowing. Scope travels with the grant, so a team-scoped
// admin's key stays team-scoped; the list can only remove, never add.
func NarrowGrants(grants []Grant, permissions []Permission) []Grant {
	if permissions == nil {
		return grants
	}
	narrowed := make([]Grant, 0, len(grants))
	for _, grant := range grants {
		if slices.Contains(permissions, grant.Permission) {
			narrowed = append(narrowed, grant)
		}
	}
	return narrowed
}

// APIKeyBearable reports whether a permission may be placed on a personal
// key: a registered key that the registry marks as holdable by an API key.
// The api_key.* family is human-only, so a key can never mint or revoke
// keys.
func APIKeyBearable(permission Permission) bool {
	definition, ok := LookupPermission(permission)
	return ok && definition.HeldBy(HolderAPIKey)
}

// ManagementPrincipal distinguishes management callers from protocol agents.
func ManagementPrincipal(p Principal) bool {
	return p.OrganizationID != "" && ((p.ActorType == ActorHuman && p.MembershipID != "") || (p.ActorType == ActorAPIKey && p.Auth.Method == AuthMethodAPIKey && p.Auth.CredentialID == p.ActorID && p.ActorID != ""))
}

// PermissionAllowedForPrincipal enforces credential holder restrictions at use.
func PermissionAllowedForPrincipal(p Principal, permission Permission) bool {
	d, ok := LookupPermission(permission)
	if !ok {
		return false
	}
	if p.ActorType == ActorAPIKey {
		return p.Auth.Method == AuthMethodAPIKey && d.HeldBy(HolderOrganizationAPIKey)
	}
	if p.Auth.Method == AuthMethodAPIKey {
		return p.ActorType == ActorHuman && d.HeldBy(HolderAPIKey)
	}
	return true
}

func FilterPrincipalGrants(p Principal, grants []Grant) []Grant {
	filtered := make([]Grant, 0, len(grants))
	for _, g := range grants {
		if PermissionAllowedForPrincipal(p, g.Permission) {
			filtered = append(filtered, g)
		}
	}
	return filtered
}

// ManagementActorID references a real member or organization key for provenance.
// It is distinct from audit ActorID, which identifies the user for human events.
func (p Principal) ManagementActorID() ID {
	if p.ActorType == ActorAPIKey {
		return p.ActorID
	}
	return p.MembershipID
}

// ValidateOrganizationKeyPolicies uses the shared policy shape and requires
// exact delegation authority, independently of how a bundle is stored.
func ValidateOrganizationKeyPolicies(actor Principal, policies []PermissionPolicySpec) error {
	if len(policies) == 0 || len(policies) > 20 {
		return fmt.Errorf("choose between 1 and 20 permission policies: %w", ErrInvalid)
	}
	for _, policy := range policies {
		if err := policy.ValidateFor(HolderOrganizationAPIKey); err != nil {
			return err
		}
		for _, scope := range policy.Scopes() {
			for _, permission := range append(append([]Permission{}, policy.Permissions...), PermissionPolicyWrite) {
				covered := false
				for _, held := range actor.Grants {
					if held.Covers(permission, scope.Resource()) {
						covered = true
						break
					}
				}
				if !covered {
					return fmt.Errorf("policy exceeds your delegation authority: %w", ErrForbidden)
				}
			}
		}
	}
	return nil
}
