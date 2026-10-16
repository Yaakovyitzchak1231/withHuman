package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
	"github.com/withHumanAI/withHuman/loggermdc"
	"github.com/withHumanAI/withHuman/webapp"
)

func TestObservabilityUsesRequestIDAsTraceID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := NewRouter(Dependencies{Logger: logger})
	request := httptest.NewRequest(http.MethodGet, "/livez", nil)
	request.Header.Set("X-Request-ID", "request-123")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if got := response.Header().Get("X-Request-ID"); got != "request-123" {
		t.Fatalf("got response request ID %q", got)
	}
	records := decodeLogRecords(t, &output)
	if len(records) != 1 || records[0]["request_id"] != "request-123" || records[0]["trace_id"] != "request-123" {
		t.Fatalf("request correlation was not logged: %#v", records)
	}
}

func TestObservabilityGeneratesRequestAndTraceID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := NewRouter(Dependencies{Logger: logger})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/livez", nil))

	requestID := response.Header().Get("X-Request-ID")
	records := decodeLogRecords(t, &output)
	if requestID == "" || records[0]["request_id"] != requestID || records[0]["trace_id"] != requestID {
		t.Fatalf("generated correlation is inconsistent: header=%q log=%#v", requestID, records)
	}
}

type healthApprovalRepository struct {
	application.ApprovalRepository
	err error
}

func (repository healthApprovalRepository) Health(context.Context) error { return repository.err }

type healthOrchestrator struct {
	application.LifecycleOrchestrator
	err error
}

func (orchestrator healthOrchestrator) Health(context.Context) error { return orchestrator.err }

type pipelineReadinessStub struct{ err error }

func (readiness pipelineReadinessStub) Readiness() error { return readiness.err }

