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
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
)

// The management MCP surface is a door onto /api/v1 for API keys: these
// tests pin that the door admits keys and nothing else, that every tool
// re-enters the product API under the key's own authority, and that the
// compositions (save then activate) and refusals reach the agent intact.

// mcpPipelineStore is an in-memory pipeline scope: revision 1 empty and
// active, every create appends, activation checks the expected number
// the way the real store does.
type mcpPipelineStore struct {
	application.RequestApprovalPipelineRepository
	organization domain.ID
	revisions    []domain.RequestApprovalPipelineRevision
	blocks       map[domain.ID]domain.RequestApprovalBlock
	activations  []application.ActivateRequestApprovalPipelineRevisionInput
}

func newMCPPipelineStore(organization domain.ID) *mcpPipelineStore {
	return &mcpPipelineStore{
		organization: organization,
		revisions: []domain.RequestApprovalPipelineRevision{{
			ID: domain.ID(uuid.NewString()), OrganizationID: organization, Revision: 1, IsActive: true,
			BlockIDs: []domain.ID{}, CreatedByActorID: "membership-1", CreatedAt: time.Unix(80_000, 0).UTC(),
		}},
		blocks: map[domain.ID]domain.RequestApprovalBlock{},
	}
}

func (s *mcpPipelineStore) find(slug domain.AgentSlug, revision int64) (domain.RequestApprovalPipelineRevision, bool) {
	if slug != "" {
		return domain.RequestApprovalPipelineRevision{}, false
	}
	for _, candidate := range s.revisions {
		if candidate.Revision == revision {
			return candidate, true
		}
	}
	return domain.RequestApprovalPipelineRevision{}, false
}

func (s *mcpPipelineStore) CreateRequestApprovalPipelineRevision(_ context.Context, input application.CreateRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	if input.AgentSlug != "" {
		return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
	}
	revision := domain.RequestApprovalPipelineRevision{
		ID: input.ID, OrganizationID: input.OrganizationID, Revision: int64(len(s.revisions) + 1),
		BlockIDs: []domain.ID{}, DefaultEscalationPath: input.DefaultEscalationPath, CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
	}
	for _, block := range input.Blocks {
		s.blocks[block.ID] = domain.RequestApprovalBlock{
			ID: block.ID, OrganizationID: input.OrganizationID, BlockKey: block.BlockKey, Name: block.Name, BlockType: block.BlockType,
			ConfigVersion: block.ConfigVersion, Config: block.Config, TimeoutMS: block.TimeoutMS, MaxAttempts: block.MaxAttempts,
			ContentHash: strings.Repeat("c", 64), CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
		}
		revision.BlockIDs = append(revision.BlockIDs, block.ID)
	}
	s.revisions = append(s.revisions, revision)
	return revision, nil
}

func (s *mcpPipelineStore) GetActiveRequestApprovalPipelineRevision(_ context.Context, input application.GetActiveRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	if input.AgentSlug != "" {
		return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
	}
	for _, revision := range s.revisions {
		if revision.IsActive {
			return revision, nil
		}
	}
	return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
}

func (s *mcpPipelineStore) GetRequestApprovalPipelineRevision(_ context.Context, input application.GetRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	revision, ok := s.find(input.AgentSlug, input.Revision)
	if !ok {
		return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
	}
	return revision, nil
}

