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
	"github.com/withHumanAI/withHuman/internal/toolpresentations"
)

type toolPresentationHTTPRepo struct {
	records []domain.ToolPresentation
}

func (r *toolPresentationHTTPRepo) ListToolPresentations(_ context.Context, input application.ListToolPresentationsInput) ([]domain.ToolPresentation, error) {
	out := []domain.ToolPresentation{}
	for _, record := range r.records {
		if record.OrganizationID == input.OrganizationID {
			out = append(out, record)
		}
	}
	return out, nil
}

func (r *toolPresentationHTTPRepo) GetToolPresentation(context.Context, application.GetToolPresentationInput) (domain.ToolPresentation, error) {
	return domain.ToolPresentation{}, domain.ErrNotFound
}

func (r *toolPresentationHTTPRepo) CreateToolPresentation(_ context.Context, input application.CreateToolPresentationInput) (domain.ToolPresentation, error) {
	if err := input.Validate(); err != nil {
		return domain.ToolPresentation{}, err
	}
	for _, record := range r.records {
		if record.OrganizationID == input.OrganizationID && record.Entry.Key() == input.Entry.Key() {
			return domain.ToolPresentation{}, domain.ErrToolPresentationExists
		}
	}
	record := domain.ToolPresentation{ID: input.ID, OrganizationID: input.OrganizationID, Entry: input.Entry, CreatedAt: input.Now, UpdatedAt: input.Now}
	r.records = append(r.records, record)
	return record, nil
}

func (r *toolPresentationHTTPRepo) UpdateToolPresentation(_ context.Context, input application.UpdateToolPresentationInput) (domain.ToolPresentation, error) {
	if err := input.Validate(); err != nil {
		return domain.ToolPresentation{}, err
	}
	for index, record := range r.records {
		if record.ID == input.ID && record.OrganizationID == input.OrganizationID {
			r.records[index].Entry, r.records[index].UpdatedAt = input.Entry, input.Now
			return r.records[index], nil
		}
	}
	return domain.ToolPresentation{}, domain.ErrNotFound
}

