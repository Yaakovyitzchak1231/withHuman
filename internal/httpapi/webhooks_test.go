package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/secrets"
	"github.com/withHumanAI/withHuman/internal/webhookdelivery"
	"github.com/withHumanAI/withHuman/internal/webhookendpoints"
)

func webhookTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	unconfigured := webhookendpoints.NewService(nil, nil, webhookdelivery.Policy{}, nil, application.RealClock{}, logger)
	return NewRouter(Dependencies{Logger: logger, WebhookEndpoints: unconfigured})
}

// Endpoint routes are product surface behind the session guard and never
// appear on the AAP wire. Their shape is the escalation path shape.
func TestWebhookEndpointRoutes(t *testing.T) {
	router := webhookTestRouter()
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/webhook_endpoints"},
		{http.MethodGet, "/api/v1/webhook_endpoints/fraud"},
		{http.MethodPost, "/api/v1/webhook_endpoints/fraud/revisions"},
		{http.MethodGet, "/api/v1/webhook_endpoints/fraud/revisions"},
		{http.MethodGet, "/api/v1/webhook_endpoints/fraud/revisions/1"},
		{http.MethodPost, "/api/v1/webhook_endpoints/fraud/revisions/1/activate"},
		{http.MethodPost, "/api/v1/webhook_endpoints/fraud/revisions/1/test"},
		{http.MethodGet, "/api/v1/webhook_endpoints/fraud/uses"},
		{http.MethodDelete, "/api/v1/webhook_endpoints/fraud/active"},
		{http.MethodPost, "/api/v1/webhook_endpoints/fraud/rotate_secret"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 from the session guard", recorder.Code)
			}
		})
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/aap/v1/webhook_endpoints", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("AAP surface exposes webhook endpoints: %d", recorder.Code)
	}
}

func TestRuntimeConfigReportsWebhookAvailability(t *testing.T) {
	router := webhookTestRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/runtime-config", nil))
	var config map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := config["webhook_enabled"].(bool); !ok || enabled {
		t.Fatalf("webhook_enabled = %v, want false without a secrets key", config["webhook_enabled"])
	}
}

// webhookHTTPRepository is the slice of the endpoint repository the
// management routes exercise, over one key with one revision.
type webhookHTTPRepository struct {
	application.WebhookEndpointRepository
	revision domain.WebhookEndpointRevision
	created  *application.CreateWebhookEndpointRevisionInput
	uses     []domain.PipelineUse
}

func webhookHTTPRepositoryWithRevision() *webhookHTTPRepository {
	return &webhookHTTPRepository{revision: domain.WebhookEndpointRevision{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OrganizationID: "organization-1", EndpointKey: "fraud",
		Revision: 1, IsActive: true, Name: "Fraud service", URL: "https://fraud.example.com/hooks",
		CreatedByActorID: "membership-1", CreatedAt: time.Unix(80_000, 0).UTC(),
	}}
}

func (repository *webhookHTTPRepository) CreateWebhookEndpointRevision(_ context.Context, input application.CreateWebhookEndpointRevisionInput) (application.CreatedWebhookEndpointRevision, error) {
	repository.created = &input
	revision := domain.WebhookEndpointRevision{
		ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", OrganizationID: input.OrganizationID, EndpointKey: input.EndpointKey,
		Revision: 1, Name: input.Name, URL: input.URL, CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
	}
	// The stored key already has a secret; a new key mints one.
	if input.EndpointKey == repository.revision.EndpointKey {
		revision.Revision = 2
		return application.CreatedWebhookEndpointRevision{Revision: revision}, nil
	}
	return application.CreatedWebhookEndpointRevision{Revision: revision, SecretCreated: true}, nil
}

func (repository *webhookHTTPRepository) GetActiveWebhookEndpointRevision(_ context.Context, input application.GetActiveWebhookEndpointRevisionInput) (domain.WebhookEndpointRevision, error) {
	if input.EndpointKey != repository.revision.EndpointKey || !repository.revision.IsActive {
		return domain.WebhookEndpointRevision{}, domain.ErrNotFound
	}
	return repository.revision, nil
}

func (repository *webhookHTTPRepository) GetWebhookEndpointRevision(_ context.Context, input application.GetWebhookEndpointRevisionInput) (domain.WebhookEndpointRevision, error) {
	if input.EndpointKey != repository.revision.EndpointKey || input.Revision != repository.revision.Revision {
		return domain.WebhookEndpointRevision{}, domain.ErrNotFound
	}
	return repository.revision, nil
}

