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
	"github.com/withHumanAI/withHuman/internal/reviewrouting"
)

func TestRoutingRoutesAreProductAPIs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/escalation-paths"},
		{method: http.MethodPost, path: "/api/v1/escalation-paths"},
		{method: http.MethodGet, path: "/api/v1/escalation-paths/payments"},
		{method: http.MethodDelete, path: "/api/v1/escalation-paths/payments"},
		{method: http.MethodPost, path: "/api/v1/escalation-paths/payments/revisions"},
		{method: http.MethodGet, path: "/api/v1/escalation-paths/payments/revisions"},
		{method: http.MethodGet, path: "/api/v1/escalation-paths/payments/revisions/1"},
		{method: http.MethodPost, path: "/api/v1/escalation-paths/payments/revisions/1/activate"},
		{method: http.MethodGet, path: "/api/v1/escalation-paths/payments/uses"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
			}
		})
	}

	// The routing policy surface is gone, not merely unauthorized.
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/routing-policy", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("routing policy surface still routed: status %d", response.Code)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/aap/v1/escalation-paths", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("routing control plane leaked into AAP: status %d", response.Code)
	}
}

const routingHTTPPathDocument = `{"working_hours":[{"id":"uk-office","name":"UK office hours","timezone":"Europe/London","intervals":[{"weekdays":["mon","tue","wed","thu","fri"],"start":"09:00","end":"18:00"}]}],"nodes":[{"id":"41111111-1111-4111-8111-111111111111","type":"level","targets":[{"type":"user","id":"51111111-1111-4111-8111-111111111111"}],"escalate_after":"15m"},{"id":"42222222-2222-4222-8222-222222222222","type":"if_else","conditions":[{"type":"request","when":{"id":"43333333-3333-4333-8333-333333333333","field":"/request/tool","operator":"starts_with","value":["stripe."]}}],"then":[{"id":"44444444-4444-4444-8444-444444444444","type":"defer","working_hours_id":"uk-office"}]}]}`

type routingHTTPPathRepository struct {
	application.EscalationPathRepository
	revision domain.EscalationPathRevision
	created  *application.CreateEscalationPathRevisionInput
	// createdPath is the CreateEscalationPath input; archived the key
	// ArchiveEscalationPath was given.
	createdPath *application.CreateEscalationPathInput
	archived    string
	listed      *application.ListEscalationPathSummariesInput
	uses        []domain.PipelineUse
}

func routingHTTPPathRepositoryWithRevision() *routingHTTPPathRepository {
	return &routingHTTPPathRepository{
		revision: domain.EscalationPathRevision{
			ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OrganizationID: "organization-1",
			PathKey: "payments", Revision: 1, Name: "Payments escalation path",
			Document: json.RawMessage(routingHTTPPathDocument), DocumentVersion: 2,
			CreatedByActorID: "membership-1", CreatedAt: time.Unix(80_000, 0).UTC(),
		},
	}
}

func (repository *routingHTTPPathRepository) CreateEscalationPathRevision(_ context.Context, input application.CreateEscalationPathRevisionInput) (domain.EscalationPathRevision, error) {
	repository.created = &input
	return domain.EscalationPathRevision{
		ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", OrganizationID: input.OrganizationID,
		PathKey: input.PathKey, Revision: 2, Name: input.Name, Document: input.Document,
		DocumentVersion: input.DocumentVersion, CreatedByActorID: input.CreatedByActorID,
		CreatedAt: input.Now,
	}, nil
}

func (repository *routingHTTPPathRepository) ListEscalationPathSummaries(_ context.Context, input application.ListEscalationPathSummariesInput) ([]domain.EscalationPathSummary, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	repository.listed = &input
	archivedAt := time.Unix(90_000, 0).UTC()
	summaries := []domain.EscalationPathSummary{{PathKey: "payments", Name: "Payments escalation path", LatestRevision: 1, RevisionCount: 1, LatestCreatedAt: repository.revision.CreatedAt}}
	if input.Status != application.EscalationPathStatusLive && input.Status != "" {
		summaries = append(summaries, domain.EscalationPathSummary{PathKey: "old", Name: "Old", LatestRevision: 2, RevisionCount: 2, LatestCreatedAt: repository.revision.CreatedAt, ArchivedAt: &archivedAt})
	}
	return summaries, nil
}