func (r *toolPresentationHTTPRepo) DeleteToolPresentation(_ context.Context, input application.DeleteToolPresentationInput) error {
	for index, record := range r.records {
		if record.ID == input.ID && record.OrganizationID == input.OrganizationID {
			r.records = append(r.records[:index], r.records[index+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

type toolPresentationHTTPClock struct{}

func (toolPresentationHTTPClock) Now() time.Time { return time.Unix(90_000, 0).UTC() }

func toolPresentationTestRouter(repo *toolPresentationHTTPRepo, principal domain.Principal) (*gin.Engine, *API) {
	api := &API{
		toolPresentations: toolpresentations.NewService(repo, toolPresentationHTTPClock{}, nil),
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	router := gin.New()
	router.Use(api.observability())
	group := router.Group("/api/v1", func(c *gin.Context) {
		setPrincipal(c, principal)
		c.Next()
	})
	api.installToolPresentationRoutes(group)
	return router, api
}

func jsonRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestToolPresentationsListCarriesTheCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router, _ := toolPresentationTestRouter(&toolPresentationHTTPRepo{}, organizationHTTPPrincipal(domain.PermissionOrganizationRead))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tool_presentations", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Presentations []map[string]any `json:"presentations"`
		BuiltIn       []map[string]any `json:"built_in"`
		Kinds         []string         `json:"kinds"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Presentations == nil || len(body.Presentations) != 0 {
		t.Fatalf("presentations = %v, want an empty array", body.Presentations)
	}
	if len(body.BuiltIn) == 0 || len(body.Kinds) == 0 {
		t.Fatalf("catalog missing: %d built in, %d kinds", len(body.BuiltIn), len(body.Kinds))
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, jsonRequest(http.MethodPost, "/api/v1/tool_presentations", `{"tool":"x","spec":{"fields":[]}}`))
	if response.Code != http.StatusForbidden {
		t.Fatalf("create with organization.read only = %d, want 403", response.Code)
	}
}

func TestToolPresentationsWriteLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &toolPresentationHTTPRepo{}
	router, _ := toolPresentationTestRouter(repo, organizationHTTPPrincipal(domain.PermissionOrganizationSettingsWrite))

	create := `{"server":"crm","tool":"update_deal","spec":{"title":"Deal {{/deal_id}}","fields":[{"path":"/notes","kind":"prose","label":"Notes"},{"kind":"diff","before":"/old","after":"/new"}]}}`
	response := httptest.NewRecorder()
	router.ServeHTTP(response, jsonRequest(http.MethodPost, "/api/v1/tool_presentations", create))
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	if id == "" || created["server"] != "crm" || created["runtime"] != nil {
		t.Fatalf("created = %v", created)
	}
	fields := created["spec"].(map[string]any)["fields"].([]any)
	if fields[0].(map[string]any)["role"] != "primary" {
		t.Fatalf("role not normalized on the wire: %v", fields[0])
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, jsonRequest(http.MethodPost, "/api/v1/tool_presentations", create))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "tool_presentation_exists") {
		t.Fatalf("duplicate = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, jsonRequest(http.MethodPost, "/api/v1/tool_presentations", `{"tool":"y","spec":{"fields":[{"path":"notes","kind":"prose"}]}}`))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "JSON pointer") {
		t.Fatalf("invalid spec = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, jsonRequest(http.MethodPost, "/api/v1/tool_presentations", `{"tool":"y","spec":{"fields":[]},"extra":1}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", response.Code)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, jsonRequest(http.MethodPut, "/api/v1/tool_presentations/"+id, `{"runtime":"codex","tool":"update_deal","spec":{"fields":[]}}`))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"runtime":"codex"`) {
		t.Fatalf("update = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, jsonRequest(http.MethodPut, "/api/v1/tool_presentations/missing", `{"tool":"update_deal","spec":{"fields":[]}}`))
	if response.Code != http.StatusNotFound {
		t.Fatalf("update missing = %d", response.Code)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/tool_presentations/"+id, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/tool_presentations/"+id, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d", response.Code)
	}
}

// The queue representation carries the resolved presentation, and the AAP
// representation never does.
func TestQueueResponsesCarryPresentations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &toolPresentationHTTPRepo{}
	_, api := toolPresentationTestRouter(repo, organizationHTTPPrincipal(domain.PermissionOrganizationRead))
	requests := []domain.ApprovalRequest{
		{ID: "r1", OrganizationID: "organization-1", AgentSlug: "claude-code", Tool: "Bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
		{ID: "r2", OrganizationID: "organization-1", AgentSlug: "claude-code", Tool: "no_such_tool", Arguments: json.RawMessage(`{}`)},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/requests", nil)
	setPrincipal(c, organizationHTTPPrincipal(domain.PermissionRequestRead))
	responses := api.presentMany(c, requests, time.Now())
	if responses[0].Presentation == nil || responses[0].Presentation.Source != toolpresentations.SourceBuiltIn || responses[0].Presentation.Title == "" {
		t.Fatalf("Bash presentation = %+v", responses[0].Presentation)
	}
	if responses[1].Presentation != nil {
		t.Fatalf("unknown tool presented: %+v", responses[1].Presentation)
	}
	payload, _ := json.Marshal(newApprovalResponse(requests[0]))
	if strings.Contains(string(payload), "presentation") {
		t.Fatalf("AAP representation carries a presentation: %s", payload)
	}
	one := api.presentOne(c, requests[0], time.Now())
	if one.Presentation == nil {
		t.Fatalf("presentOne dropped the presentation")
	}
	// Without the service (a composition that never wires it) nothing breaks.
	bare := &API{logger: api.logger}
	if bare.presentMany(c, requests, time.Now())[0].Presentation != nil {
		t.Fatalf("bare API presented")
	}
}
