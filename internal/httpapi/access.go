package httpapi

// Route access labels. Every route the router registers carries one
// label, the same value its OpenAPI operation declares as
// x-withhuman-permission: a registry permission key, or one of the two
// markers below. The router gates a permission-labelled route on "holds
// it anywhere"; the service behind it checks the permission against the
// resource. The conformance test proves the router and the contract
// carry the same labels.

import (
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

const (
	// AccessPublic marks a route that authenticates itself or needs no
	// principal: health, discovery, the login ceremonies, provider
	// callbacks, and credential minting.
	AccessPublic = "public"
	// AccessSelf marks a route that acts only on the caller's own
	// account or session and therefore needs no permission.
	AccessSelf = "self"
)

// Requires is the router's coarse gate: the principal must hold the
// permission at some scope. It runs after the authentication middleware
// set the principal, and answers the same 403 the services do so the
// client sees one shape whether the router or the service refused.
func Requires(logger *slog.Logger, permission domain.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authorization.HoldsAnywhere(Principal(c), permission) {
			WriteError(c, logger, authorization.Denied{Permission: permission, Resource: domain.OrganizationResource()})
			return
		}
		c.Next()
	}
}

func (a *API) requires(permission domain.Permission) gin.HandlerFunc {
	return Requires(a.logger, permission)
}

// routeKey identifies one registered route by method and absolute path
// in gin's own parameter syntax.
type routeKey struct{ Method, Path string }

// browserOnlyRoutes are the /api/v1 operations a personal API key may not
// call: personal self-service, browser hand-offs to another site, issuing
// or revoking keys, and handing out authority (permission policies and
// invitations), all of which stay a human act with a fresh session. Every other /api/v1
// route accepts a key alongside the session cookie, and the OpenAPI
// contract says which is which; the conformance test keeps the two lists
// equal.
var browserOnlyRoutes = map[routeKey]struct{}{
	{http.MethodPut, "/api/v1/members/:id/role"}:                              {},
	{http.MethodPost, "/api/v1/members/:id/permission_policies"}:              {},
	{http.MethodPut, "/api/v1/members/:id/permission_policies/:policy_id"}:    {},
	{http.MethodDelete, "/api/v1/members/:id/permission_policies/:policy_id"}: {},
	{http.MethodPost, "/api/v1/teams/:id/permission_policies"}:                {},
	{http.MethodPut, "/api/v1/teams/:id/permission_policies/:policy_id"}:      {},
	{http.MethodDelete, "/api/v1/teams/:id/permission_policies/:policy_id"}:   {},
	{http.MethodPost, "/api/v1/invitations"}:                                  {},
	{http.MethodPost, "/api/v1/invitations/:id/resend"}:                       {},
	{http.MethodGet, "/api/v1/api_keys/:id"}:                                  {},
	{http.MethodPost, "/api/v1/me/password"}:                                  {},
	{http.MethodGet, "/api/v1/me/contact-methods"}:                            {},
	{http.MethodPost, "/api/v1/me/contact-methods"}:                           {},
	{http.MethodPost, "/api/v1/me/contact-methods/:id/verify"}:                {},
	{http.MethodPost, "/api/v1/me/contact-methods/:id/resend"}:                {},
	{http.MethodDelete, "/api/v1/me/contact-methods/:id"}:                     {},
	{http.MethodGet, "/api/v1/me/notification-rules"}:                         {},
	{http.MethodPut, "/api/v1/me/notification-rules/:urgency"}:                {},
	{http.MethodPost, "/api/v1/me/discord/link"}:                              {},
	{http.MethodDelete, "/api/v1/me/discord"}:                                 {},
	{http.MethodPost, "/api/v1/me/api_keys"}:                                  {},
	{http.MethodGet, "/api/v1/me/api_keys"}:                                   {},
	{http.MethodDelete, "/api/v1/me/api_keys/:id"}:                            {},
	{http.MethodGet, "/api/v1/api_keys"}:                                      {},
	{http.MethodDelete, "/api/v1/api_keys/:id"}:                               {},
	{http.MethodPost, "/api/v1/agent_enrollments/preview"}:                    {},
	{http.MethodGet, "/api/v1/agent_enrollments/agents"}:                      {},
	{http.MethodPost, "/api/v1/agent_enrollments/authorize"}:                  {},
	{http.MethodPost, "/api/v1/oauth_authorizations/preview"}:                 {},
	{http.MethodPost, "/api/v1/oauth_authorizations"}:                         {},
	{http.MethodPost, "/api/v1/enterprise/slack/install"}:                     {},
	{http.MethodPost, "/api/v1/enterprise/discord/install"}:                   {},
}

