package discovery

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/agentruntime"
	"github.com/withHumanAI/withHuman/internal/cli/clitest"
	"github.com/withHumanAI/withHuman/internal/cli/mcpclient"
)

// claudeStore is a credentials store with a live token, an entry Claude
// Code never finished signing in to, and an expired one.
const claudeStore = `{"mcpOAuth":{
	"crm|1a2b":{"serverName":"crm","serverUrl":"https://crm.example.com/mcp","accessToken":"tok-crm","expiresAt":4102444800000},
	"stale|9f":{"serverName":"stale","serverUrl":"https://stale.example.com/mcp","accessToken":"","expiresAt":0},
	"old|c3":{"serverName":"old","serverUrl":"https://old.example.com/mcp","accessToken":"tok-old","expiresAt":946684800000}}}`

func TestClaudeCodeDiscoverUsesStoredOAuthTokens(t *testing.T) {
	server := httptest.NewServer(clitest.RequireBearer(clitest.MCPHTTPHandler(), "tok-123"))
	defer server.Close()
	env, _ := fixtureEnv(t, "claude-code", agentruntime.ClaudeCode)
	config, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"crm":  map[string]any{"type": "http", "url": server.URL + "/mcp"},
		"open": map[string]any{"type": "http", "url": server.URL + "/mcp?other"},
	}})
	if err := os.WriteFile(filepath.Join(env.Home, ".claude.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := json.Marshal(map[string]any{"mcpOAuth": map[string]any{
		"crm|1a2b": map[string]any{"serverName": "crm", "serverUrl": server.URL + "/mcp", "accessToken": "tok-123"},
	}})
	if err := os.WriteFile(claudeCredentialsPath(env), store, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := claudeCode{}.Discover(context.Background(), env, Detection{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got.MCP) != 1 || len(got.MCP["crm"]) != len(clitest.MCPFixtureTools) {
		t.Fatalf("mcp = %+v, want crm listed with the stored token", got.MCP)
	}
	if len(got.Errors) != 1 || got.Errors[0].Server != "open" {
		t.Fatalf("errors = %+v, want only the server without a token", got.Errors)
	}
	if msg := got.Errors[0].Message; !strings.Contains(msg, "HTTP 401") || !strings.Contains(msg, claudeSignInHint) {
		t.Fatalf("open error = %q, want the 401 and the sign-in hint", msg)
	}
}

func TestClaudeMCPTokensReadTheKeychainWithoutACredentialsFile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the keychain fallback is macOS only")
	}
	env, runs := fixtureEnv(t, "claude-code", agentruntime.ClaudeCode)
	env.Run = func(_ context.Context, command string, args ...string) ([]byte, error) {
		*runs = append(*runs, command+" "+strings.Join(args, " "))
		return []byte(claudeStore + "\n"), nil
	}
	tokens := claudeMCPTokens(context.Background(), env)
	if len(tokens) != 1 || tokens["https://crm.example.com/mcp"] != "tok-crm" {
		t.Fatalf("tokens = %v, want the crm token alone: no token for stale, an expired one for old", tokens)
	}
	if len(*runs) != 1 || !strings.Contains((*runs)[0], "security find-generic-password -s "+claudeKeychainService+" -a "+claudeKeychainAccount(env)+" -w") {
		t.Fatalf("runs = %v, want one security lookup under the OS account", *runs)
	}
}

func TestWithClaudeTokensKeepsConfiguredHeaders(t *testing.T) {
	servers := []mcpServer{
		{Name: "crm", Spec: mcpclient.Spec{URL: "https://crm.example.com/mcp"}},
		{Name: "keyed", Spec: mcpclient.Spec{URL: "https://crm.example.com/mcp", Headers: map[string]string{"authorization": "Bearer own"}}},
		{Name: "local", Spec: mcpclient.Spec{Command: "local-mcp"}},
	}
	got := withClaudeTokens(servers, parseClaudeMCPTokens([]byte(claudeStore), time.Now()))
	if got[0].Spec.Headers["Authorization"] != "Bearer tok-crm" {
		t.Fatalf("crm headers = %v, want the stored token", got[0].Spec.Headers)
	}
	if len(got[1].Spec.Headers) != 1 || got[1].Spec.Headers["authorization"] != "Bearer own" {
		t.Fatalf("keyed headers = %v, want the configured header kept", got[1].Spec.Headers)
	}
	if got[2].Spec.Headers != nil {
		t.Fatalf("local headers = %v, want none", got[2].Spec.Headers)
	}
}