func (repository *routingHTTPPathRepository) CreateEscalationPath(_ context.Context, input application.CreateEscalationPathInput) (domain.EscalationPathRevision, error) {
	if input.PathKey == repository.revision.PathKey {
		return domain.EscalationPathRevision{}, domain.ErrEscalationPathExists
	}
	repository.createdPath = &input
	return domain.EscalationPathRevision{
		ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", OrganizationID: input.OrganizationID,
		PathKey: input.PathKey, Revision: 1, Name: input.Name, Document: input.Document,
		DocumentVersion: input.DocumentVersion, CreatedByActorID: input.CreatedByActorID,
		CreatedAt: input.Now,
	}, nil
}

func (repository *routingHTTPPathRepository) ArchiveEscalationPath(_ context.Context, input application.ArchiveEscalationPathInput) ([]domain.EscalationPathRevision, error) {
	if input.PathKey != repository.revision.PathKey {
		return nil, domain.ErrNotFound
	}
	if len(repository.uses) > 0 {
		return nil, domain.EscalationPathInUseError{PathKey: input.PathKey, Uses: repository.uses}
	}
	repository.archived = input.PathKey
	return []domain.EscalationPathRevision{repository.revision}, nil
}

func (repository *routingHTTPPathRepository) GetEscalationPathRevision(_ context.Context, input application.GetEscalationPathRevisionInput) (domain.EscalationPathRevision, error) {
	if repository.revision.OrganizationID != input.OrganizationID || repository.revision.PathKey != input.PathKey || repository.revision.Revision != input.Revision {
		return domain.EscalationPathRevision{}, domain.ErrNotFound
	}
	return repository.revision, nil
}

func (repository *routingHTTPPathRepository) ActivateEscalationPathRevision(_ context.Context, _ application.ActivateEscalationPathRevisionInput) (domain.EscalationPathRevision, error) {
	revision := repository.revision
	revision.IsActive = true
	return revision, nil
}

func (repository *routingHTTPPathRepository) ListEscalationPathUses(_ context.Context, input application.ListEscalationPathUsesInput) ([]domain.PipelineUse, error) {
	if input.PathKey != repository.revision.PathKey {
		return []domain.PipelineUse{}, nil
	}
	return repository.uses, nil
}

func (repository *routingHTTPPathRepository) ResolveRoutingTargets(_ context.Context, _ application.ResolveRoutingTargetsInput) (application.ResolvedRoutingTargets, error) {
	return application.ResolvedRoutingTargets{}, nil
}

// routingHTTPTeams answers the roster warning with a fixed count.
type routingHTTPTeams struct {
	lacking int
}

func (t routingHTTPTeams) GetTeam(_ context.Context, input application.GetTeamInput) (domain.Team, []application.TeamMemberRecord, error) {
	return domain.Team{ID: input.TeamID, OrganizationID: input.OrganizationID, Name: "Payments"}, nil, nil
}

func (t routingHTTPTeams) CountTeamMembersLacking(context.Context, application.CountTeamMembersLackingInput) (application.TeamMemberPermissionCount, error) {
	return application.TeamMemberPermissionCount{MemberCount: 3, LackingCount: t.lacking}, nil
}

type routingHTTPClock struct{}

func (routingHTTPClock) Now() time.Time { return time.Unix(80_000, 0).UTC() }

func routingManagementTestRouter(repository *routingHTTPPathRepository, principal domain.Principal) *gin.Engine {
	return routingManagementTestRouterWithTeams(repository, routingHTTPTeams{}, principal)
}

func routingManagementTestRouterWithTeams(repository *routingHTTPPathRepository, teams routingHTTPTeams, principal domain.Principal) *gin.Engine {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := &API{
		routing: reviewrouting.NewManagementService(repository, teams, routingHTTPClock{}),
		logger:  logger,
	}
	router := gin.New()
	router.Use(api.observability())
	group := router.Group("/api/v1", func(c *gin.Context) {
		setPrincipal(c, principal)
		c.Next()
	})
	api.installRoutingRoutes(group)
	return router
}

func routingHTTPPrincipal() domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "organization-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{
		domain.PermissionEscalationPathRead, domain.PermissionEscalationPathWrite, domain.PermissionEscalationPathActivate,
	}))
}

