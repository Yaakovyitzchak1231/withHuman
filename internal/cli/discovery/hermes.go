package discovery

import (
	"context"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/withHumanAI/withHuman/agentruntime"
	"github.com/withHumanAI/withHuman/internal/cli/mcpclient"
)

// hermes discovers runtime tools for guided onboarding.
type hermes struct{}

func (hermes) Key() agentruntime.Key { return agentruntime.Hermes }

func (hermes) configPath(env Env) string {
	return filepath.Join(env.Home, ".hermes", "config.yaml")
}

func (h hermes) load(env Env) (*yaml.Node, error) {
	content, existed, err := readFile(h.configPath(env))
	if err != nil {
		return nil, err
	}
	return loadYAMLMapping(content, existed)
}

// servers reads mcp_servers: <name>: {command, args, env} or {url, headers}.
func (hermes) servers(root *yaml.Node, source string) []mcpServer {
	configured := yamlGet(root, "mcp_servers")
	if configured == nil || configured.Kind != yaml.MappingNode {
		return nil
	}
	var servers []mcpServer
	for i := 0; i+1 < len(configured.Content); i += 2 {
		name, entry := configured.Content[i].Value, configured.Content[i+1]
		if entry.Kind != yaml.MappingNode {
			continue
		}
		spec := mcpclient.Spec{Name: name}
		if url := yamlString(entry, "url"); url != "" {
			spec.URL = url
			spec.Headers = yamlStringMap(entry, "headers")
		} else {
			spec.Command = yamlString(entry, "command")
			spec.Args = yamlStrings(entry, "args")
			spec.Env = yamlStringMap(entry, "env")
			spec.Cwd = yamlString(entry, "cwd")
		}
		if spec.Command == "" && spec.URL == "" {
			continue
		}
		servers = append(servers, mcpServer{Name: name, Source: source, Spec: spec})
	}
	return servers
}

func (h hermes) Discover(ctx context.Context, env Env, _ Detection) (Discovered, error) {
	root, err := h.load(env)
	if err != nil {
		return Discovered{}, err
	}
	return discover(ctx, h.Key(), h.servers(root, h.configPath(env))), nil
}
