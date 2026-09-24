package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

// installPermissionRoutes registers the permission vocabulary and directory mapping.
func (a *API) installPermissionRoutes(router *gin.RouterGroup) {
	read := "self"
	a.handle(router, http.MethodGet, "/permissions", read, a.listPermissions)
	a.handle(router, http.MethodGet, "/enterprise/directory/groups", string(domain.PermissionConnectionRead), a.listDirectoryGroups)
	a.handle(router, http.MethodPut, "/enterprise/directory/groups/:provider_object_id/grant", string(domain.PermissionConnectionWrite), a.setDirectoryGroupGrant)
	a.handle(router, http.MethodDelete, "/enterprise/directory/groups/:provider_object_id/grant", string(domain.PermissionConnectionWrite), a.clearDirectoryGroupGrant)
}

// permissionView is one registry entry as the permission matrix shows it.
type permissionView struct {
	Key         domain.Permission         `json:"key"`
	Area        string                    `json:"area"`
	Description string                    `json:"description"`
	Holders     []domain.PermissionHolder `json:"holders"`
	Resource    domain.ResourceKind       `json:"resource"`
	Dangerous   bool                      `json:"dangerous"`
}

func (a *API) listPermissions(c *gin.Context) {
	principal := currentPrincipal(c)
	if !authorization.HoldsAnywhere(principal, domain.PermissionPolicyRead) && !authorization.HoldsAnywhere(principal, domain.PermissionPolicyWrite) {
		a.writeError(c, domain.ErrForbidden)
		return
	}
	definitions := domain.Permissions()
	views := make([]permissionView, 0, len(definitions))
	for _, def := range definitions {
		views = append(views, permissionView{
			Key: def.Key, Area: def.Area(), Description: def.Description,
			Holders: def.Holders, Resource: def.Resource, Dangerous: def.Dangerous,
		})
	}
	c.JSON(http.StatusOK, gin.H{"permissions": views})
}

func (a *API) listDirectoryGroups(c *gin.Context) {
	links, err := a.auth.ListDirectoryGroupLinks(c.Request.Context(), currentPrincipal(c))
	if err != nil {
		a.writeError(c, err)
		return
	}
	if links == nil {
		links = []domain.DirectoryGroupLink{}
	}
	views := make([]gin.H, 0, len(links))
	for _, link := range links {
		view := gin.H{"provider_object_id": link.ProviderObjectID, "team_id": link.TeamID, "team_name": link.TeamName, "updated_at": link.UpdatedAt, "permission_policy": nil}
		if link.Grant != nil {
			role, err := a.roles.GetRole(c.Request.Context(), application.GetRoleInput{OrganizationID: currentPrincipal(c).OrganizationID, RoleID: link.Grant.RoleID})
			if err != nil {
				a.writeError(c, err)
				return
			}
			ids := []string{}
			if link.Grant.ScopeID != "" {
				ids = append(ids, link.Grant.ScopeID)
			}
			view["permission_policy"] = domain.PermissionPolicySpec{ScopeKind: link.Grant.ScopeKind, ResourceIDs: ids, Permissions: role.Permissions}
		}
		views = append(views, view)
	}
	c.JSON(http.StatusOK, gin.H{"groups": views})
}

func (a *API) setDirectoryGroupGrant(c *gin.Context) {
	var request domain.PermissionPolicySpec
	if !bindStrict(c, &request) {
		return
	}
	specs, err := a.members.BuildPolicyAssignments(c.Request.Context(), currentPrincipal(c), []domain.PermissionPolicySpec{request}, true)
	if err != nil {
		a.writeError(c, err)
		return
	}
	if len(specs) != 1 {
		a.writeError(c, domain.ErrInvalid)
		return
	}
	if err := a.auth.SetDirectoryGroupGrant(c.Request.Context(), currentPrincipal(c), c.Param("provider_object_id"), specs[0]); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) clearDirectoryGroupGrant(c *gin.Context) {
	if err := a.auth.ClearDirectoryGroupGrant(c.Request.Context(), currentPrincipal(c), c.Param("provider_object_id")); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
