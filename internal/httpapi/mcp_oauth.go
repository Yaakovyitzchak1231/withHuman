package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/mcpoauth"
)

// OAuth for the management MCP server. The discovery documents, client
// registration and token exchange speak OAuth's own wire shapes (RFC
// 8414, 9728, 7591, 6749), because MCP clients read them; the consent
// endpoints the browser calls are ordinary product API routes.

// MCPAuthorizePath is the consent page, a product route the SPA serves.
const MCPAuthorizePath = "/oauth/authorize"

func (a *API) installMCPOAuthRoutes(root, auth, appAPI *gin.RouterGroup) {
	a.handle(root, http.MethodGet, mcpoauth.AuthorizationServerMetaPath, AccessPublic, a.oauthAuthorizationServerMetadata)
	a.handle(root, http.MethodGet, mcpoauth.ProtectedResourceMetaPathRoot, AccessPublic, a.oauthProtectedResourceMetadata)
	a.handle(root, http.MethodGet, mcpoauth.ProtectedResourceMetaPathRoot+MCPPath, AccessPublic, a.oauthProtectedResourceMetadata)
	a.handle(auth, http.MethodPost, "/oauth/register", AccessPublic, a.oauthRegisterClient)
	a.handle(auth, http.MethodPost, "/oauth/token", AccessPublic, a.oauthToken)
	a.handle(appAPI, http.MethodPost, "/oauth_authorizations/preview", AccessSelf, a.oauthPreviewAuthorization)
	a.handle(appAPI, http.MethodPost, "/oauth_authorizations", string(domain.PermissionAPIKeyIssue), a.oauthAuthorize)
}

// oauthService answers 404 in the OAuth shape when the surface is not
// composed, which only test routers do.
func (a *API) oauthService(c *gin.Context) (*mcpoauth.Service, bool) {
	if a.mcpOAuth == nil {
		writeOAuthError(c, a.logger, &mcpoauth.Error{Code: "invalid_request", Description: "OAuth is not available on this deployment.", Status: http.StatusNotFound})
		return nil, false
	}
	return a.mcpOAuth, true
}

// writeOAuthError answers in RFC 6749's error shape, the one exception to
// the product's error envelope: OAuth clients parse this and nothing
// else. Unexpected failures are logged and answered as server_error.
func writeOAuthError(c *gin.Context, logger *slog.Logger, err error) {
	var refusal *mcpoauth.Error
	if errors.As(err, &refusal) {
		c.Header("Cache-Control", "no-store")
		c.AbortWithStatusJSON(refusal.Status, gin.H{"error": refusal.Code, "error_description": refusal.Description})
		return
	}
	logger.ErrorContext(c.Request.Context(), "oauth request failed", "error", err)
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "An internal error occurred."})
}

func (a *API) oauthAuthorizationServerMetadata(c *gin.Context) {
	service, ok := a.oauthService(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, service.AuthorizationServerMetadata())
}

func (a *API) oauthProtectedResourceMetadata(c *gin.Context) {
	service, ok := a.oauthService(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, service.ProtectedResourceMetadata())
}

