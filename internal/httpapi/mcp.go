package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/onboarding"
)

// MCPPath is where the product server speaks MCP. It is the management
// surface for AI agents: a coding agent or assistant holding a personal
// (whk_) or organization (who_) API key connects here and sets up
// approval pipelines, escalation paths, teams and webhook endpoints
// through tools instead of hand-written HTTP calls. It is a protocol
// namespace like /api/aap/v1, distinct from the chokepoint gateway's
// /gateway/mcp, which sits in the data path of an agent's own tool calls.
const MCPPath = "/api/mcp/v1"

// mcpLoopbackKey marks a request the MCP server dispatches to the
// product API in process, so the observability middleware does not
// count it as a second inbound HTTP request.
type mcpLoopbackKey struct{}

func isMCPLoopback(ctx context.Context) bool {
	marked, _ := ctx.Value(mcpLoopbackKey{}).(bool)
	return marked
}

// loopbackPrincipalKey carries an already-resolved principal into a
// loopback request. The setup assistant runs the management tools under
// the person's own browser session, which has no bearer to forward, so
// the principal itself rides the context. Only in-process dispatch can
// set it: an inbound HTTP request's context never carries the key.
type loopbackPrincipalKey struct{}

func loopbackPrincipal(ctx context.Context) (domain.Principal, bool) {
	if !isMCPLoopback(ctx) {
		return domain.Principal{}, false
	}
	principal, ok := ctx.Value(loopbackPrincipalKey{}).(domain.Principal)
	return principal, ok
}

// installMCPRoutes mounts the streamable HTTP endpoint. Bearer
// authentication happens here, with the same resolver every /api/v1
// route uses; tool calls then re-enter /api/v1 in process carrying that
// bearer, so authorization, organization-key gating, validation, audit
// provenance and key usage recording are the product API's own. GET and
// DELETE are registered so the SDK can answer them per the protocol (405
// in stateless mode) instead of the frontend fallback serving a page.
func (a *API) installMCPRoutes(root *gin.RouterGroup) {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return a.mcpServer() }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       a.logger,
		// The product server sits behind an ingress on every real
		// deployment, so the Host header is not the boundary; the
		// bearer is. Same stance as the chokepoint gateway.
		DisableLocalhostProtection: true,
	})
	serve := func(c *gin.Context) {
		// The challenge was set for a refusal; a request that got this
		// far is authenticated.
		c.Writer.Header().Del("WWW-Authenticate")
		handler.ServeHTTP(c.Writer, c.Request)
	}
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		a.handle(root, method, MCPPath, AccessSelf, a.mcpAuth(), serve)
	}
}

// mcpAuth admits API keys only: a session cookie never opens the MCP
// surface, because MCP clients are programs, not browsers, and the
// cookie would carry the cross-site risk the same-origin guard exists
// for. A refusal carries the RFC 9728 challenge that tells an MCP client
// where to start the OAuth flow, which ends in a key of its own. The
// resolved request id is copied onto the request so tool calls can carry
// it into their loopback requests.
func (a *API) mcpAuth() gin.HandlerFunc {
	principal := a.principalAuth()
	return func(c *gin.Context) {
		if a.mcpOAuth != nil {
			c.Header("WWW-Authenticate", fmt.Sprintf(`Bearer realm="withHuman", resource_metadata="%s"`, a.mcpOAuth.ResourceMetadataURL()))
		}
		if !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
			WriteError(c, a.logger, domain.ErrUnauthenticated)
			return
		}
		if requestID := c.Writer.Header().Get("X-Request-ID"); requestID != "" {
			c.Request.Header.Set("X-Request-ID", requestID)
		}
		principal(c)
	}
}

// mcpServer builds the tool catalog once. Tools carry no per-session
// state: each call reads the bearer from its own request headers.
func (a *API) mcpServer() *mcp.Server {
	a.mcpOnce.Do(func() {
		server := mcp.NewServer(&mcp.Implementation{Name: "withhuman", Title: "withHuman", Version: a.config.Version}, &mcp.ServerOptions{
			Instructions: mcpInstructions,
		})
		a.registerMCPTools(server)
		a.mcp = server
	})
	return a.mcp
}

// mcpCall is one tool invocation's view of the product API: every
// request it dispatches carries the caller's bearer and request id, or,
// for the setup assistant, the person's resolved principal.
type mcpCall struct {
	api       *API
	ctx       context.Context
	header    http.Header
	principal *domain.Principal
}

func (a *API) newMCPCall(ctx context.Context, request *mcp.CallToolRequest) *mcpCall {
	header := http.Header{}
	if request != nil && request.Extra != nil && request.Extra.Header != nil {
		header = request.Extra.Header
	}
	return &mcpCall{api: a, ctx: ctx, header: header}
}

