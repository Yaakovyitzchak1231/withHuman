package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/domain"
)

// The AAP instance endpoints authenticate with a provisioner bearer
// (whp_) themselves, so they sit outside the instance-credential group.

type deliveryRequest struct {
	Type           string `json:"type"`
	URL            string `json:"url"`
	RegistrationID string `json:"registration_id"`
	SigningSecret  string `json:"signing_secret"`
}

func (delivery deliveryRequest) proposed() *application.ProposedInstanceDelivery {
	return &application.ProposedInstanceDelivery{
		Type: delivery.Type, URL: delivery.URL,
		RegistrationID: domain.ID(delivery.RegistrationID), SigningSecret: delivery.SigningSecret,
	}
}

func provisionerBearer(c *gin.Context) (string, bool) {
	bearer := c.GetHeader("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(bearer, "Bearer ")), true
}

func (a *API) mintAgentInstance(c *gin.Context) {
	token, ok := provisionerBearer(c)
	if !ok {
		a.writeError(c, domain.ErrUnauthenticated)
		return
	}
	var request struct {
		InstanceName     string           `json:"instance_name"`
		InstanceMetadata json.RawMessage  `json:"instance_metadata"`
		TTLSeconds       int64            `json:"ttl_seconds"`
		Delivery         *deliveryRequest `json:"delivery"`
	}
	if c.Request.ContentLength != 0 && !bindStrict(c, &request) {
		return
	}
	mint := authn.MintInstanceRequest{
		Token: token, IdempotencyKey: c.GetHeader("Idempotency-Key"),
		InstanceName: request.InstanceName, Metadata: request.InstanceMetadata, TTLSeconds: request.TTLSeconds,
	}
	if request.Delivery != nil {
		mint.Delivery = request.Delivery.proposed()
	}
	result, err := a.auth.MintAgentInstance(c.Request.Context(), mint)
	if err != nil {
		a.writeError(c, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	c.JSON(status, result.AgentInstanceCredential)
}

func (a *API) getProvisionedInstance(c *gin.Context) {
	token, ok := provisionerBearer(c)
	if !ok {
		a.writeError(c, domain.ErrUnauthenticated)
		return
	}
	instance, err := a.auth.GetProvisionedInstance(c.Request.Context(), token, domain.ID(c.Param("id")))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, instance)
}

// updateProvisionedInstance tells an absent delivery (unchanged) from an
// explicit null (remove the receiver), so it reads delivery raw.
func (a *API) updateProvisionedInstance(c *gin.Context) {
	token, ok := provisionerBearer(c)
	if !ok {
		a.writeError(c, domain.ErrUnauthenticated)
		return
	}
	var request struct {
		InstanceName *string         `json:"instance_name"`
		Delivery     json.RawMessage `json:"delivery"`
	}
	if !bindStrict(c, &request) {
		return
	}
	update := authn.UpdateInstanceRequest{
		Token: token, IdempotencyKey: c.GetHeader("Idempotency-Key"),
		InstanceID: domain.ID(c.Param("id")), Name: request.InstanceName,
	}
	switch {
	case len(request.Delivery) == 0:
	case bytes.Equal(bytes.TrimSpace(request.Delivery), []byte("null")):
		update.DeliveryChange = application.DeliveryRemoved
	default:
		var delivery deliveryRequest
		decoder := json.NewDecoder(bytes.NewReader(request.Delivery))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&delivery); err != nil {
			writeAPIError(c, http.StatusBadRequest, "invalid_request", "invalid_request", fmt.Sprintf("delivery: %v", err), nil)
			return
		}
		update.DeliveryChange, update.Delivery = application.DeliveryReplaced, delivery.proposed()
	}
	if update.Name == nil && update.DeliveryChange == application.DeliveryUnchanged {
		writeAPIError(c, http.StatusBadRequest, "invalid_request", "invalid_request", "supply instance_name, delivery, or both", nil)
		return
	}
	instance, err := a.auth.UpdateProvisionedInstance(c.Request.Context(), update)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, instance)
}

// deleteProvisionedInstance retires the instance, then stands down the
// workflows of the requests the deletion cancelled.
func (a *API) deleteProvisionedInstance(c *gin.Context) {
	token, ok := provisionerBearer(c)
	if !ok {
		a.writeError(c, domain.ErrUnauthenticated)
		return
	}
	organizationID, cancelled, err := a.auth.DeleteProvisionedInstance(c.Request.Context(), token, domain.ID(c.Param("id")))
	if err != nil {
		a.writeError(c, err)
		return
	}
	a.approvals.SignalCancelled(c.Request.Context(), organizationID, cancelled)
	c.Status(http.StatusNoContent)
}