func TestReadinessKeepsSynchronousPolicyPathAvailableWithoutTemporal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{
		ApprovalRepo: healthApprovalRepository{},
		Orchestrator: healthOrchestrator{err: errors.New("temporal unavailable")},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"temporal":"degraded"`) {
		t.Fatalf("Temporal outage removed synchronous API readiness: status=%d body=%s", response.Code, response.Body.String())
	}
}

func (pipelineReadinessStub) Grammar() string { return "" }

func TestReadinessWaitsForStartupPipelineCompilation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{
		ApprovalRepo:      healthApprovalRepository{},
		Orchestrator:      healthOrchestrator{},
		CompiledPipelines: pipelineReadinessStub{err: errors.New("internal compiler detail")},
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"compiled_pipelines":"unavailable"`) ||
		strings.Contains(response.Body.String(), "internal compiler detail") {
		t.Fatalf("compiled pipeline readiness was not safely closed: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestReadinessFailsOnceTheDrainBegins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	drain := NewDrain()
	router := NewRouter(Dependencies{
		ApprovalRepo: healthApprovalRepository{},
		Orchestrator: healthOrchestrator{},
		Drain:        drain,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("readiness before the drain: %d %s", response.Code, response.Body.String())
	}

	drain.Begin()
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"shutdown":"draining"`) {
		t.Fatalf("readiness while draining: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("liveness while draining: %d", response.Code)
	}
}

func TestGinContextIsEnrichedAsIdentifiersBecomeKnown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(loggermdc.NewHandler(slog.NewJSONHandler(&output, nil)))
	api := &API{logger: logger}
	router := gin.New()
	router.Use(api.observability())
	router.GET("/requests/:id", func(c *gin.Context) {
		setPrincipal(c, domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", OrganizationID: "org-1", AgentInstanceID: "instance-1"})
		setApprovalRequest(c, domain.ID(c.Param("id")))
		logger.InfoContext(c.Request.Context(), "inside handler")
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/requests/approval-1", nil)
	request.Header.Set("X-Request-ID", "request-1")
	router.ServeHTTP(httptest.NewRecorder(), request)

	for _, record := range decodeLogRecords(t, &output) {
		if record["trace_id"] != "request-1" || record["organization_id"] != "org-1" ||
			record["actor_id"] != "agent-1" || record["agent_instance_id"] != "instance-1" ||
			record["approval_request_id"] != "approval-1" {
			t.Fatalf("log was not fully enriched: %#v", record)
		}
	}
}

func decodeLogRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var records []map[string]any
	for decoder.More() {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestApplicationAPIRouteIsVersionedWithoutProductPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	for _, test := range []struct {
		path   string
		status int
	}{
		{path: "/api/v1/me", status: http.StatusUnauthorized},
		{path: "/api/product/v1/me", status: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("got status %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestAgentRoutesReplacePrototypeDeviceAndServiceAccountRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{method: http.MethodGet, path: "/api/v1/agents", status: http.StatusUnauthorized},
		{method: http.MethodGet, path: "/api/v1/agents/agent-1", status: http.StatusUnauthorized},
		{method: http.MethodPatch, path: "/api/v1/agents/agent-1", status: http.StatusUnauthorized},
		{method: http.MethodDelete, path: "/api/v1/agents/agent-1", status: http.StatusUnauthorized},
		{method: http.MethodPatch, path: "/api/v1/agent_instances/instance-1", status: http.StatusUnauthorized},
		{method: http.MethodPost, path: "/auth/agent_enrollments", status: http.StatusBadRequest},
		{method: http.MethodPost, path: "/auth/agent-enrollments", status: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/v1/agent_enrollments/preview", status: http.StatusUnauthorized},
		{method: http.MethodGet, path: "/api/v1/agent_enrollments/agents", status: http.StatusUnauthorized},
		{method: http.MethodPost, path: "/api/v1/agent_enrollments/authorize", status: http.StatusUnauthorized},
		{method: http.MethodPost, path: "/api/aap/v1/instances", status: http.StatusUnauthorized},
		{method: http.MethodPost, path: "/api/aap/v1/enrollments", status: http.StatusNotFound},
		{method: http.MethodPost, path: "/auth/agent-instances", status: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/v1/agents/agent-1/provisioners", status: http.StatusUnauthorized},
		{method: http.MethodDelete, path: "/api/v1/agent-provisioners/provisioner-1", status: http.StatusUnauthorized},
		{method: http.MethodPost, path: "/api/v1/service-accounts", status: http.StatusNotFound},
		{method: http.MethodPost, path: "/auth/device/code", status: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("got status %d, want %d", response.Code, test.status)
			}
		})
	}
}
func TestRequestApprovalPipelineRoutesAreProductAPIs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/request-approval-pipelines"},
		{method: http.MethodPost, path: "/api/v1/request-approval-pipelines"},
		{method: http.MethodGet, path: "/api/v1/request-approval-pipelines/billing"},
		{method: http.MethodDelete, path: "/api/v1/request-approval-pipelines/billing"},
		{method: http.MethodGet, path: "/api/v1/request-approval-pipelines/billing/uses"},
		{method: http.MethodPost, path: "/api/v1/request-approval-pipelines/billing/preview"},
		{method: http.MethodPost, path: "/api/v1/request-approval-pipelines/billing/revisions"},
		{method: http.MethodGet, path: "/api/v1/request-approval-pipelines/billing/revisions"},
		{method: http.MethodGet, path: "/api/v1/request-approval-pipelines/billing/revisions/1"},
		{method: http.MethodGet, path: "/api/v1/request-approval-pipelines/billing/revisions/1/references"},
		{method: http.MethodPost, path: "/api/v1/request-approval-pipelines/billing/revisions/1/activate"},
		{method: http.MethodGet, path: "/api/v1/request-approval-pipelines/entry"},
		{method: http.MethodPost, path: "/api/v1/request-approval-pipelines/entry/revisions"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
			}
		})
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/aap/v1/request-approval-pipelines", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("pipeline control plane leaked into AAP: status %d", response.Code)
	}
}

func TestRequestApprovalPipelinePreviewRouteIsStrictAndTransient(t *testing.T) {
	repository := &pipelineHTTPRepository{}
	router := pipelineManagementTestRouter(repository, pipelineHTTPPrincipal())
	conditionID := "21aa670a-734c-454c-a1e9-653d54be054b"
	body := `{
		"blocks":[{
			"id":"small-refund","name":"Approve small refunds","type":"branch","enabled":true,
			"timeout":"50ms","max_attempts":1,
			"config":{
				"when":{"id":"` + conditionID + `","field":"/request/arguments/amount_cents","operator":"less_than","value":1000},
				"blocks":[{
					"id":"approve-small-refund","name":"Approve","type":"outcome","enabled":true,"timeout":"50ms","max_attempts":1,
					"config":{"outcome":"approve","reason":"Small refunds are pre-authorized."}
				}],
				"reason":""
			}
		}],
		"sample":{"request":{"tool":"stripe.refund","arguments":{"amount_cents":900}},"agent":{"slug":"billing","name":"billing-agent"}}
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/request-approval-pipelines/billing/preview", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "preview-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d: %s", response.Code, response.Body.String())
	}
	var preview struct {
		Outcome         string `json:"outcome"`
		ReasonCode      string `json:"reason_code"`
		TerminalBlockID string `json:"terminal_block_id"`
		Blocks          []struct {
			BlockID string `json:"block_id"`
			Trace   []struct {
				NodeID string `json:"node_id"`
			} `json:"trace"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Outcome != "approve" || preview.ReasonCode != "block_outcome" || preview.TerminalBlockID != "approve-small-refund" ||
		len(preview.Blocks) != 2 || preview.Blocks[0].BlockID != "small-refund" || len(preview.Blocks[0].Trace) == 0 || preview.Blocks[0].Trace[0].NodeID != conditionID ||
		preview.Blocks[1].BlockID != "approve-small-refund" {
		t.Fatalf("unexpected preview response: %s", response.Body.String())
	}
	if repository.createCalls != 0 || repository.activateCalls != 0 {
		t.Fatal("preview invoked a persistence mutation")
	}

	unknown := strings.Replace(body, `"blocks":`, `"unknown":true,"blocks":`, 1)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/request-approval-pipelines/billing/preview", strings.NewReader(unknown))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"type":"invalid_request"`) || !strings.Contains(response.Body.String(), `"request_id"`) {
		t.Fatalf("strict bind error was inconsistent: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRequestApprovalPipelineActivationRequiresAndChecksIfMatch(t *testing.T) {
	repository := pipelineHTTPRepositoryWithRevision()
	repository.activateErr = domain.ErrPreconditionFailed
	router := pipelineManagementTestRouter(repository, pipelineHTTPPrincipal())

	request := httptest.NewRequest(http.MethodPost, "/api/v1/request-approval-pipelines/billing/revisions/1/activate", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired || !strings.Contains(response.Body.String(), `"code":"if_match_required"`) {
		t.Fatalf("missing If-Match: status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/request-approval-pipelines/billing/revisions/1/activate", nil)
	request.Header.Set("If-Match", `"1"`)
	request.Header.Set("X-Request-ID", "activation-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: status=%d body=%s", response.Code, response.Body.String())
	}
	var errorBody struct {
		Error struct {
			Type      string `json:"type"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &errorBody); err != nil {
		t.Fatal(err)
	}
	if errorBody.Error.Type != "conflict" || errorBody.Error.Code != "active_revision_changed" || errorBody.Error.RequestID != "activation-1" {
		t.Fatalf("unexpected precondition error: %s", response.Body.String())
	}
}

func TestHistoricalPipelineRevisionReturnsOrderedCanonicalBlocks(t *testing.T) {
	repository := pipelineHTTPRepositoryWithRevision()
	router := pipelineManagementTestRouter(repository, pipelineHTTPPrincipal())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/request-approval-pipelines/billing/revisions/1", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Revision int64 `json:"revision"`
		Blocks   []struct {
			ID         string    `json:"id"`
			SnapshotID domain.ID `json:"snapshot_id"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Revision != 1 || len(body.Blocks) != 2 || body.Blocks[0].ID != "first" || body.Blocks[1].ID != "second" {
		t.Fatalf("ordered revision response = %s", response.Body.String())
	}
}

func TestEmailFirstLoginRoutesReplaceSlugLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	for _, test := range []struct {
		path   string
		status int
	}{
		{path: "/auth/login/methods", status: http.StatusBadRequest},
		{path: "/auth/login", status: http.StatusBadRequest},
		{path: "/auth/login/select", status: http.StatusBadRequest},
		{path: "/auth/local/login", status: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("got status %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestFrontendFallbackSeparatesProductRoutesFromAPIs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	(&API{frontend: webapp.Assets()}).installFrontend(router)

	tests := []struct {
		path        string
		status      int
		contentType string
	}{
		{path: "/requests/example", status: http.StatusOK, contentType: "text/html"},
		{path: "/api/missing", status: http.StatusNotFound, contentType: "application/json"},
		{path: "/auth/missing", status: http.StatusNotFound, contentType: "application/json"},
		{path: "/metrics", status: http.StatusNotFound, contentType: "application/json"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("got status %d, want %d", response.Code, test.status)
			}
			if !strings.HasPrefix(response.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("got content type %q, want %q", response.Header().Get("Content-Type"), test.contentType)
			}
		})
	}
}

func TestFrontendShellIsNotIndexed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	(&API{frontend: webapp.Assets()}).installFrontend(router)

	for _, path := range []string{"/", "/signup", "/welcome", "/requests/example"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("got status %d", response.Code)
			}
			if response.Header().Get("X-Robots-Tag") != "noindex" {
				t.Fatalf("got X-Robots-Tag %q, want noindex", response.Header().Get("X-Robots-Tag"))
			}
		})
	}
}

func TestFrontendRouteChunksAreEmbeddedAndImmutable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	(&API{frontend: webapp.Assets()}).installFrontend(router)

	assets, err := fs.Glob(webapp.Assets(), "assets/_*.js")
	if err != nil || len(assets) == 0 {
		t.Fatalf("embedded underscore-prefixed route chunk not found: %v", err)
	}
	response := httptest.NewRecorder()
	assetPath := "/" + assets[0]
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, assetPath, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d", response.Code)
	}
	if response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("unexpected cache policy: %q", response.Header().Get("Cache-Control"))
	}
}

func TestParseWaitCapsAndRejectsInvalidDurations(t *testing.T) {
	if actual := parseWait("90s", 30*time.Second); actual != 30*time.Second {
		t.Fatalf("wait was not capped: %s", actual)
	}
	for _, value := range []string{"invalid", "-1s", ""} {
		if actual := parseWait(value, 30*time.Second); actual != 0 {
			t.Fatalf("%q produced wait %s", value, actual)
		}
	}
}

func TestStrictBindRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/requests", func(c *gin.Context) {
		var request struct {
			Tool string `json:"tool" binding:"required"`
		}
		if !bindStrict(c, &request) {
			return
		}
		c.Status(http.StatusNoContent)
	})

	for _, test := range []struct {
		name   string
		body   string
		status int
	}{
		{name: "valid", body: `{"tool":"deploy"}`, status: http.StatusNoContent},
		{name: "unknown field", body: `{"tool":"deploy","agent_slug":"release"}`, status: http.StatusBadRequest},
		{name: "trailing value", body: `{"tool":"deploy"}{"tool":"second"}`, status: http.StatusBadRequest},
		{name: "required field", body: `{}`, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/requests", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("got status %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestApprovalRepresentationDoesNotExposeInternalPolicyFields(t *testing.T) {
	now := time.Unix(70_000, 0).UTC()
	response := newApprovalResponse(domain.ApprovalRequest{
		ID: "request-1", OrganizationID: "org-1", AgentID: "agent-1", AgentInstanceID: "instance-1",
		AgentName: "release-agent", AgentInstanceName: "runner-1", AgentSlug: "release",
		Tool: "deploy", Arguments: json.RawMessage(`{"version":"1"}`), IdempotencyKey: "secret-key",
		SemanticHash: "semantic-hash", Status: domain.ApprovalApproved, DeadlineAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
		Decision: &domain.Decision{
			ID: "decision-1", RequestID: "request-1", OrganizationID: "org-1",
			Source: domain.DecisionSourcePipeline, Status: domain.ApprovalApproved,
			Channel: "pipeline", Auth: domain.AuthContext{Method: "pipeline", Assurance: "automatic", AuthenticatedAt: now},
			IdempotencyKey: "decision-key", DecidedAt: now,
		},
	})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"agent_id", "agent_type", "idempotency_key", "semantic_hash", "updated_at"} {
		if _, exists := body[field]; exists {
			t.Fatalf("internal field %q crossed the approval response boundary: %s", field, encoded)
		}
	}
	decision := body["decision"].(map[string]any)
	if _, exists := decision["source"]; exists {
		t.Fatalf("decision source crossed the AAP response boundary: %s", encoded)
	}
}

type pipelineHTTPRepository struct {
	application.RequestApprovalPipelineRepository
	revision      domain.RequestApprovalPipelineRevision
	blocks        []domain.RequestApprovalBlock
	createCalls   int
	activateCalls int
	activateErr   error
}

func pipelineHTTPRepositoryWithRevision() *pipelineHTTPRepository {
	now := time.Unix(80_000, 0).UTC()
	firstID := domain.ID("11111111-1111-4111-8111-111111111111")
	secondID := domain.ID("22222222-2222-4222-8222-222222222222")
	return &pipelineHTTPRepository{
		revision: domain.RequestApprovalPipelineRevision{
			ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OrganizationID: "organization-1", PipelineKey: "billing", Name: "Billing",
			Revision: 1, BlockIDs: []domain.ID{firstID, secondID}, CreatedByActorID: "membership-1", CreatedAt: now,
		},
		blocks: []domain.RequestApprovalBlock{
			{
				ID: firstID, OrganizationID: "organization-1", BlockKey: "first", Name: "First", BlockType: "outcome",
				ConfigVersion: 1, Config: json.RawMessage(`{"outcome":"approve","reason":"Approved."}`),
				TimeoutMS: 50, MaxAttempts: 1, ContentHash: strings.Repeat("a", 64), CreatedByActorID: "membership-1", CreatedAt: now,
			},
			{
				ID: secondID, OrganizationID: "organization-1", BlockKey: "second", Name: "Second", BlockType: "outcome",
				ConfigVersion: 1, Config: json.RawMessage(`{"outcome":"human","reason":"Review."}`),
				TimeoutMS: 50, MaxAttempts: 1, ContentHash: strings.Repeat("b", 64), CreatedByActorID: "membership-1", CreatedAt: now,
			},
		},
	}
}

func (repository *pipelineHTTPRepository) GetRequestApprovalPipelineRevision(_ context.Context, input application.GetRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	if repository.revision.OrganizationID != input.OrganizationID || repository.revision.PipelineKey != input.PipelineKey || repository.revision.Revision != input.Revision {
		return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
	}
	return repository.revision, nil
}

func (repository *pipelineHTTPRepository) GetRequestApprovalBlocks(_ context.Context, input application.GetRequestApprovalBlocksInput) ([]domain.RequestApprovalBlock, error) {
	if input.OrganizationID != repository.revision.OrganizationID || len(input.BlockIDs) != len(repository.blocks) {
		return nil, domain.ErrNotFound
	}
	return append([]domain.RequestApprovalBlock(nil), repository.blocks...), nil
}

func (repository *pipelineHTTPRepository) ListActiveRequestApprovalPipelineRevisions(_ context.Context, input application.ListActiveRequestApprovalPipelineRevisionsInput) ([]domain.RequestApprovalPipelineRevision, error) {
	if repository.revision.IsActive && repository.revision.ID > input.AfterID {
		return []domain.RequestApprovalPipelineRevision{repository.revision}, nil
	}
	return []domain.RequestApprovalPipelineRevision{}, nil
}

func (repository *pipelineHTTPRepository) ActivateRequestApprovalPipelineRevision(_ context.Context, _ application.ActivateRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	repository.activateCalls++
	if repository.activateErr != nil {
		return domain.RequestApprovalPipelineRevision{}, repository.activateErr
	}
	revision := repository.revision
	revision.IsActive = true
	return revision, nil
}

func pipelineManagementTestRouter(repository *pipelineHTTPRepository, principal domain.Principal) *gin.Engine {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := &API{
		pipelines: approvalpipeline.NewManagementService(repository, approvalpipeline.NewCompiledPipelineState(repository, approvalpipeline.NewBlockRegistry()), pipelineHTTPClock{}),
		logger:    logger,
	}
	router := gin.New()
	router.Use(api.observability())
	group := router.Group("/api/v1", func(c *gin.Context) {
		setPrincipal(c, principal)
		c.Next()
	})
	api.installRequestApprovalPipelineRoutes(group)
	return router
}

func pipelineHTTPPrincipal() domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "organization-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{
		domain.PermissionPipelineRead, domain.PermissionPipelineWrite, domain.PermissionPipelineActivate,
	}))
}

