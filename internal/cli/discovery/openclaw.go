package discovery

import (
	"context"
	"fmt"
	"github.com/withHumanAI/withHuman/internal/cli/configfile"
	"path/filepath"

	"github.com/withHumanAI/withHuman/agentruntime"
)

// openClaw wires OpenClaw: the embedded plugin is written under our config
// directory and openclaw.json points the Gateway at it. OpenClaw reads
// JSON5, so a file that is not strict JSON gets a snippet to paste instead
// of an edit that would flatten the person's comments.
type openClaw struct{}

func (openClaw) Key() agentruntime.Key { return agentruntime.OpenClaw }

func (openClaw) configPath(env Env) string {
	return filepath.Join(env.Home, ".openclaw", "openclaw.json")
}

func (o openClaw) Discover(ctx context.Context, env Env, _ Detection) (Discovered, error) {
	content, existed, err := configfile.ReadFile(o.configPath(env))
	if err != nil {
		return Discovered{}, err
	}
	var servers []mcpServer
	doc, err := configfile.LoadJSONObject(content, existed)
	if err != nil {
		return Discovered{}, fmt.Errorf("cannot discover tools from OpenClaw configuration: %w", err)
	}
	if object, ok := openClawServers(doc.Root); ok {
		servers = env.ownServers(claudeStyleServers(object, o.configPath(env), env.Cwd))
	}
	return discover(ctx, o.Key(), servers), nil
}

// openClawServers is the mcp.servers object, where OpenClaw keeps its
// MCP servers.
func openClawServers(root *configfile.JSONObject) (*configfile.JSONObject, bool) {
	mcp, ok := root.ObjectIfPresent("mcp")
	if !ok {
		return nil, false
	}
	return mcp.ObjectIfPresent("servers")
}