func (s *mcpPipelineStore) GetRequestApprovalBlocks(_ context.Context, input application.GetRequestApprovalBlocksInput) ([]domain.RequestApprovalBlock, error) {
	blocks := make([]domain.RequestApprovalBlock, 0, len(input.BlockIDs))
	for _, id := range input.BlockIDs {
		block, ok := s.blocks[id]
		if !ok {
			return nil, domain.ErrNotFound
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func (s *mcpPipelineStore) ListActiveRequestApprovalPipelineRevisions(context.Context, application.ListActiveRequestApprovalPipelineRevisionsInput) ([]domain.RequestApprovalPipelineRevision, error) {
	return []domain.RequestApprovalPipelineRevision{}, nil
}

func (s *mcpPipelineStore) ActivateRequestApprovalPipelineRevision(_ context.Context, input application.ActivateRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	s.activations = append(s.activations, input)
	target, ok := s.find(input.AgentSlug, input.Revision)
	if !ok {
		return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
	}
	for index := range s.revisions {
		if s.revisions[index].IsActive && s.revisions[index].Revision != input.ExpectedActiveRevision {
			return domain.RequestApprovalPipelineRevision{}, domain.ErrPreconditionFailed
		}
	}
	for index := range s.revisions {
		s.revisions[index].IsActive = s.revisions[index].Revision == input.Revision
	}
	target.IsActive = true
	return target, nil
}

// mcpHarness is a full router whose only authenticator is the API key
// stub from api_keys_test.go: whk_<id>_secret is the owner narrowed to
// the pipeline permissions, and the organization variant is an
// ActorAPIKey holding the same.
type mcpHarness struct {
	server   *httptest.Server
	store    *mcpPipelineStore
	token    string
	orgToken string
}

func newMCPHarness(t *testing.T) *mcpHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	organization := domain.ID(uuid.NewString())
	principal := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: domain.ID(uuid.NewString()), OrganizationID: organization,
		MembershipID: domain.ID(uuid.NewString()),
		Auth:         domain.AuthContext{Method: "password", Assurance: "strong", AuthenticatedAt: time.Now(), SessionID: "session-1"},
	}.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleOwner}))
	identities := &apiKeyIdentityStub{principal: principal}
	auth := authn.NewService(identities, nil, nil, nil, entitlements.NewUnlimited(), nil, apiKeyClock{}, authn.ServiceConfig{SessionTTL: time.Hour})
	auth.RegisterAPIKeys(&mcpAPIKeyStore{principal: principal})
	store := newMCPPipelineStore(organization)
	registry := approvalpipeline.NewBlockRegistry()
	router := NewRouter(Dependencies{
		Auth: auth, Identities: identities, Logger: logger,
		Pipelines: approvalpipeline.NewManagementService(store, approvalpipeline.NewCompiledPipelineState(store, registry), pipelineHTTPClock{}),
		Config:    Config{PublicOrigin: "http://localhost:8080", Version: "test"},
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &mcpHarness{
		server: server, store: store,
		token:    "whk_" + apiKeyTestKeyID + "_secret",
		orgToken: "who_" + apiKeyTestKeyID + "_secret",
	}
}

// mcpAPIKeyStore resolves whk_ and who_ tokens with the same id and
// secret: the personal one as the person with the pipeline permissions,
// the organization one as an ActorAPIKey with the same grants.
type mcpAPIKeyStore struct {
	apiKeyStoreStub
	principal domain.Principal
}

func (s *mcpAPIKeyStore) ResolveAPIKey(_ context.Context, input application.ResolveAPIKeyInput) (domain.Principal, error) {
	if string(input.KeyID) != apiKeyTestKeyID || string(input.SecretHash) != string(authn.HashToken("secret")) {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	principal := s.principal
	principal.Auth = domain.AuthContext{Method: domain.AuthMethodAPIKey, Assurance: "strong", AuthenticatedAt: input.Now, CredentialID: input.KeyID}
	if input.Kind == domain.APIKeyOrganization {
		principal.ActorType, principal.ActorID, principal.MembershipID = domain.ActorAPIKey, input.KeyID, ""
	}
	return principal.WithGrants(domain.NarrowGrants(s.principal.Grants, []domain.Permission{
		domain.PermissionPipelineRead, domain.PermissionPipelineWrite, domain.PermissionPipelineActivate, domain.PermissionTeamWrite,
	})), nil
}

type mcpBearerTransport struct{ token string }

func (t mcpBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(request)
}

func (h *mcpHarness) connect(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: h.server.URL + MCPPath, HTTPClient: &http.Client{Transport: mcpBearerTransport{token: token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func (h *mcpHarness) call(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

func mcpText(result *mcp.CallToolResult) string {
	var text strings.Builder
	for _, content := range result.Content {
		if block, ok := content.(*mcp.TextContent); ok {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func mcpStructured(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool refused: %s", mcpText(result))
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("structured content %s: %v", encoded, err)
	}
	return decoded
}

func TestMCPAdmitsAPIKeysOnly(t *testing.T) {
	h := newMCPHarness(t)
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	post := func(bearer string, cookie bool) *http.Response {
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, h.server.URL+MCPPath, strings.NewReader(initialize))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie {
			request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: apiKeyCookieToken})
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() })
		return response
	}
	if response := post("", false); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no bearer = %d, want 401", response.StatusCode)
	}
	// A browser session never opens the MCP surface, even a valid one.
	if response := post("", true); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie only = %d, want 401", response.StatusCode)
	}
	if response := post("whk_"+apiKeyTestKeyID+"_wrong", false); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d, want 401", response.StatusCode)
	}
	response := post(h.token, false)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("initialize with key = %d", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), `"instructions"`) || !strings.Contains(string(body), `"withhuman"`) {
		t.Fatalf("initialize answer lacks the server identity and instructions: %s", body)
	}
	// Stateless: no standalone stream and nothing to terminate.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		request, _ := http.NewRequestWithContext(t.Context(), method, h.server.URL+MCPPath, nil)
		request.Header.Set("Authorization", "Bearer "+h.token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s = %d, want 405", method, response.StatusCode)
		}
	}
}