type pipelineHTTPClock struct{}

func (pipelineHTTPClock) Now() time.Time { return time.Unix(80_000, 0).UTC() }

type runtimeConfigSocialStub struct{ key, label string }

func (stub runtimeConfigSocialStub) Key() string   { return stub.key }
func (stub runtimeConfigSocialStub) Label() string { return stub.label }
func (runtimeConfigSocialStub) Capabilities() []authn.Capability {
	return []authn.Capability{authn.CapabilitySocial}
}
func (runtimeConfigSocialStub) Begin(context.Context, map[string]string) (authn.Flow, error) {
	return authn.Flow{}, nil
}
func (runtimeConfigSocialStub) Complete(context.Context, map[string]string) (authn.Assertion, error) {
	return authn.Assertion{}, nil
}

// The runtime configuration tells the shared frontend which social
// providers this edition registered: an empty list, never a missing field,
// for the open edition.
func TestRuntimeConfigReportsRegisteredSocialProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	read := func(router http.Handler) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/runtime-config", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("got status %d", response.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	body := read(NewRouter(Dependencies{Logger: logger}))
	if providers, ok := body["social_providers"].([]any); !ok || len(providers) != 0 {
		t.Fatalf("open edition must report an empty provider list, got %#v", body["social_providers"])
	}

	auth := authn.NewService(nil, nil, nil, nil, nil, nil, application.RealClock{}, authn.ServiceConfig{})
	auth.RegisterSocialAuthenticator(runtimeConfigSocialStub{key: "google", label: "Google"})
	body = read(NewRouter(Dependencies{Logger: logger, Auth: auth}))
	providers, _ := body["social_providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("registered provider missing: %#v", body["social_providers"])
	}
	if provider, _ := providers[0].(map[string]any); provider["key"] != "google" || provider["label"] != "Google" {
		t.Fatalf("unexpected provider descriptor: %#v", providers[0])
	}
}

func TestApprovalCancellationIsAnAAPRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/aap/v1/requests/11111111-1111-4111-8111-111111111111", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("AAP cancel route: got status %d, want %d", response.Code, http.StatusUnauthorized)
	}
	// Humans decide; they never withdraw an agent's request for it.
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/requests/11111111-1111-4111-8111-111111111111", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("cancel leaked into the product API: status %d", response.Code)
	}
}

func TestAlreadyTerminalHasItsOwnErrorCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/aap/v1/requests/request-1", nil)
	WriteError(c, slog.New(slog.NewTextHandler(io.Discard, nil)), domain.ErrAlreadyTerminal)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != "conflict" || body.Error.Code != "already_terminal" {
		t.Fatalf("error envelope = %+v", body.Error)
	}
}
