package clitest

import (
	"encoding/json"
	"io"
	"os"

	"github.com/withHumanAI/withHuman/internal/cli/jsonrpc"
)

// CodexAppServerFixtureEnv makes the test binary behave as `codex
// app-server` instead of running tests. A test points the Codex adapter
// at the test binary with this variable set and gets a canned MCP
// inventory back.
const CodexAppServerFixtureEnv = "WITHHUMAN_TEST_CODEX_APP_SERVER"

// MaybeRunCodexAppServerFixture serves the fixture over stdio and exits
// when the helper variable is set; a TestMain calls it first.
func MaybeRunCodexAppServerFixture() {
	if os.Getenv(CodexAppServerFixtureEnv) != "1" {
		return
	}
	ServeCodexAppServer(os.Stdin, os.Stdout)
	os.Exit(0)
}

// CodexAppServerStatuses is the fixture's inventory, split over two pages
// the way Codex pages mcpServerStatus/list: a server Codex is logged in
// to, one it has disabled (empty catalog, no error), one it is not
// logged in to, a stdio server, and Codex's own codex_apps server with a
// Gmail connector's tools, named the way ChatGPT connectors name them.
var CodexAppServerStatuses = [][]map[string]any{
	{
		{
			"name":       "crm",
			"authStatus": "oAuth",
			"tools": map[string]any{
				"get_customer": map[string]any{
					"name":        "get_customer",
					"description": "Fetch a customer record.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"customer_id": map[string]any{"type": "string"}}},
					"annotations": map[string]any{"readOnlyHint": true},
				},
				"delete_customer": map[string]any{
					"name":        "delete_customer",
					"description": "Delete a customer record.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"reason": map[string]any{"type": "string"}, "customer_id": map[string]any{"type": "string"}}},
					"annotations": map[string]any{"destructiveHint": true},
				},
			},
			"toolsError": nil,
		},
		{
			"name":       "parked",
			"authStatus": "unsupported",
			"tools":      map[string]any{},
			"toolsError": nil,
		},
	},
	{
		{
			"name":       "tracker",
			"authStatus": "notLoggedIn",
			"tools":      map[string]any{},
			"toolsError": "MCP startup failed: OAuth authorization required",
		},
		{
			"name":       "github",
			"authStatus": "unsupported",
			"tools": map[string]any{
				"create_issue": map[string]any{
					"name":        "create_issue",
					"description": "Open an issue.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}},
				},
			},
			"toolsError": nil,
		},
		{
			"name":       "codex_apps",
			"authStatus": "unsupported",
			"tools": map[string]any{
				"gmail.send_email": map[string]any{
					"name":        "gmail.send_email",
					"title":       "send_email",
					"description": "Send a Gmail message now from the authenticated account.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"to": map[string]any{"type": "string"}, "subject": map[string]any{"type": "string"}}},
					"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": true},
					"_meta":       map[string]any{"connector_id": "connector_2128aebfecb84f64a069897515042a44", "connector_name": "Gmail"},
				},
				"gmail.search_emails": map[string]any{
					"name":        "gmail.search_emails",
					"title":       "search_emails",
					"description": "Search the mailbox.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
					"annotations": map[string]any{"readOnlyHint": true},
					"_meta":       map[string]any{"connector_id": "connector_2128aebfecb84f64a069897515042a44", "connector_name": "Gmail"},
				},
			},
			"toolsError": nil,
		},
	},
}

type codexAppServerRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Detail string `json:"detail"`
		Cursor string `json:"cursor"`
	} `json:"params"`
}

// ServeCodexAppServer answers the app-server protocol subset discovery
// uses: initialize, the initialized notification, and a paged
// mcpServerStatus/list.
func ServeCodexAppServer(stdin io.Reader, stdout io.Writer) {
	out := jsonrpc.NewLineWriter(stdout)
	scanner := jsonrpc.NewScanner(stdin)
	initialized := false
	for scanner.Scan() {
		var msg codexAppServerRequest
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			initialized = true
			_ = out.WriteJSON(map[string]any{"id": msg.ID, "result": map[string]any{"userAgent": "codex-fixture", "codexHome": os.Getenv("CODEX_HOME")}})
		case "initialized":
		case "mcpServerStatus/list":
			switch {
			case !initialized:
				_ = out.WriteJSON(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32600, "message": "not initialized"}})
			case msg.Params.Detail != "toolsAndAuthOnly":
				_ = out.WriteJSON(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32602, "message": "fixture expects detail toolsAndAuthOnly"}})
			case msg.Params.Cursor == "":
				_ = out.WriteJSON(map[string]any{"id": msg.ID, "result": map[string]any{"data": CodexAppServerStatuses[0], "nextCursor": "page-2"}})
			default:
				_ = out.WriteJSON(map[string]any{"id": msg.ID, "result": map[string]any{"data": CodexAppServerStatuses[1], "nextCursor": nil}})
			}
		default:
			if len(msg.ID) > 0 {
				_ = out.WriteJSON(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32601, "message": "unknown method " + msg.Method}})
			}
		}
	}
}