// mcpToolError is a product API refusal relayed to the agent verbatim:
// the same code, message, parameter and details the HTTP client would
// read, because the agent has to act on them the same way.
type mcpToolError struct {
	Status  int
	Type    string          `json:"type"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Param   string          `json:"param,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (e *mcpToolError) Error() string {
	var text strings.Builder
	text.WriteString(e.Code)
	text.WriteString(": ")
	text.WriteString(e.Message)
	if e.Param != "" {
		fmt.Fprintf(&text, " (param: %s)", e.Param)
	}
	if len(e.Details) > 0 {
		text.WriteString("\ndetails: ")
		text.Write(e.Details)
	}
	return text.String()
}

// loopbackResponse is what one in-process product API request answered.
type loopbackResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// loopbackRecorder is the response writer the in-process dispatch hands
// gin. It records the status, headers and body and nothing else; the
// product API never streams to a tool call.
type loopbackRecorder struct {
	status int
	header http.Header
	body   bytes.Buffer
}

func (r *loopbackRecorder) Header() http.Header { return r.header }

func (r *loopbackRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}

func (r *loopbackRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

// do dispatches one request to the product API in process. A non-2xx
// answer becomes an mcpToolError carrying the API's own error envelope.
func (c *mcpCall) do(method, path string, query url.Values, body any, extra http.Header) (loopbackResponse, error) {
	engine := c.api.engine
	if engine == nil {
		return loopbackResponse{}, &mcpToolError{Status: http.StatusInternalServerError, Type: "internal", Code: "internal_error", Message: "The management API is unavailable."}
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return loopbackResponse{}, &mcpToolError{Status: http.StatusBadRequest, Type: "invalid_request", Code: "invalid_request", Message: err.Error()}
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	ctx := context.WithValue(c.ctx, mcpLoopbackKey{}, true)
	if c.principal != nil {
		ctx = context.WithValue(ctx, loopbackPrincipalKey{}, *c.principal)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return loopbackResponse{}, &mcpToolError{Status: http.StatusBadRequest, Type: "invalid_request", Code: "invalid_request", Message: err.Error()}
	}
	if c.principal == nil {
		request.Header.Set("Authorization", c.header.Get("Authorization"))
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if requestID := c.header.Get("X-Request-ID"); requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	for key, values := range extra {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	recorder := &loopbackRecorder{header: http.Header{}}
	engine.ServeHTTP(recorder, request)
	response := loopbackResponse{Status: recorder.status, Header: recorder.header, Body: recorder.body.Bytes()}
	if response.Status >= 200 && response.Status < 300 {
		return response, nil
	}
	return response, decodeMCPToolError(response)
}

func decodeMCPToolError(response loopbackResponse) *mcpToolError {
	var envelope struct {
		Error mcpToolError `json:"error"`
	}
	if err := json.Unmarshal(response.Body, &envelope); err != nil || envelope.Error.Code == "" {
		return &mcpToolError{Status: response.Status, Type: "internal", Code: "unexpected_response", Message: fmt.Sprintf("The management API answered status %d.", response.Status)}
	}
	envelope.Error.Status = response.Status
	return &envelope.Error
}

func (c *mcpCall) get(path string, query url.Values) (loopbackResponse, error) {
	return c.do(http.MethodGet, path, query, nil, nil)
}

func (c *mcpCall) post(path string, body any, extra http.Header) (loopbackResponse, error) {
	return c.do(http.MethodPost, path, nil, body, extra)
}

// mcpResult turns a JSON answer into the tool result: the structured
// content is the API's own object, and the text block is the same JSON
// for clients that read only text. A bodiless success answers ok.
func mcpResult(body []byte) (*mcp.CallToolResult, any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte(`{"ok":true}`)
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(body)}},
		StructuredContent: json.RawMessage(body),
	}, nil, nil
}

// mcpObject assembles a composed answer (a saved revision plus whether it
// was activated) from raw JSON parts.
func mcpObject(fields map[string]any) (*mcp.CallToolResult, any, error) {
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, err
	}
	return mcpResult(encoded)
}

// addMCPTool registers one typed tool whose handler answers with raw
// JSON from the product API, so every tool shares the same result and
// error shaping. The tool also joins the catalog the setup assistant
// reads, with the input schema the SDK infers, so both callers see one
// definition.
func addMCPTool[In any](a *API, server *mcp.Server, tool *mcp.Tool, handler func(call *mcpCall, input In) (*mcp.CallToolResult, any, error)) {
	mcp.AddTool(server, tool, func(ctx context.Context, request *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		return handler(a.newMCPCall(ctx, request), input)
	})
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("mcp tool %s: input schema: %v", tool.Name, err))
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("mcp tool %s: input schema: %v", tool.Name, err))
	}
	a.mcpCatalog = append(a.mcpCatalog, mcpCatalogEntry{tool: tool, schema: encoded, invoke: func(call *mcpCall, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var input In
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, fmt.Errorf("the input could not be read: %w", err)
			}
		}
		result, _, err := handler(call, input)
		return result, err
	}})
}

