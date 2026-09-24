// Package clitest holds the test doubles the CLI packages share: a fake
// onboarding provider and a small MCP server that tests run as a helper
// process by re-executing their own test binary.
package clitest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/withHumanAI/withHuman/internal/cli/jsonrpc"
)

// MCPFixtureEnv is the environment variable that turns a test binary into
// the fixture server. Tests call MaybeRunMCPFixture from TestMain.
const MCPFixtureEnv = "WITHHUMAN_TEST_MCP_FIXTURE"

// MaybeRunMCPFixture serves the fixture over stdio and exits when the
// process was started as a helper, and returns at once otherwise.
func MaybeRunMCPFixture() {
	if os.Getenv(MCPFixtureEnv) != "1" {
		return
	}
	ServeMCPStdio(os.Stdin, os.Stdout)
	os.Exit(0)
}

// MCPFixtureCommand is the stdio command a test writes into a runtime's
// MCP configuration to reach the fixture: the test binary itself with the
// helper variable set.
func MCPFixtureCommand() (command string, env map[string]string) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return exe, map[string]string{MCPFixtureEnv: "1"}
}

// MCPFixtureTools is the fixture's catalog: an ungated echo, a gated
// deploy, and two tools whose annotations exercise the suggestion rules.
var MCPFixtureTools = []map[string]any{
	{
		"name":        "echo",
		"description": "Echo the input text back.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
	},
	{
		"name":        "deploy_prod",
		"description": "Deploy the given service to production.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"service": map[string]any{"type": "string"}}, "required": []string{"service"}},
	},
	{
		"name":        "get_customer",
		"description": "Look up a customer record by id.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"customer_id": map[string]any{"type": "string"}}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name":        "delete_customer",
		"description": "Permanently delete a customer record.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"customer_id": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}},
		"annotations": map[string]any{"destructiveHint": true},
	},
}

type mcpRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Name            string         `json:"name"`
		Arguments       map[string]any `json:"arguments"`
		Cursor          string         `json:"cursor"`
	} `json:"params"`
}

func mcpResult(id json.RawMessage, res any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": res}
}

func mcpTextResult(id json.RawMessage, text string) map[string]any {
	return mcpResult(id, map[string]any{"content": []map[string]any{{"type": "text", "text": text}}})
}

// HandleMCP answers one fixture request, or nil for a notification. The
// tool list is served in two pages so clients must follow nextCursor.
func HandleMCP(msg mcpRequest) map[string]any {
	switch msg.Method {
	case "initialize":
		return mcpResult(msg.ID, map[string]any{
			"protocolVersion": msg.Params.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "withhuman-fixture", "version": "0.0.1"},
		})
	case "tools/list":
		if msg.Params.Cursor == "" {
			return mcpResult(msg.ID, map[string]any{"tools": MCPFixtureTools[:2], "nextCursor": "page-2"})
		}
		return mcpResult(msg.ID, map[string]any{"tools": MCPFixtureTools[2:]})
	case "tools/call":
		switch msg.Params.Name {
		case "echo":
			return mcpTextResult(msg.ID, fmt.Sprintf("echo: %v", msg.Params.Arguments["text"]))
		case "deploy_prod":
			return mcpTextResult(msg.ID, fmt.Sprintf("deployed %v to production", msg.Params.Arguments["service"]))
		default:
			return map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32602, "message": "unknown tool"}}
		}
	case "ping":
		return mcpResult(msg.ID, map[string]any{})
	default:
		if msg.ID != nil {
			return mcpResult(msg.ID, map[string]any{})
		}
		return nil
	}
}

// ServeMCPStdio runs the fixture over newline-delimited JSON-RPC until
// stdin closes.
func ServeMCPStdio(stdin io.Reader, stdout io.Writer) {
	out := jsonrpc.NewLineWriter(stdout)
	scanner := jsonrpc.NewScanner(stdin)
	for scanner.Scan() {
		var msg mcpRequest
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		if resp := HandleMCP(msg); resp != nil {
			_ = out.WriteJSON(resp)
		}
	}
}

// MCPHTTPHandler serves the fixture as a streamable-HTTP MCP endpoint:
// one JSON response per POST, a fixed Mcp-Session-Id, no SSE. Tests mount
// it on an httptest server.
func MCPHTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var msg mcpRequest
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "bad json-rpc", http.StatusBadRequest)
			return
		}
		resp := HandleMCP(msg)
		if msg.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "fixture-session-1")
		}
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	return mux
}

// RequireBearer turns away requests that do not carry the given bearer
// token with HTTP 401, the way an OAuth-protected MCP endpoint does.
func RequireBearer(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
