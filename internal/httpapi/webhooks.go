package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/webhookendpoints"
)

// Webhook endpoints are revisioned the same way escalation paths are, and
// the routes have the same shape: a key with append-only revisions, an
// activate with If-Match, a uses listing, and an archive. The key-level
// extras are the signing secret rotation and the per-revision test.
func (a *API) installWebhookEndpointRoutes(router *gin.RouterGroup) {
	read, write := string(domain.PermissionWebhookRead), string(domain.PermissionWebhookWrite)
	a.handle(router, http.MethodGet, "/webhook_endpoints", read, a.listWebhookEndpoints)
	a.handle(router, http.MethodGet, "/webhook_endpoints/:endpoint_key", read, a.getActiveWebhookEndpoint)
	a.handle(router, http.MethodPost, "/webhook_endpoints/:endpoint_key/revisions", write, a.createWebhookEndpointRevision)
	a.handle(router, http.MethodGet, "/webhook_endpoints/:endpoint_key/revisions", read, a.listWebhookEndpointRevisions)
	a.handle(router, http.MethodGet, "/webhook_endpoints/:endpoint_key/revisions/:revision", read, a.getWebhookEndpointRevision)
	a.handle(router, http.MethodPost, "/webhook_endpoints/:endpoint_key/revisions/:revision/activate", write, a.activateWebhookEndpointRevision)
	a.handle(router, http.MethodPost, "/webhook_endpoints/:endpoint_key/revisions/:revision/test", write, a.testWebhookEndpointRevision)
	a.handle(router, http.MethodGet, "/webhook_endpoints/:endpoint_key/uses", read, a.listWebhookEndpointUses)
	a.handle(router, http.MethodDelete, "/webhook_endpoints/:endpoint_key/active", write, a.archiveWebhookEndpoint)
	a.handle(router, http.MethodPost, "/webhook_endpoints/:endpoint_key/rotate_secret", write, a.rotateWebhookEndpointSecret)
}

type webhookEndpointSummaryResponse struct {
	EndpointKey    string `json:"endpoint_key"`
	Name           string `json:"name"`
	URL            string `json:"url"`
	ActiveRevision *int64 `json:"active_revision,omitempty"`
	// ArchivedAt is present while the endpoint is archived. A key without
	// it and without an active revision holds a draft nobody activated.
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
	LatestRevision  int64      `json:"latest_revision"`
	RevisionCount   int64      `json:"revision_count"`
	LatestCreatedAt time.Time  `json:"latest_created_at"`
	SecretRotatedAt time.Time  `json:"secret_rotated_at"`
	// Uses are the active pipeline revisions posting to the key, so the
	// list can mark which endpoints can be archived without a fetch per
	// row.
	Uses []pipelineUseResponse `json:"uses"`
}

type webhookEndpointRevisionResponse struct {
	ID               domain.ID `json:"id"`
	EndpointKey      string    `json:"endpoint_key"`
	Revision         int64     `json:"revision"`
	IsActive         bool      `json:"is_active"`
	Name             string    `json:"name"`
	URL              string    `json:"url"`
	CreatedByActorID domain.ID `json:"created_by_actor_id"`
	CreatedAt        time.Time `json:"created_at"`
	// SigningSecret is present only on the response that created the
	// key's first revision: the one time the secret is shown.
	SigningSecret string `json:"signing_secret,omitempty"`
}

type webhookEndpointSecretResponse struct {
	EndpointKey     string    `json:"endpoint_key"`
	SigningSecret   string    `json:"signing_secret"`
	SecretRotatedAt time.Time `json:"secret_rotated_at"`
}