func (repository *webhookHTTPRepository) ActivateWebhookEndpointRevision(_ context.Context, _ application.ActivateWebhookEndpointRevisionInput) (domain.WebhookEndpointRevision, error) {
	revision := repository.revision
	revision.IsActive = true
	return revision, nil
}

func (repository *webhookHTTPRepository) ListWebhookEndpointUses(_ context.Context, input application.ListWebhookEndpointUsesInput) (map[string][]domain.PipelineUse, error) {
	uses := map[string][]domain.PipelineUse{}
	for _, key := range input.EndpointKeys {
		if key == repository.revision.EndpointKey && len(repository.uses) > 0 {
			uses[key] = repository.uses
		}
	}
	return uses, nil
}

func (repository *webhookHTTPRepository) DeactivateWebhookEndpoint(_ context.Context, input application.DeactivateWebhookEndpointInput) (domain.WebhookEndpointRevision, error) {
	if len(repository.uses) > 0 {
		return domain.WebhookEndpointRevision{}, domain.WebhookEndpointInUseError{EndpointKey: input.EndpointKey, Uses: repository.uses}
	}
	revision := repository.revision
	revision.IsActive = false
	return revision, nil
}

func (repository *webhookHTTPRepository) RotateWebhookSigningSecret(_ context.Context, input application.RotateWebhookSigningSecretInput) (application.WebhookSigningSecretState, error) {
	return application.WebhookSigningSecretState{EndpointKey: input.EndpointKey, RotatedAt: input.Now}, nil
}

func (repository *webhookHTTPRepository) OpenWebhookSigningSecret(context.Context, application.OpenWebhookSigningSecretInput) (string, error) {
	return "whsec_stored", nil
}

func (repository *webhookHTTPRepository) RecordWebhookEndpointTest(context.Context, application.RecordWebhookEndpointTestInput) error {
	return nil
}

type webhookHTTPClock struct{}

func (webhookHTTPClock) Now() time.Time { return time.Unix(80_000, 0).UTC() }

func webhookManagementTestRouter(t *testing.T, repository *webhookHTTPRepository, principal domain.Principal) *gin.Engine {
	t.Helper()
	sealer, err := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := &API{
		webhooks: webhookendpoints.NewService(repository, sealer, webhookdelivery.Policy{}, &webhookHTTPDeliverer{}, webhookHTTPClock{}, logger),
		logger:   logger,
	}
	router := gin.New()
	router.Use(api.observability())
	group := router.Group("/api/v1", func(c *gin.Context) {
		setPrincipal(c, principal)
		c.Next()
	})
	api.installWebhookEndpointRoutes(group)
	return router
}

type webhookHTTPDeliverer struct{}

func (webhookHTTPDeliverer) Deliver(context.Context, webhookdelivery.Delivery) (webhookdelivery.Response, error) {
	return webhookdelivery.Response{StatusCode: http.StatusOK, Body: []byte(`{"outcome":"next"}`)}, nil
}

func webhookHTTPPrincipal() domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "organization-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionWebhookRead, domain.PermissionWebhookWrite}))
}