// oauthRegisterClient is RFC 7591 dynamic registration: open, because a
// client registers before anyone signs in. Unknown metadata fields are
// ignored, as the RFC says, so the body is decoded leniently.
func (a *API) oauthRegisterClient(c *gin.Context) {
	service, ok := a.oauthService(c)
	if !ok {
		return
	}
	var registration mcpoauth.ClientRegistration
	if err := c.ShouldBindJSON(&registration); err != nil {
		writeOAuthError(c, a.logger, &mcpoauth.Error{Code: "invalid_client_metadata", Description: "The registration body must be a JSON object.", Status: http.StatusBadRequest})
		return
	}
	client, err := service.RegisterClient(c.Request.Context(), registration)
	if err != nil {
		writeOAuthError(c, a.logger, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, client)
}

// oauthToken is the token endpoint: a form-encoded authorization_code
// grant with PKCE, answered with the personal API key as the bearer.
func (a *API) oauthToken(c *gin.Context) {
	service, ok := a.oauthService(c)
	if !ok {
		return
	}
	response, err := service.Exchange(c.Request.Context(), mcpoauth.TokenRequest{
		GrantType: c.PostForm("grant_type"), Code: c.PostForm("code"), RedirectURI: c.PostForm("redirect_uri"),
		ClientID: c.PostForm("client_id"), CodeVerifier: c.PostForm("code_verifier"),
	})
	if err != nil {
		writeOAuthError(c, a.logger, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, response)
}

type oauthAuthorizationRequestBody struct {
	ClientID            string `json:"client_id" binding:"required"`
	RedirectURI         string `json:"redirect_uri" binding:"required"`
	State               string `json:"state"`
	CodeChallenge       string `json:"code_challenge" binding:"required"`
	CodeChallengeMethod string `json:"code_challenge_method" binding:"required"`
	Scope               string `json:"scope"`
	Resource            string `json:"resource"`
	ResponseType        string `json:"response_type"`
}

func (body oauthAuthorizationRequestBody) request() mcpoauth.AuthorizationRequest {
	return mcpoauth.AuthorizationRequest{
		ClientID: body.ClientID, RedirectURI: body.RedirectURI, State: body.State,
		CodeChallenge: body.CodeChallenge, CodeChallengeMethod: body.CodeChallengeMethod,
		Scope: body.Scope, Resource: body.Resource, ResponseType: body.ResponseType,
	}
}

// oauthPreviewAuthorization is what the consent page loads: the client,
// what it asked for, and what the person could grant. It validates the
// whole request so a bad redirect URI is refused before anything shows.
func (a *API) oauthPreviewAuthorization(c *gin.Context) {
	if a.mcpOAuth == nil {
		a.writeError(c, domain.ErrNotFound)
		return
	}
	var body oauthAuthorizationRequestBody
	if !bindStrict(c, &body) {
		return
	}
	consent, err := a.mcpOAuth.Preview(c.Request.Context(), currentPrincipal(c), body.request())
	if err != nil {
		a.writeError(c, err)
		return
	}
	bearable := make([]apiKeyPermissionView, 0, len(consent.Bearable))
	for _, definition := range consent.Bearable {
		bearable = append(bearable, apiKeyPermissionView{Key: definition.Key, Area: definition.Area(), Description: definition.Description, Dangerous: definition.Dangerous})
	}
	c.JSON(http.StatusOK, gin.H{
		"client":                    gin.H{"id": consent.Client.ID, "name": consent.Client.Name, "client_uri": consent.Client.ClientURI, "redirect_uri": body.RedirectURI},
		"requested_permissions":     consent.Requested,
		"bearable_permissions":      bearable,
		"scoping_enabled":           consent.ScopingEnabled,
		"personal_api_keys_allowed": consent.PersonalKeysAllowed,
	})
}

type oauthAuthorizeBody struct {
	oauthAuthorizationRequestBody
	Approve     bool                `json:"approve"`
	Permissions []domain.Permission `json:"permissions"`
	ExpiresAt   *string             `json:"expires_at"`
}

// oauthAuthorize records the person's decision and answers where the
// browser goes next. Approval mints the key under this session, so it
// carries the same freshness and MFA rules as a hand-made key.
func (a *API) oauthAuthorize(c *gin.Context) {
	if a.mcpOAuth == nil {
		a.writeError(c, domain.ErrNotFound)
		return
	}
	var body oauthAuthorizeBody
	if !bindStrict(c, &body) {
		return
	}
	decision := mcpoauth.Decision{AuthorizationRequest: body.request(), Approve: body.Approve, Permissions: body.Permissions}
	if body.ExpiresAt != nil {
		expiresAt, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			a.writeError(c, fmt.Errorf("expires_at must be an RFC 3339 timestamp: %w", domain.ErrInvalid))
			return
		}
		decision.ExpiresAt = &expiresAt
	}
	redirect, err := a.mcpOAuth.Authorize(c.Request.Context(), currentPrincipal(c), decision)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"redirect_url": redirect})
}
