package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/domain"
)

// installAPIKeyRoutes registers personal self-service and the oversight
// of both key kinds. Every route but verify is browser-only, so a key can
// never mint or revoke keys. Issuing organization keys (POST /api_keys,
// /api_keys/options) and delegating access to them (their
// /permission_policies) are the hosted edition's routes, mounted on
// core's exported authn and permission-policy seams.
func (a *API) installAPIKeyRoutes(router *gin.RouterGroup) {
	a.handle(router, http.MethodGet, "/me/api_keys", AccessSelf, a.listMyAPIKeys)
	a.handle(router, http.MethodPost, "/me/api_keys", string(domain.PermissionAPIKeyIssue), a.createAPIKey)
	a.handle(router, http.MethodDelete, "/me/api_keys/:id", AccessSelf, a.revokeMyAPIKey)
	a.handle(router, http.MethodGet, "/api_keys/verify", AccessSelf, a.verifyAPIKey)
	a.handle(router, http.MethodGet, "/api_keys/:id", string(domain.PermissionAPIKeyRead), a.getAPIKey)
	a.handle(router, http.MethodGet, "/api_keys", string(domain.PermissionAPIKeyRead), a.listOrganizationAPIKeys)
	a.handle(router, http.MethodDelete, "/api_keys/:id", string(domain.PermissionAPIKeyRevoke), a.revokeOrganizationAPIKey)
}

// apiKeyView is one key as the settings page and the verify call show
// it: never the secret, always the public prefix and the derived status.
type apiKeyView struct {
	ID               domain.ID           `json:"id"`
	Prefix           string              `json:"prefix"`
	Grants           []domain.Grant      `json:"grants"`
	Usable           bool                `json:"usable"`
	BlockedReason    string              `json:"blocked_reason,omitempty"`
	Kind             domain.APIKeyKind   `json:"kind"`
	MembershipID     domain.ID           `json:"membership_id,omitempty"`
	CreatedBy        *apiKeyCreator      `json:"created_by,omitempty"`
	Name             string              `json:"name"`
	Permissions      []domain.Permission `json:"permissions"`
	AssuranceAtIssue string              `json:"assurance_at_issue"`
	// OAuthClient names the MCP client the key was minted for through
	// the consent page; absent for a key made by hand.
	OAuthClient      *apiKeyOAuthClient            `json:"oauth_client,omitempty"`
	Status           domain.APIKeyStatus           `json:"status"`
	CreatedAt        string                        `json:"created_at"`
	ExpiresAt        *string                       `json:"expires_at"`
	LastUsedAt       *string                       `json:"last_used_at"`
	RevokedAt        *string                       `json:"revoked_at"`
	RevocationReason domain.APIKeyRevocationReason `json:"revocation_reason,omitempty"`
}

type apiKeyOAuthClient struct {
	ID   domain.ID `json:"id"`
	Name string    `json:"name"`
}

type apiKeyCreator struct {
	MembershipID domain.ID `json:"membership_id"`
	DisplayName  string    `json:"display_name"`
	Email        string    `json:"email"`
}

func oauthClientView(key domain.APIKey) *apiKeyOAuthClient {
	if key.OAuthClientID == "" {
		return nil
	}
	return &apiKeyOAuthClient{ID: key.OAuthClientID, Name: key.OAuthClientName}
}

func rfc3339Pointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

func toAPIKeyView(key domain.APIKey, now time.Time, creator *apiKeyCreator) apiKeyView {
	return apiKeyView{
		Grants: key.Grants, Usable: key.Status(now) == domain.APIKeyActive && key.BlockedReason == "", BlockedReason: key.BlockedReason, ID: key.ID, Prefix: key.Prefix(), Kind: key.Kind, MembershipID: key.MembershipID, CreatedBy: creator,
		Name: key.Name, Permissions: key.Permissions, AssuranceAtIssue: key.AssuranceAtIssue,
		OAuthClient: oauthClientView(key),
		Status:      key.Status(now),
		CreatedAt:   key.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:   rfc3339Pointer(key.ExpiresAt), LastUsedAt: rfc3339Pointer(key.LastUsedAt), RevokedAt: rfc3339Pointer(key.RevokedAt),
		RevocationReason: key.RevocationReason,
	}
}

// apiKeyPermissionView is one bearable registry entry for the picker.
type apiKeyPermissionView struct {
	Key         domain.Permission `json:"key"`
	Area        string            `json:"area"`
	Description string            `json:"description"`
	Dangerous   bool              `json:"dangerous"`
}

