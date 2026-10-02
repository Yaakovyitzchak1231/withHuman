package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// installPermissionPolicyRoutes registers the member and team policy
// collections. An organization key's policies are the hosted edition's
// routes, since delegating access to a key needs issuance authority the
// open edition never grants.
func (a *API) installPermissionPolicyRoutes(router *gin.RouterGroup) {
	for _, collection := range []string{"members", "teams"} {
		base := "/" + collection + "/:id/permission_policies"
		// Read access is resolved per policy, including the member's own
		// inherited access. A coarse gate cannot represent that union.
		a.handle(router, http.MethodGet, base, "self", a.readPermissionPolicies)
		a.handle(router, http.MethodPost, base, string(domain.PermissionPolicyWrite), a.mutatePermissionPolicy)
		a.handle(router, http.MethodGet, base+"/:policy_id", "self", a.readPermissionPolicies)
		a.handle(router, http.MethodPut, base+"/:policy_id", string(domain.PermissionPolicyWrite), a.mutatePermissionPolicy)
		a.handle(router, http.MethodDelete, base+"/:policy_id", string(domain.PermissionPolicyWrite), a.mutatePermissionPolicy)
	}
}

func permissionPolicyTarget(c *gin.Context) application.ReadPermissionPoliciesInput {
	kind := domain.PrincipalMembership
	if strings.HasPrefix(c.FullPath(), "/api/v1/teams/") {
		kind = domain.PrincipalTeam
	}
	return application.ReadPermissionPoliciesInput{Actor: currentPrincipal(c), PrincipalKind: kind, PrincipalID: domain.ID(c.Param("id"))}
}

func (a *API) readPermissionPolicies(c *gin.Context) {
	if a.permissionPolicies == nil {
		a.writeError(c, domain.ErrNotFound)
		return
	}
	page, err := a.permissionPolicies.ReadPermissionPolicies(c.Request.Context(), permissionPolicyTarget(c))
	if err != nil {
		a.writeError(c, err)
		return
	}
	if id := c.Param("policy_id"); id != "" {
		for _, p := range page.Policies {
			if string(p.ID) == id {
				c.JSON(http.StatusOK, p)
				return
			}
		}
		a.writeError(c, domain.ErrNotFound)
		return
	}
	permissions := []permissionView{}
	for _, d := range domain.Permissions() {
		if d.HeldBy(domain.HolderHuman) {
			permissions = append(permissions, permissionView{Key: d.Key, Area: d.Area(), Description: d.Description, Holders: d.Holders, Resource: d.Resource, Dangerous: d.Dangerous})
		}
	}
	c.JSON(http.StatusOK, struct {
		application.PermissionPoliciesPage
		Permissions []permissionView `json:"permissions"`
	}{page, permissions})
}

func (a *API) mutatePermissionPolicy(c *gin.Context) {
	if a.permissionPolicies == nil {
		a.writeError(c, domain.ErrNotFound)
		return
	}
	if !a.config.CustomPermissions {
		a.writeError(c, domain.ErrCustomPermissionsUnavailable)
		return
	}
	input := application.MutatePermissionPolicyInput{ReadPermissionPoliciesInput: permissionPolicyTarget(c), PolicyID: domain.ID(c.Param("policy_id")), IdempotencyKey: c.GetHeader("Idempotency-Key"), Now: time.Now().UTC(), Delete: c.Request.Method == http.MethodDelete}
	if input.Delete {
		input.Revision, _ = strconv.ParseInt(c.Query("revision"), 10, 64)
	} else {
		var body struct {
			domain.PermissionPolicySpec
			Revision int64 `json:"revision"`
		}
		if !bindStrict(c, &body) {
			return
		}
		input.Spec, input.Revision = body.PermissionPolicySpec, body.Revision
	}
	result, err := a.permissionPolicies.MutatePermissionPolicy(c.Request.Context(), input)
	if err != nil {
		a.writeError(c, err)
		return
	}
	if input.Delete {
		c.Status(http.StatusNoContent)
		return
	}
	status := http.StatusOK
	if input.PolicyID == "" {
		status = http.StatusCreated
	}
	c.JSON(status, result)
}