func TestEscalationPathRevisionCreationValidatesAndReturnsDocument(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := routingHTTPPathRepositoryWithRevision()
	router := routingManagementTestRouter(repository, routingHTTPPrincipal())

	body := `{"name":"Payments escalation path",` + routingHTTPPathDocument[1:]
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/escalation-paths/payments/revisions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	var created escalationPathRevisionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.PathKey != "payments" || created.Revision != 2 || len(created.Nodes) != 2 ||
		created.Nodes[0].EscalateAfter != "15m" || created.DocumentVersion != 2 ||
		len(created.Nodes[1].Conditions) != 1 || created.Nodes[1].Conditions[0].When == nil {
		t.Fatalf("unexpected creation response: %s", response.Body.String())
	}
	if repository.created == nil || repository.created.OrganizationID != "organization-1" || repository.created.Name != "Payments escalation path" {
		t.Fatalf("repository create input = %+v", repository.created)
	}
	if created.Warnings == nil || len(created.Warnings) != 0 {
		t.Fatalf("a document without team targets carries an empty warnings array: %s", response.Body.String())
	}

	invalid := `{"name":"Broken","working_hours":[],"nodes":[{"id":"41111111-1111-4111-8111-111111111111","type":"defer","working_hours_id":"missing"}]}`
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/escalation-paths/payments/revisions", strings.NewReader(invalid))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid document = %d, want 400", response.Code)
	}
}

func TestEscalationDocumentsRejectChannelAnnouncements(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name string
		node string
	}{
		{"removed node type", `{"id":"41111111-1111-4111-8111-111111111111","type":"notify_channel"}`},
		{"removed channel field", `{"id":"41111111-1111-4111-8111-111111111111","type":"level","targets":[{"type":"user","id":"51111111-1111-4111-8111-111111111111"}],"escalate_after":"15m","channel":{"provider":"slack","id":"C1"}}`},
	} {
		for _, nested := range []bool{false, true} {
			nodes := test.node
			location := "top level"
			if nested {
				location = "nested"
				nodes = `{"id":"42222222-2222-4222-8222-222222222222","type":"if_else","conditions":[{"type":"urgency","urgency":"interrupt"}],"then":[` + nodes + `]}`
			}
			document := `{"working_hours":[],"nodes":[` + nodes + `]}`
			for _, endpoint := range []string{"create path", "path revision", "team policy"} {
				t.Run(test.name+"/"+location+"/"+endpoint, func(t *testing.T) {
					paths := routingHTTPPathRepositoryWithRevision()
					policies := teamEscalationHTTPPoliciesWithRevision()
					router := routingManagementTestRouter(paths, routingHTTPPrincipal())
					path := "/api/v1/escalation-paths/payments/revisions"
					body := `{"name":"Payments",` + document[1:]
					switch endpoint {
					case "create path":
						path = "/api/v1/escalation-paths"
						body = `{"path_key":"new-path",` + body[1:]
					case "team policy":
						router = teamEscalationTestRouter(policies, &teamEscalationHTTPTeams{}, teamEscalationHTTPPrincipal())
						path = teamEscalationPolicyPath("/revisions")
						body = document
					}
					response := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(response, request)
					if response.Code != http.StatusBadRequest {
						t.Fatalf("removed feature accepted: %d %s", response.Code, response.Body.String())
					}
					if paths.createdPath != nil || paths.created != nil || policies.created != nil {
						t.Fatal("invalid document reached persistence")
					}
				})
			}
		}
	}
}

func TestRoutingRoutesRequirePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	principal := routingHTTPPrincipal()
	principal = principal.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionPipelineWrite}))
	router := routingManagementTestRouter(routingHTTPPathRepositoryWithRevision(), principal)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/escalation-paths", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("without escalation_path.read = %d, want 403", response.Code)
	}
}

func TestEscalationPathActivationRequiresAndChecksIfMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := routingManagementTestRouter(routingHTTPPathRepositoryWithRevision(), routingHTTPPrincipal())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/escalation-paths/payments/revisions/1/activate", nil))
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("activation without If-Match = %d, want 428", response.Code)
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/escalation-paths/payments/revisions/1/activate", nil)
	request.Header.Set("If-Match", `"0"`)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("activation = %d, ETag %q", response.Code, response.Header().Get("ETag"))
	}
}