// mcpCatalogEntry is one tool as the setup assistant sees it.
type mcpCatalogEntry struct {
	tool   *mcp.Tool
	schema json.RawMessage
	invoke func(call *mcpCall, input json.RawMessage) (*mcp.CallToolResult, error)
}

// mcpAssistantTools hands the setup assistant the management tool
// catalog, executed in process under the person's own principal: the
// same authorization, validation, and audit provenance a browser request
// of theirs would get.
type mcpAssistantTools struct{ api *API }

func (t mcpAssistantTools) Tools() []application.ToolUseTool {
	t.api.mcpServer()
	tools := make([]application.ToolUseTool, 0, len(t.api.mcpCatalog))
	for _, entry := range t.api.mcpCatalog {
		tools = append(tools, application.ToolUseTool{Name: entry.tool.Name, Description: entry.tool.Description, InputSchema: entry.schema})
	}
	return tools
}

func (t mcpAssistantTools) Grammar() string { return t.api.compiledPipelines.Grammar() }

func (t mcpAssistantTools) Call(ctx context.Context, principal domain.Principal, name string, input json.RawMessage) (onboarding.AssistantToolResult, error) {
	t.api.mcpServer()
	for _, entry := range t.api.mcpCatalog {
		if entry.tool.Name != name {
			continue
		}
		result, err := entry.invoke(&mcpCall{api: t.api, ctx: ctx, header: http.Header{}, principal: &principal}, input)
		if err != nil {
			return onboarding.AssistantToolResult{Content: err.Error(), IsError: true}, nil
		}
		return onboarding.AssistantToolResult{Content: mcpResultText(result), IsError: result != nil && result.IsError}, nil
	}
	return onboarding.AssistantToolResult{}, fmt.Errorf("unknown tool %s", name)
}

// mcpResultText is a tool result as text: what a client that reads only
// text would see.
func mcpResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return `{"ok":true}`
	}
	var text strings.Builder
	for _, content := range result.Content {
		if typed, ok := content.(*mcp.TextContent); ok {
			text.WriteString(typed.Text)
		}
	}
	return text.String()
}

var (
	mcpReadOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)}
	// Additive writes: a new revision, a new key, a new team. Nothing
	// earlier is lost, so a retry at worst adds another revision.
	mcpAdditive = &mcp.ToolAnnotations{DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)}
	// Replacing writes: the call sets the whole value and repeating it
	// changes nothing further.
	mcpReplacing = &mcp.ToolAnnotations{DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)}
	// Archiving ends a resource for good, even though its history stays.
	mcpArchiving = &mcp.ToolAnnotations{DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)}
)

func boolPointer(value bool) *bool { return &value }

// mcpServerState lives on the API so the catalog is built once per
// process and shared by every stateless request and by the setup
// assistant.
type mcpServerState struct {
	mcpOnce    sync.Once
	mcp        *mcp.Server
	mcpCatalog []mcpCatalogEntry
}

// mcpInstructions is what the client hands its model at initialize: the
// workflow and the facts an agent gets wrong without them.
const mcpInstructions = `withHuman decides which of an AI agent's tool calls need a human's approval. You are connected to one organization's management API with an API key; every tool acts with that key's permissions and is recorded in the audit log.

How the pieces fit:
- An approval request runs through the organization's approval pipeline first, then the requesting agent's own pipeline. Blocks run in order; the first block that decides (approve, deny, or human) ends the pipeline. A request no block decides falls through to human review, so end a pipeline with an always block whose outcome is approve when the rest should be automatic. Every agent has exactly one pipeline, seeded empty (everything falls through to a person) when the agent is created.
- A block that answers "human" hands the request to an escalation path, which names who reviews it (members by membership id, teams by team id), in what order, and what happens when nobody answers. A pipeline revision also carries a default_escalation_path for human exits that name no path. Setting ask_agent_owner beside a block's escalation_path (default_ask_agent_owner beside the default) sends the request to the person who owns the agent instance instead, with the path as the fallback for agents without an owner.
- Pipelines, escalation paths and webhook endpoints are revisioned: every save creates a new numbered revision and the write tools activate it right away unless activate is false. Earlier revisions stay readable for rollback through the activate_* tools. Activating a pipeline requires every escalation path and webhook endpoint it names to be active.

Typical setup:
1. list_agents to find the agent slug (or create_agent).
2. list_members and list_teams to find reviewer targets; create_team and set_team_members to shape a team.
3. create_escalation_path naming those targets.
4. update_pipeline for the organization or one agent, with blocks that send risky calls to that path and approve the rest.
5. preview_pipeline with a sample request to check the outcome before or after activating.

Call read_reference before writing your first pipeline blocks, conditions, or escalation path document: the grammar is strict, unknown fields are refused, and every condition and node id must be a UUID you generate.`
