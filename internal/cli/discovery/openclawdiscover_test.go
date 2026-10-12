package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/withHumanAI/withHuman/agentruntime"
)

// OpenClaw keeps its MCP servers under mcp.servers. Detection lists the
// person's own and leaves out the relay the CLI registered there.
func TestOpenClawDetectReadsMCPServers(t *testing.T) {
	env, _ := fixtureEnv(t, "openclaw", agentruntime.OpenClaw)
	config, _ := json.Marshal(map[string]any{"mcp": map[string]any{"servers": map[string]any{
		"crm":       map[string]any{"command": "crm-mcp"},
		"docs":      map[string]any{"url": "https://docs.example.com/mcp", "transport": "streamable-http"},
		"withhuman": map[string]any{"command": testBinary, "args": []string{"mcp", "openclaw", "withhuman"}},
	}}})
	if err := os.WriteFile(filepath.Join(env.Home, ".openclaw", "openclaw.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	det, err := openClaw{}.Detect(env)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, server := range det.MCPServers {
		names = append(names, server.Name)
	}
	if len(names) != 2 || names[0] != "crm" || names[1] != "docs" {
		t.Fatalf("detected servers = %v, want crm and docs", names)
	}
}