// listMyAPIKeys answers the caller's keys plus what the create form needs:
// whether the organization allows personal keys, whether this edition and
// plan let a key be scoped, and the permissions the caller could place on
// one.
func (a *API) listMyAPIKeys(c *gin.Context) {
	principal := currentPrincipal(c)
	keys, err := a.auth.ListAPIKeys(c.Request.Context(), principal, false)
	if err != nil {
		a.writeError(c, err)
		return
	}
	allowed, err := a.auth.PersonalAPIKeysAllowed(c.Request.Context(), principal.OrganizationID)
	if err != nil {
		a.writeError(c, err)
		return
	}
	scoping, err := a.auth.APIKeyScopingEnabled(c.Request.Context(), principal.OrganizationID)
	if err != nil {
		a.writeError(c, err)
		return
	}
	now := time.Now()
	views := make([]apiKeyView, 0, len(keys))
	for _, key := range keys {
		views = append(views, toAPIKeyView(key, now, nil))
	}
	bearable := make([]apiKeyPermissionView, 0)
	for _, definition := range a.auth.APIKeyBearable(principal) {
		bearable = append(bearable, apiKeyPermissionView{
			Key: definition.Key, Area: definition.Area(), Description: definition.Description, Dangerous: definition.Dangerous,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"keys": views, "personal_api_keys_allowed": allowed, "scoping_enabled": scoping,
		"bearable_permissions": bearable,
	})
}

type createAPIKeyRequest struct {
	Name        string              `json:"name" binding:"required"`
	Permissions []domain.Permission `json:"permissions"`
	ExpiresAt   *string             `json:"expires_at"`
}

func (a *API) createAPIKey(c *gin.Context) {
	var request createAPIKeyRequest
	if !bindStrict(c, &request) {
		return
	}
	var expiresAt *time.Time
	if request.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *request.ExpiresAt)
		if err != nil {
			a.writeError(c, domain.ErrInvalid)
			return
		}
		expiresAt = &parsed
	}
	key, token, err := a.auth.CreateAPIKey(c.Request.Context(), currentPrincipal(c), authn.CreateAPIKeyRequest{
		Name: request.Name, Permissions: request.Permissions, ExpiresAt: expiresAt,
	})
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{"key": toAPIKeyView(key, time.Now(), nil), "token": token})
}

// keyIDParam reads the key id from the path. Anything that is not a
// uuid names no key of the organization's and is answered as absence,
// never as a storage error: in an edition without the hosted issuance
// routes, "options" lands here.
func keyIDParam(c *gin.Context) (domain.ID, error) {
	id := c.Param("id")
	if uuid.Validate(id) != nil {
		return "", domain.ErrNotFound
	}
	return domain.ID(id), nil
}

func (a *API) revokeMyAPIKey(c *gin.Context) {
	id, err := keyIDParam(c)
	if err != nil {
		a.writeError(c, err)
		return
	}
	if err := a.auth.RevokeAPIKey(c.Request.Context(), currentPrincipal(c), id, false); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// verifyAPIKey is the call a script makes first with a fresh key: it
// answers the key's own row and the permissions the key holds right now,
// after narrowing and the member's current roles.
func (a *API) verifyAPIKey(c *gin.Context) {
	principal := currentPrincipal(c)
	key, err := a.auth.VerifyAPIKey(c.Request.Context(), principal)
	if err != nil {
		a.writeError(c, err)
		return
	}
	permissions := principal.Permissions
	if permissions == nil {
		permissions = []domain.Permission{}
	}
	c.JSON(http.StatusOK, gin.H{"key": toAPIKeyView(key, time.Now(), nil), "effective_permissions": permissions, "effective_grants": principal.Grants})
}

func (a *API) getAPIKey(c *gin.Context) {
	id, err := keyIDParam(c)
	if err != nil {
		a.writeError(c, err)
		return
	}
	key, err := a.auth.GetAPIKey(c.Request.Context(), currentPrincipal(c), id)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		apiKeyView
		EffectiveGrants []domain.Grant `json:"effective_grants"`
	}{toAPIKeyView(key, time.Now(), a.apiKeyCreator(c, key)), domain.FilterPrincipalGrants(domain.Principal{ActorType: domain.ActorAPIKey, Auth: domain.AuthContext{Method: domain.AuthMethodAPIKey}}, key.Grants)})
}

// listOrganizationAPIKeys lists both key kinds with their human creators.
func (a *API) listOrganizationAPIKeys(c *gin.Context) {
	principal := currentPrincipal(c)
	keys, err := a.auth.ListAPIKeys(c.Request.Context(), principal, true)
	if err != nil {
		a.writeError(c, err)
		return
	}
	now := time.Now()
	creators := make(map[domain.ID]*apiKeyCreator)
	views := make([]apiKeyView, 0, len(keys))
	kind := domain.APIKeyKind(c.Query("kind"))
	if kind != "" && kind != domain.APIKeyPersonal && kind != domain.APIKeyOrganization {
		a.writeError(c, domain.ErrInvalid)
		return
	}
	for _, key := range keys {
		if kind != "" && key.Kind != kind {
			continue
		}
		creator, seen := creators[key.CreatedByMembershipID]
		if !seen {
			creator = a.apiKeyCreator(c, key)
			creators[key.CreatedByMembershipID] = creator
		}
		views = append(views, toAPIKeyView(key, now, creator))
	}
	c.JSON(http.StatusOK, gin.H{"keys": views})
}

func (a *API) revokeOrganizationAPIKey(c *gin.Context) {
	id, err := keyIDParam(c)
	if err != nil {
		a.writeError(c, err)
		return
	}
	if err := a.auth.RevokeAPIKey(c.Request.Context(), currentPrincipal(c), id, true); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// apiKeyCreator preserves provenance even when a creator no longer has access.
func (a *API) apiKeyCreator(c *gin.Context, key domain.APIKey) *apiKeyCreator {
	if key.CreatedByMembershipID == "" {
		return nil
	}
	creator := &apiKeyCreator{MembershipID: key.CreatedByMembershipID}
	if a.identities == nil {
		return creator
	}
	membership, err := a.identities.GetMembership(c.Request.Context(), application.GetMembershipInput{
		OrganizationID: key.OrganizationID, MembershipID: key.CreatedByMembershipID,
	})
	if err != nil {
		return creator
	}
	if user, err := a.identities.GetUser(c.Request.Context(), application.GetUserInput{UserID: membership.UserID}); err == nil {
		creator.DisplayName, creator.Email = user.DisplayName, user.PrimaryEmail
	}
	return creator
}
