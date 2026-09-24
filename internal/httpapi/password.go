package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/authn"
)

// Password change is a signed-in action under /api/v1/me; reset is public
// under /auth, where the token in the body is the authorization, the same
// shape as invitation redemption.

func (a *API) changePassword(c *gin.Context) {
	var request struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required"`
	}
	if !bind(c, &request) {
		return
	}
	err := a.auth.ChangePassword(c.Request.Context(), currentPrincipal(c), authn.ChangePasswordInput{
		CurrentPassword: request.CurrentPassword, NewPassword: request.NewPassword,
	})
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) requestPasswordReset(c *gin.Context) {
	var request struct {
		Email string `json:"email" binding:"required"`
	}
	if !bind(c, &request) {
		return
	}
	if err := a.auth.RequestPasswordReset(c.Request.Context(), request.Email); err != nil {
		a.writeError(c, err)
		return
	}
	// Accepted whether or not an account exists: the answer must not say.
	c.Status(http.StatusAccepted)
}

func (a *API) previewPasswordReset(c *gin.Context) {
	var request struct {
		Token string `json:"token" binding:"required"`
	}
	if !bind(c, &request) {
		return
	}
	preview, err := a.auth.PreviewPasswordReset(c.Request.Context(), request.Token)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"expires_at": preview.ExpiresAt})
}

func (a *API) completePasswordReset(c *gin.Context) {
	var request struct {
		Token    string `json:"token" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if !bind(c, &request) {
		return
	}
	if err := a.auth.CompletePasswordReset(c.Request.Context(), request.Token, request.Password); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