// The revision response carries the path editor's roster warning with
// the team named, and the write still succeeds.
func TestEscalationPathRevisionResponseCarriesTeamWarnings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := routingHTTPPathRepositoryWithRevision()
	router := routingManagementTestRouterWithTeams(repository, routingHTTPTeams{lacking: 2}, routingHTTPPrincipal())

	body := `{"name":"Payments","working_hours":[],"nodes":[{"id":"41111111-1111-4111-8111-111111111111","type":"level","targets":[{"type":"team","id":"61111111-1111-4111-8111-111111111111"}],"escalate_after":"15m"}]}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/escalation-paths/payments/revisions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	var created struct {
		Warnings []map[string]any `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(created.Warnings) != 1 {
		t.Fatalf("warnings = %v", created.Warnings)
	}
	warning := created.Warnings[0]
	if warning["code"] != "team_members_cannot_decide" || warning["team_id"] != "61111111-1111-4111-8111-111111111111" ||
		warning["team_name"] != "Payments" || warning["count"] != float64(2) || warning["member_count"] != float64(3) {
		t.Fatalf("warning = %v", warning)
	}
}

// A path some request walks without meeting a level reports the
// reachability warning, which names no team.
func TestEscalationPathRevisionResponseCarriesReachabilityWarning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := routingHTTPPathRepositoryWithRevision()
	router := routingManagementTestRouter(repository, routingHTTPPrincipal())

	body := `{"name":"Payments","working_hours":[],"nodes":[{"id":"41111111-1111-4111-8111-111111111111","type":"if_else",` +
		`"conditions":[{"type":"request","when":{"id":"51111111-1111-4111-8111-111111111111","field":"/request/tool","operator":"equals","value":"deploy"}}],` +
		`"then":[{"id":"42111111-1111-4111-8111-111111111111","type":"level","targets":[{"type":"user","id":"61111111-1111-4111-8111-111111111111"}],"escalate_after":"15m"}]}]}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/escalation-paths/payments/revisions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	var created struct {
		Warnings []map[string]any `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(created.Warnings) != 1 || created.Warnings[0]["code"] != "path_may_reach_nobody" {
		t.Fatalf("warnings = %v", created.Warnings)
	}
	if _, present := created.Warnings[0]["team_id"]; present {
		t.Fatalf("reachability warning must carry no team fields: %v", created.Warnings[0])
	}
}

func TestEscalationPathUsesExplainAndRefuseArchive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := routingHTTPPathRepositoryWithRevision()
	repository.uses = []domain.PipelineUse{
		{PipelineID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", PipelineKey: "billing", Revision: 3, BlockKey: "manual-review"},
		{PipelineID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", PipelineKey: "entry", Revision: 1},
	}
	router := routingManagementTestRouter(repository, routingHTTPPrincipal())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/escalation-paths/payments/uses", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("uses = %d: %s", response.Code, response.Body.String())
	}
	var listed struct {
		Uses []map[string]any `json:"uses"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Uses) != 2 || listed.Uses[0]["pipeline_key"] != "billing" || listed.Uses[0]["block_key"] != "manual-review" {
		t.Fatalf("block use = %+v", listed.Uses)
	}
	if listed.Uses[1]["pipeline_key"] != "entry" || listed.Uses[1]["block_key"] != nil || listed.Uses[1]["scope"] != nil {
		t.Fatalf("default use should omit block_key: %+v", listed.Uses[1])
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/escalation-paths/payments", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("archive while used = %d: %s", response.Code, response.Body.String())
	}
	var refused struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				PathKey string           `json:"path_key"`
				Uses    []map[string]any `json:"uses"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Error.Code != "escalation_path_in_use" || refused.Error.Details.PathKey != "payments" || len(refused.Error.Details.Uses) != 2 {
		t.Fatalf("refusal = %s", response.Body.String())
	}

	repository.uses = nil
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/escalation-paths/payments", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("archive without uses = %d: %s", response.Code, response.Body.String())
	}
}