func TestMCPListsTheManagementCatalog(t *testing.T) {
	h := newMCPHarness(t)
	session := h.connect(t, h.token)
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]*mcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		names[tool.Name] = tool
	}
	for _, want := range []string{
		"list_agents", "create_agent", "list_members", "list_teams", "get_team", "create_team", "set_team_members",
		"list_pipelines", "get_pipeline", "list_pipeline_revisions", "get_pipeline_revision", "update_pipeline", "activate_pipeline_revision", "preview_pipeline",
		"list_escalation_paths", "get_escalation_path", "list_escalation_path_revisions", "create_escalation_path", "update_escalation_path", "activate_escalation_path_revision", "archive_escalation_path",
		"list_webhook_endpoints", "get_webhook_endpoint", "save_webhook_endpoint", "read_reference",
	} {
		tool, ok := names[want]
		if !ok {
			t.Errorf("tool %s missing", want)
			continue
		}
		if tool.Description == "" || tool.Annotations == nil {
			t.Errorf("tool %s lacks a description or annotations", want)
		}
	}
	if !names["get_pipeline"].Annotations.ReadOnlyHint || names["update_pipeline"].Annotations.ReadOnlyHint {
		t.Fatal("read-only hints do not match the tools' effects")
	}
	schema, _ := json.Marshal(names["update_pipeline"].InputSchema)
	for _, property := range []string{`"agent_slug"`, `"blocks"`, `"default_escalation_path"`, `"activate"`} {
		if !strings.Contains(string(schema), property) {
			t.Errorf("update_pipeline schema lacks %s: %s", property, schema)
		}
	}
}

