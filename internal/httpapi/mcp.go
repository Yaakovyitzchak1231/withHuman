package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/mcptools"
)

// MCPPath is where the product server speaks MCP. It is the management
// surface for AI agents: a coding agent or assistant holding a personal
// (whk_) or organization (who_) API key connects here and manages
// approval pipelines, escalation paths, teams, agents and webhook
// endpoints (plus whatever the edition adds through
// runtime.ServerOptions.MCPTools) through tools instead of hand-written
// HTTP calls. It is a protocol
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
// it into their loopback requests, and the call's JSON-RPC method and
// tool are handed to the key use record, which would otherwise say only
// that the route was posted to.
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
		if call, ok := peekMCPCall(c.Request); ok {
			c.Set(mcpCallUseKey, call)
		}
		principal(c)
	}
}

// mcpCallUseKey carries an MCP request's mcpCallUse from mcpAuth to the
// key use record principalAuth writes.
const mcpCallUseKey = "withhuman.mcp_call_use"

// mcpCallUse is what an API key use records of an MCP request: the
// JSON-RPC method and, for tools/call, the tool's name. Never the
// arguments, which can carry anything the agent chose to send.
type mcpCallUse struct {
	Method string
	Tool   string
}

// peekMCPCall reads a POSTed JSON-RPC message's method and tool name and
// puts the body back for the SDK, which reads it again under its own
// size limit. A body over that limit, a batch, or anything that is not a
// single JSON-RPC object records nothing beyond the route; so does a name
// longer than the record keeps. The SDK then answers the request itself.
func peekMCPCall(request *http.Request) (mcpCallUse, bool) {
	if request.Method != http.MethodPost || request.Body == nil {
		return mcpCallUse{}, false
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, mcp.DefaultMaxRequestBodyBytes+1))
	request.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(body), request.Body), request.Body}
	if err != nil || len(body) > mcp.DefaultMaxRequestBodyBytes {
		return mcpCallUse{}, false
	}
	var message struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &message) != nil || message.Method == "" || len(message.Method) > application.MaxMCPUseNameLength {
		return mcpCallUse{}, false
	}
	call := mcpCallUse{Method: message.Method}
	if message.Method == "tools/call" && len(message.Params.Name) <= application.MaxMCPUseNameLength {
		call.Tool = message.Params.Name
	}
	return call, true
}

// mcpServer builds the tool catalog once: core's tools, then the
// edition's. Tools carry no per-session state: each call reads the
// bearer from its own request headers.
func (a *API) mcpServer() *mcp.Server {
	a.mcpOnce.Do(func() {
		server := mcp.NewServer(&mcp.Implementation{Name: "withhuman", Title: "withHuman", Version: a.config.Version}, &mcp.ServerOptions{
			Instructions: mcpInstructions,
		})
		catalog := mcptools.NewCatalog(server, a.newMCPCall)
		a.registerMCPTools(catalog)
		for _, register := range a.mcpTools {
			register(catalog)
		}
		a.mcp = server
	})
	return a.mcp
}

// mcpCall is one tool invocation's door onto the product API: every
// request it dispatches carries the caller's bearer and request id.
type mcpCall struct {
	api    *API
	ctx    context.Context
	header http.Header
}

func (a *API) newMCPCall(ctx context.Context, request *mcp.CallToolRequest) *mcptools.Call {
	header := http.Header{}
	if request != nil && request.Extra != nil && request.Extra.Header != nil {
		header = request.Extra.Header
	}
	return mcptools.NewCall((&mcpCall{api: a, ctx: ctx, header: header}).dispatch)
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

// dispatch sends one request to the product API in process and answers
// what it answered; mcptools turns a refusal into the agent's error.
func (c *mcpCall) dispatch(method, path string, query url.Values, body any, extra http.Header) (mcptools.Response, error) {
	engine := c.api.engine
	if engine == nil {
		return mcptools.Response{}, &mcptools.Refusal{Status: http.StatusInternalServerError, Type: "internal", Code: "internal_error", Message: "The management API is unavailable."}
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return mcptools.Response{}, &mcptools.Refusal{Status: http.StatusBadRequest, Type: "invalid_request", Code: "invalid_request", Message: err.Error()}
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	ctx := context.WithValue(c.ctx, mcpLoopbackKey{}, true)
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return mcptools.Response{}, &mcptools.Refusal{Status: http.StatusBadRequest, Type: "invalid_request", Code: "invalid_request", Message: err.Error()}
	}
	request.Header.Set("Authorization", c.header.Get("Authorization"))
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
	return mcptools.Response{Status: recorder.status, Header: recorder.header, Body: recorder.body.Bytes()}, nil
}

// mcpServerState lives on the API so the catalog is built once per
// process and shared by every stateless request. mcpTools are the
// edition's own tools, registered after core's.
type mcpServerState struct {
	mcpOnce  sync.Once
	mcp      *mcp.Server
	mcpTools []mcptools.Registration
}

// mcpInstructions is what the client hands its model at initialize: the
// workflow and the facts an agent gets wrong without them.
const mcpInstructions = `withHuman decides which of an AI agent's tool calls need a human's approval. You are connected to one organization's management API with an API key; every tool acts with that key's permissions and is recorded in the audit log.

How the pieces fit:
- Every approval request starts in the organization's entry pipeline (key entry). Blocks run in order; the first block that decides (approve, deny, or human) ends the pipeline. A delegate block hands the requests its condition matches to another pipeline by key, and that pipeline's owners decide what happens to them: a request that enters it is decided inside it or goes to a person, and nothing after the delegate runs for it. A request no block decides falls through to human review, so end a pipeline with an outcome block whose outcome is approve when the rest should be automatic. "If this, then that" is a branch whose condition tests the request and whose blocks end in an outcome block. Pipelines other than entry are created explicitly (create_pipeline) and owned through grants at pipeline:<key>.
- A block that answers "human" hands the request to an escalation path, which names who reviews it (members by membership id, teams by team id), in what order, and what happens when nobody answers. A pipeline revision also carries a default_escalation_path for human exits that name no path. Setting ask_agent_owner beside a block's escalation_path (default_ask_agent_owner beside the default) sends the request to the person who owns the agent instance instead; the path, if any, is the fallback for agents without an owner, else the pipeline default applies.
- A team target without its own escalation policy pages every member at once; update_team_escalation_policy orders how the team is paged.
- Pipelines, escalation paths, team escalation policies and webhook endpoints are revisioned: every save creates a new numbered revision and the write tools activate it right away unless activate is false. Earlier revisions stay readable for rollback through the activate_* tools. Activating a pipeline requires every escalation path and webhook endpoint it names to be active; get_pipeline_revision_references and the *_uses tools explain an activation or archive refusal before you try.
- Credentials, webhook signing secrets and provisioner tokens a tool mints are shown once in its answer. Relay them to the person who needs them and do not repeat them elsewhere.
- Deciding requests, inviting people and granting roles or permission policies are a person's acts in the app; no tool does them.

Typical setup:
1. list_agents to find the agent slug (or create_agent).
2. list_members and list_teams to find reviewer targets; create_team and set_team_members to shape a team. list_tool_definitions gives the exact tool names and arguments conditions test.
3. create_escalation_path naming those targets.
4. update_pipeline for the entry pipeline (pipeline_key entry) with blocks that send risky calls to that path and approve the rest; create_pipeline and a delegate block when another team should own part of the rules.
5. preview_pipeline with a sample request to check the outcome before or after activating.

Call read_reference before writing your first pipeline blocks, conditions, or escalation path document: the grammar is strict, unknown fields are refused, and every condition and node id must be a UUID you generate.`