func TestEscalationPathCreateIsExplicit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := routingHTTPPathRepositoryWithRevision()
	router := routingManagementTestRouter(repository, routingHTTPPrincipal())
	post := func(body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/escalation-paths", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		return response
	}

	response := post(`{"path_key":"refunds","name":"Refunds",` + routingHTTPPathDocument[1:])
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	var created escalationPathRevisionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.PathKey != "refunds" || created.Revision != 1 || created.IsActive || created.Name != "Refunds" || len(created.Nodes) != 2 {
		t.Fatalf("unexpected creation response: %s", response.Body.String())
	}
	if repository.createdPath == nil || repository.createdPath.PathKey != "refunds" || repository.createdPath.CreatedByActorID != "membership-1" {
		t.Fatalf("repository create input = %+v", repository.createdPath)
	}

	// The key must be in the body; the document is validated as for a
	// revision; an existing key is a conflict, not a new revision.
	if response := post(`{"name":"Refunds",` + routingHTTPPathDocument[1:]); response.Code != http.StatusBadRequest {
		t.Fatalf("create without path_key = %d, want 400", response.Code)
	}
	if response := post(`{"path_key":"Not Valid","name":"Refunds",` + routingHTTPPathDocument[1:]); response.Code != http.StatusBadRequest {
		t.Fatalf("create with an invalid key = %d, want 400", response.Code)
	}
	if response := post(`{"path_key":"refunds","name":"Refunds","working_hours":[],"nodes":[]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("create without nodes = %d, want 400", response.Code)
	}
	response = post(`{"path_key":"payments","name":"Payments",` + routingHTTPPathDocument[1:])
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"escalation_path_exists"`) {
		t.Fatalf("create on an existing key = %d %s, want 409 escalation_path_exists", response.Code, response.Body.String())
	}
}

func TestEscalationPathArchiveRefusesUsesAndUnknownKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := routingHTTPPathRepositoryWithRevision()
	repository.uses = []domain.PipelineUse{{PipelineID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", Revision: 1}}
	router := routingManagementTestRouter(repository, routingHTTPPrincipal())
	archive := func(pathKey string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/escalation-paths/"+pathKey, nil))
		return response
	}

	response := archive("payments")
	if response.Code != http.StatusConflict {
		t.Fatalf("archive while used = %d: %s", response.Code, response.Body.String())
	}
	var refused struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				PathKey string           `json:"path_key"`
				Uses    []map[string]any `json:"uses"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Error.Code != "escalation_path_in_use" || refused.Error.Details.PathKey != "payments" || len(refused.Error.Details.Uses) != 1 {
		t.Fatalf("refusal = %s", response.Body.String())
	}
	if repository.archived != "" {
		t.Fatal("refused archive reached the repository")
	}

	repository.uses = nil
	if response := archive("payments"); response.Code != http.StatusNoContent {
		t.Fatalf("archive without uses = %d: %s", response.Code, response.Body.String())
	}
	if repository.archived != "payments" {
		t.Fatalf("archived key = %q, want payments", repository.archived)
	}
	if response := archive("missing"); response.Code != http.StatusNotFound {
		t.Fatalf("archive of an unknown key = %d, want 404", response.Code)
	}
	if response := archive("Not%20Valid"); response.Code != http.StatusBadRequest {
		t.Fatalf("archive of an invalid key = %d, want 400", response.Code)
	}
}

func TestEscalationPathListStatusFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := routingHTTPPathRepositoryWithRevision()
	router := routingManagementTestRouter(repository, routingHTTPPrincipal())
	list := func(query string) (*httptest.ResponseRecorder, []map[string]any) {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/escalation-paths"+query, nil))
		var body struct {
			Paths []map[string]any `json:"escalation_paths"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		return response, body.Paths
	}

	// Live by default, archived on request, both with all; an archived
	// summary carries archived_at.
	if response, paths := list(""); response.Code != http.StatusOK || repository.listed.Status != application.EscalationPathStatusLive || len(paths) != 1 {
		t.Fatalf("default list = %d, status %q, %d paths", response.Code, repository.listed.Status, len(paths))
	}
	if response, paths := list("?status=all"); response.Code != http.StatusOK || repository.listed.Status != application.EscalationPathStatusAll || len(paths) != 2 || paths[1]["archived_at"] == nil || paths[0]["archived_at"] != nil {
		t.Fatalf("all list = %d: %s", response.Code, response.Body.String())
	}
	if response, _ := list("?status=archived"); response.Code != http.StatusOK || repository.listed.Status != application.EscalationPathStatusArchived {
		t.Fatalf("archived list = %d, status %q", response.Code, repository.listed.Status)
	}
	if response, _ := list("?status=gone"); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown status = %d, want 400", response.Code)
	}
}