func TestWebhookEndpointRevisionCreationShowsTheSecretOnlyForANewKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := webhookHTTPRepositoryWithRevision()
	router := webhookManagementTestRouter(t, repository, webhookHTTPPrincipal())

	post := func(path, body string) (*httptest.ResponseRecorder, webhookEndpointRevisionResponse) {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		var created webhookEndpointRevisionResponse
		_ = json.Unmarshal(response.Body.Bytes(), &created)
		return response, created
	}
	response, created := post("/api/v1/webhook_endpoints/budget/revisions", `{"name":"Budget check","url":"https://finance.example.com/withhuman"}`)
	if response.Code != http.StatusCreated || created.EndpointKey != "budget" || created.Revision != 1 || created.IsActive || !strings.HasPrefix(created.SigningSecret, "whsec_") {
		t.Fatalf("first revision = %d %s", response.Code, response.Body.String())
	}
	if repository.created == nil || repository.created.Name != "Budget check" || len(repository.created.SealedSigningSecret) == 0 {
		t.Fatalf("repository create input = %+v", repository.created)
	}
	response, created = post("/api/v1/webhook_endpoints/fraud/revisions", `{"name":"Fraud service","url":"https://fraud-v2.example.com/hooks"}`)
	if response.Code != http.StatusCreated || created.Revision != 2 || created.SigningSecret != "" {
		t.Fatalf("later revision must not carry a secret = %d %s", response.Code, response.Body.String())
	}
	if response, _ := post("/api/v1/webhook_endpoints/Fraud%20Service/revisions", `{"name":"x","url":"https://fraud.example.com/"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid key = %d", response.Code)
	}
	if response, _ := post("/api/v1/webhook_endpoints/fraud/revisions", `{"name":"x","url":"http://fraud.example.com/"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("insecure URL = %d", response.Code)
	}
	if response, _ := post("/api/v1/webhook_endpoints/fraud/revisions", `{"name":"x","url":"https://fraud.example.com/","extra":true}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", response.Code)
	}
}

func TestWebhookEndpointRoutesRequirePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	principal := webhookHTTPPrincipal().WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionWebhookRead}))
	router := webhookManagementTestRouter(t, webhookHTTPRepositoryWithRevision(), principal)

	// webhook.read covers every read, so a pipeline author can pick an
	// endpoint by its active revision; every write is webhook.write.
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/webhook_endpoints/fraud", nil))
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("active revision with webhook.read = %d, ETag %q", response.Code, response.Header().Get("ETag"))
	}
	for _, test := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/webhook_endpoints/fraud/rotate_secret"},
		{http.MethodPost, "/api/v1/webhook_endpoints/fraud/revisions"},
		{http.MethodPost, "/api/v1/webhook_endpoints/fraud/revisions/1/activate"},
		{http.MethodPost, "/api/v1/webhook_endpoints/fraud/revisions/1/test"},
		{http.MethodDelete, "/api/v1/webhook_endpoints/fraud/active"},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s without webhook.write = %d, want 403", test.method, test.path, response.Code)
		}
	}
}

func TestWebhookEndpointActivationRequiresAndChecksIfMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := webhookManagementTestRouter(t, webhookHTTPRepositoryWithRevision(), webhookHTTPPrincipal())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/webhook_endpoints/fraud/revisions/1/activate", nil))
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("activation without If-Match = %d, want 428", response.Code)
	}
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/webhook_endpoints/fraud/revisions/1/activate", nil)
	request.Header.Set("If-Match", `"0"`)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("activation = %d, ETag %q", response.Code, response.Header().Get("ETag"))
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/webhook_endpoints/fraud/active", nil)
	request.Header.Set("If-Match", `"0"`)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf(`deactivation with If-Match "0" = %d, want 400`, response.Code)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/webhook_endpoints/fraud/revisions/1/test", nil))
	var tested struct {
		Test webhookEndpointTestView `json:"test"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &tested)
	if response.Code != http.StatusOK || tested.Test.Outcome != "next" || tested.Test.Problem != "" {
		t.Fatalf("test delivery = %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/webhook_endpoints/fraud/rotate_secret", nil))
	var rotated webhookEndpointSecretResponse
	_ = json.Unmarshal(response.Body.Bytes(), &rotated)
	if response.Code != http.StatusOK || rotated.EndpointKey != "fraud" || !strings.HasPrefix(rotated.SigningSecret, "whsec_") {
		t.Fatalf("rotation = %d %s", response.Code, response.Body.String())
	}
}

func TestWebhookEndpointUsesExplainAndRefuseArchive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := webhookHTTPRepositoryWithRevision()
	repository.uses = []domain.PipelineUse{
		{PipelineID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", PipelineKey: "billing", Revision: 3, BlockKey: "fraud-check"},
	}
	router := webhookManagementTestRouter(t, repository, webhookHTTPPrincipal())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/webhook_endpoints/fraud/uses", nil))
	var listed struct {
		Uses []map[string]any `json:"uses"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &listed)
	if response.Code != http.StatusOK || len(listed.Uses) != 1 || listed.Uses[0]["pipeline_key"] != "billing" || listed.Uses[0]["block_key"] != "fraud-check" {
		t.Fatalf("uses = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/webhook_endpoints/fraud/active", nil)
	request.Header.Set("If-Match", `"1"`)
	router.ServeHTTP(response, request)
	var refused struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				EndpointKey string           `json:"endpoint_key"`
				Uses        []map[string]any `json:"uses"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &refused)
	if response.Code != http.StatusConflict || refused.Error.Code != "webhook_endpoint_in_use" || refused.Error.Details.EndpointKey != "fraud" || len(refused.Error.Details.Uses) != 1 {
		t.Fatalf("refusal = %d %s", response.Code, response.Body.String())
	}

	repository.uses = nil
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/webhook_endpoints/fraud/active", nil)
	request.Header.Set("If-Match", `"1"`)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("archive without uses = %d: %s", response.Code, response.Body.String())
	}
}