func TestMCPUpdatePipelineSavesThenActivatesAgainstTheActiveRevision(t *testing.T) {
	h := newMCPHarness(t)
	session := h.connect(t, h.token)
	block := map[string]any{
		"id": "hold-everything", "name": "Everything needs a person", "type": "always", "enabled": true, "timeout": "50ms", "max_attempts": 1,
		"config": map[string]any{"outcome": "human", "reason": "A person decides."},
	}
	result := mcpStructured(t, h.call(t, session, "update_pipeline", map[string]any{"blocks": []any{block}}))
	if result["activated"] != true {
		t.Fatalf("activated = %v", result["activated"])
	}
	revision := result["revision"].(map[string]any)
	if revision["revision"] != float64(2) || revision["is_active"] != true {
		t.Fatalf("revision = %v", revision)
	}
	if len(h.store.activations) != 1 || h.store.activations[0].Revision != 2 || h.store.activations[0].ExpectedActiveRevision != 1 {
		t.Fatalf("activation inputs = %+v, want revision 2 expecting 1 active", h.store.activations)
	}
	blocks := revision["blocks"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["id"] != "hold-everything" {
		t.Fatalf("blocks = %v", blocks)
	}
	// A draft stays a draft, and the next activation names the right
	// active number.
	draft := mcpStructured(t, h.call(t, session, "update_pipeline", map[string]any{"blocks": []any{}, "activate": false}))
	if draft["activated"] != false || draft["revision"].(map[string]any)["revision"] != float64(3) {
		t.Fatalf("draft = %v", draft)
	}
	if len(h.store.activations) != 1 {
		t.Fatal("a draft must not activate")
	}
	rollback := mcpStructured(t, h.call(t, session, "activate_pipeline_revision", map[string]any{"revision": 3}))
	if rollback["revision"] != float64(3) || h.store.activations[1].ExpectedActiveRevision != 2 {
		t.Fatalf("rollback = %v, activations = %+v", rollback, h.store.activations)
	}
	current := mcpStructured(t, h.call(t, session, "get_pipeline", nil))
	if current["revision"] != float64(3) {
		t.Fatalf("get_pipeline = %v", current)
	}
}

func TestMCPRelaysProductAPIRefusals(t *testing.T) {
	h := newMCPHarness(t)
	session := h.connect(t, h.token)
	// A validation refusal keeps the product API's code and message.
	invalid := h.call(t, session, "update_pipeline", map[string]any{"blocks": []any{map[string]any{"id": "x", "name": "x", "type": "always", "config": map[string]any{"outcome": "maybe"}}}})
	if !invalid.IsError || !strings.HasPrefix(mcpText(invalid), "invalid_request: ") {
		t.Fatalf("invalid block = %v %q", invalid.IsError, mcpText(invalid))
	}
	if len(h.store.revisions) != 1 {
		t.Fatal("a refused revision must not be stored")
	}
	missing := h.call(t, session, "get_pipeline", map[string]any{"agent_slug": "nobody"})
	if !missing.IsError || !strings.HasPrefix(mcpText(missing), "not_found: ") {
		t.Fatalf("unknown agent = %q", mcpText(missing))
	}
	// A route the key may not use (teams need a person's session or
	// key, never an organization key) is refused with the API's code.
	organization := h.connect(t, h.orgToken)
	refused := h.call(t, organization, "create_team", map[string]any{"name": "Payments"})
	if !refused.IsError || !strings.HasPrefix(mcpText(refused), "api_key_not_allowed: ") {
		t.Fatalf("organization key creating a team = %q", mcpText(refused))
	}
	// The organization key still reads pipelines.
	if listed := h.call(t, organization, "get_pipeline", nil); listed.IsError {
		t.Fatalf("organization key reading the pipeline: %s", mcpText(listed))
	}
	unknown := h.call(t, session, "read_reference", map[string]any{"topic": "everything"})
	if !unknown.IsError || !strings.Contains(mcpText(unknown), "pipeline_blocks") {
		t.Fatalf("unknown topic = %q", mcpText(unknown))
	}
}

func TestMCPReferenceTopicsExplainTheGrammar(t *testing.T) {
	h := newMCPHarness(t)
	session := h.connect(t, h.token)
	for topic, marker := range map[string]string{
		"pipeline_blocks":  `"type": "cel"`,
		"conditions":       "greater_than_or_equal",
		"escalation_paths": `"type": "level"`,
	} {
		result := h.call(t, session, "read_reference", map[string]any{"topic": topic})
		if result.IsError || !strings.Contains(mcpText(result), marker) {
			t.Errorf("topic %s: error=%v, text lacks %q", topic, result.IsError, marker)
		}
	}
}
