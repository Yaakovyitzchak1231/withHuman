package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/domain"
)

func (a *API) installInstallationRoutes(router *gin.RouterGroup) {
	a.handle(router, http.MethodGet, "/installation", string(domain.PermissionOrganizationSettingsWrite), a.getInstallation)
}

type installationResponse struct {
	ID              domain.ID                 `json:"id"`
	Version         string                    `json:"version"`
	LatestVersion   *string                   `json:"latest_version"`
	UpdateAvailable bool                      `json:"update_available"`
	Advisories      []domain.SecurityAdvisory `json:"advisories"`
	CheckedAt       *time.Time                `json:"checked_at"`
}

func (a *API) getInstallation(c *gin.Context) {
	status, err := a.installation.Read(c.Request.Context(), currentPrincipal(c))
	if err != nil {
		a.writeError(c, err)
		return
	}
	response := installationResponse{
		ID: status.InstallationID, Version: status.Version, UpdateAvailable: status.UpdateAvailable, Advisories: status.Advisories,
	}
	if status.LatestVersion != "" {
		response.LatestVersion = &status.LatestVersion
	}
	if !status.CheckedAt.IsZero() {
		checkedAt := status.CheckedAt.UTC()
		response.CheckedAt = &checkedAt
	}
	c.JSON(http.StatusOK, response)
}