type webhookEndpointTestView struct {
	StatusCode int    `json:"status_code"`
	DurationMS int64  `json:"duration_ms"`
	Outcome    string `json:"outcome,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Problem    string `json:"problem,omitempty"`
	Message    string `json:"message,omitempty"`
}

func newWebhookEndpointSummaryResponses(listed []webhookendpoints.Listed) []webhookEndpointSummaryResponse {
	responses := make([]webhookEndpointSummaryResponse, len(listed))
	for index, entry := range listed {
		summary := entry.Summary
		responses[index] = webhookEndpointSummaryResponse{
			EndpointKey: summary.EndpointKey, Name: summary.Name, URL: summary.URL,
			ActiveRevision: summary.ActiveRevision, ArchivedAt: summary.ArchivedAt, LatestRevision: summary.LatestRevision,
			RevisionCount: summary.RevisionCount, LatestCreatedAt: summary.LatestCreatedAt,
			SecretRotatedAt: summary.SecretRotatedAt, Uses: newPipelineUseResponses(entry.Uses),
		}
	}
	return responses
}

func newWebhookEndpointRevisionResponse(revision domain.WebhookEndpointRevision) webhookEndpointRevisionResponse {
	return webhookEndpointRevisionResponse{
		ID: revision.ID, EndpointKey: revision.EndpointKey, Revision: revision.Revision,
		IsActive: revision.IsActive, Name: revision.Name, URL: revision.URL,
		CreatedByActorID: revision.CreatedByActorID, CreatedAt: revision.CreatedAt,
	}
}

func newWebhookEndpointRevisionResponses(revisions []domain.WebhookEndpointRevision) []webhookEndpointRevisionResponse {
	responses := make([]webhookEndpointRevisionResponse, len(revisions))
	for index, revision := range revisions {
		responses[index] = newWebhookEndpointRevisionResponse(revision)
	}
	return responses
}

func (a *API) webhookService(c *gin.Context) (*webhookendpoints.Service, bool) {
	if a.webhooks == nil {
		a.writeError(c, domain.ErrWebhookUnconfigured)
		return nil, false
	}
	return a.webhooks, true
}

func (a *API) listWebhookEndpoints(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	summaries, err := service.ListEndpoints(c.Request.Context(), currentPrincipal(c), queryInt(c, "limit", 200))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"webhook_endpoints": newWebhookEndpointSummaryResponses(summaries)})
}

func (a *API) getActiveWebhookEndpoint(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	revision, err := service.GetActiveEndpoint(c.Request.Context(), currentPrincipal(c), c.Param("endpoint_key"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	setActiveRevisionETag(c, revision.Revision)
	c.JSON(http.StatusOK, newWebhookEndpointRevisionResponse(revision))
}

func (a *API) createWebhookEndpointRevision(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	var request struct {
		Name string `json:"name" binding:"required"`
		URL  string `json:"url" binding:"required"`
	}
	if !bindStrict(c, &request) {
		return
	}
	created, err := service.CreateRevision(c.Request.Context(), currentPrincipal(c), c.Param("endpoint_key"), request.Name, request.URL)
	if err != nil {
		a.writeError(c, err)
		return
	}
	response := newWebhookEndpointRevisionResponse(created.Revision)
	response.SigningSecret = created.SigningSecret
	c.JSON(http.StatusCreated, response)
}

func (a *API) listWebhookEndpointRevisions(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	page, err := service.ListRevisions(c.Request.Context(), currentPrincipal(c), c.Param("endpoint_key"), queryInt(c, "limit", 50), c.Query("cursor"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, revisionPageResponse[webhookEndpointRevisionResponse]{
		Revisions: newWebhookEndpointRevisionResponses(page.Revisions), NextCursor: page.NextCursor, TotalCount: page.TotalCount,
	})
}

func (a *API) getWebhookEndpointRevision(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	revisionNumber, err := parsePipelineRevision(c.Param("revision"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	revision, err := service.GetRevision(c.Request.Context(), currentPrincipal(c), c.Param("endpoint_key"), revisionNumber)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, newWebhookEndpointRevisionResponse(revision))
}

func (a *API) activateWebhookEndpointRevision(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	revisionNumber, err := parsePipelineRevision(c.Param("revision"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	expected, err := parseActiveRevisionIfMatch(c.GetHeader("If-Match"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	revision, err := service.ActivateRevision(c.Request.Context(), currentPrincipal(c), c.Param("endpoint_key"), revisionNumber, expected)
	if err != nil {
		a.writeError(c, err)
		return
	}
	setActiveRevisionETag(c, revision.Revision)
	c.JSON(http.StatusOK, newWebhookEndpointRevisionResponse(revision))
}

func (a *API) testWebhookEndpointRevision(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	revisionNumber, err := parsePipelineRevision(c.Param("revision"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	result, err := service.Test(c.Request.Context(), currentPrincipal(c), c.Param("endpoint_key"), revisionNumber)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"test": webhookEndpointTestView{
		StatusCode: result.StatusCode, DurationMS: result.DurationMS, Outcome: string(result.Outcome),
		Reason: result.Reason, Problem: result.Problem, Message: result.Message,
	}})
}

// listWebhookEndpointUses answers the same set that refuses an archive, so
// the page can explain the refusal before the click.
func (a *API) listWebhookEndpointUses(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	uses, err := service.ListUses(c.Request.Context(), currentPrincipal(c), c.Param("endpoint_key"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"uses": newPipelineUseResponses(uses)})
}

func (a *API) archiveWebhookEndpoint(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	expected, err := parseActiveRevisionIfMatch(c.GetHeader("If-Match"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	if expected == nil {
		a.writeError(c, fmt.Errorf("If-Match must identify an active revision: %w", domain.ErrInvalid))
		return
	}
	if err := service.Deactivate(c.Request.Context(), currentPrincipal(c), c.Param("endpoint_key"), *expected); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) rotateWebhookEndpointSecret(c *gin.Context) {
	service, ok := a.webhookService(c)
	if !ok {
		return
	}
	rotated, err := service.RotateSecret(c.Request.Context(), currentPrincipal(c), c.Param("endpoint_key"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, webhookEndpointSecretResponse{
		EndpointKey: rotated.EndpointKey, SigningSecret: rotated.SigningSecret, SecretRotatedAt: rotated.RotatedAt,
	})
}