// BrowserOnly reports whether the route refuses API keys.
func BrowserOnly(method, path string) bool {
	_, ok := browserOnlyRoutes[routeKey{Method: method, Path: path}]
	return ok
}

// browserOnly refuses a principal that authenticated with an API key,
// answering 403 api_key_not_allowed so a script learns why rather than
// seeing a permission denial.
func (a *API) browserOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if Principal(c).Auth.Method == domain.AuthMethodAPIKey {
			a.writeError(c, domain.ErrAPIKeyNotAllowed)
			return
		}
		c.Next()
	}
}

// handle registers a route on the group and records its access label.
// A permission label prepends the coarse gate to the handler chain;
// public and self routes run their handlers as given. Registering the
// same method and path twice is a programming error.
func (a *API) handle(group *gin.RouterGroup, method, path, access string, handlers ...gin.HandlerFunc) {
	key := routeKey{Method: method, Path: strings.TrimSuffix(group.BasePath(), "/") + path}
	if a.access == nil {
		a.access = make(map[routeKey]string)
	}
	if previous, registered := a.access[key]; registered {
		panic(fmt.Sprintf("httpapi: route %s %s registered twice (first as %q, again as %q)", key.Method, key.Path, previous, access))
	}
	if access != AccessPublic && access != AccessSelf {
		permission := domain.Permission(access)
		if _, known := domain.LookupPermission(permission); !known {
			panic(fmt.Sprintf("httpapi: route %s %s names unknown permission %q", key.Method, key.Path, access))
		}
		handlers = append([]gin.HandlerFunc{a.requires(permission)}, handlers...)
	}
	if BrowserOnly(key.Method, key.Path) {
		handlers = append([]gin.HandlerFunc{a.browserOnly()}, handlers...)
	}
	handlers = append([]gin.HandlerFunc{func(c *gin.Context) {
		if !strings.HasPrefix(key.Path, "/api/v1/") || access == AccessPublic {
			c.Next()
			return
		}
		p := Principal(c)
		if p.ActorType == domain.ActorAPIKey && !OrganizationKeyAllowed(key.Method, key.Path, access) {
			a.writeError(c, domain.ErrAPIKeyNotAllowed)
			return
		}
		c.Next()
	}}, handlers...)
	a.access[key] = access
	group.Handle(method, path, handlers...)
}

// Access lists every registered route with its label.
func (a *API) Access() map[routeKey]string {
	return maps.Clone(a.access)
}

// OrganizationKeyAllowed is an explicit management-plane gate. Human self
// service and unregistered edition permissions are never admitted by default.
func OrganizationKeyAllowed(method, path, access string) bool {
	if method == http.MethodGet && path == "/api/v1/api_keys/verify" {
		return true
	}
	if !strings.HasPrefix(path, "/api/v1/") || (path == "/api/v1/me" || strings.HasPrefix(path, "/api/v1/me/")) || path == "/api/v1/events" || BrowserOnly(method, path) {
		return false
	}
	_, admitted := organizationKeyRoutes[routeKey{Method: method, Path: path}]
	def, ok := domain.LookupPermission(domain.Permission(access))
	return admitted && ok && def.HeldBy(domain.HolderOrganizationAPIKey)
}
