package mcpservers

import (
	"fmt"
	"strings"

	"github.com/withHumanAI/withHuman/agentruntime"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/cli/configfile"
)

// jsonServers is where a JSON-configured runtime keeps its MCP servers
// and the shape of one entry there.
type jsonServers struct {
	// path is the object keys from the root to the servers object.
	path []string
	// typed entries name their transport as "type": "stdio".
	typed bool
}

var (
	// claudeServers is Claude Code's top-level mcpServers.
	claudeServers = jsonServers{path: []string{"mcpServers"}, typed: true}
	// openClawServers is OpenClaw's mcp.servers. Its Gateway refuses a
	// configuration with keys it does not know, and a stdio entry is
	// known by its command alone.
	openClawServers = jsonServers{path: []string{"mcp", "servers"}}
)

// key spells the servers object's path for a sentence.
func (s jsonServers) key() string { return strings.Join(s.path, ".") }

// object returns the servers object, creating the path when absent.
func (s jsonServers) object(root *configfile.JSONObject) *configfile.JSONObject {
	for _, key := range s.path {
		root = root.Object(key)
	}
	return root
}

// objectIfPresent returns the servers object without creating it.
func (s jsonServers) objectIfPresent(root *configfile.JSONObject) (*configfile.JSONObject, bool) {
	for _, key := range s.path {
		child, ok := root.ObjectIfPresent(key)
		if !ok {
			return nil, false
		}
		root = child
	}
	return root, true
}

// entry is one relay: a stdio command with args and env.
func (s jsonServers) entry(env Env, runtime agentruntime.Key, server domain.AgentMCPServer) *configfile.JSONObject {
	args := []configfile.JSONValue{}
	for _, arg := range Args(runtime, server.Name) {
		args = append(args, arg)
	}
	variables := configfile.NewJSONObject()
	variables.Set(ConfigDirVariable, env.ConfigDir)
	entry := configfile.NewJSONObject()
	if s.typed {
		entry.Set("type", "stdio")
	}
	entry.Set("command", env.Binary)
	entry.Set("args", args)
	entry.Set("env", variables)
	return entry
}

// entryMatches reports whether the configured entry already says what
// entry would write.
func entryMatches(entry *configfile.JSONObject, env Env, runtime agentruntime.Key, server domain.AgentMCPServer) bool {
	if entry.String("command") != env.Binary || entry.StringMap("env")[ConfigDirVariable] != env.ConfigDir {
		return false
	}
	want := Args(runtime, server.Name)
	got := entry.Strings("args")
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// installJSON upserts the servers into the file's servers object. notes
// are appended when the file changed.
func installJSON(path string, servers jsonServers, env Env, reg Registration, notes []string) (Result, error) {
	content, existed, err := configfile.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	doc, err := configfile.LoadJSONObject(content, existed)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", path, err)
	}
	changed := false
	configured := servers.object(doc.Root)
	for _, server := range reg.Servers {
		if entry, ok := configured.ObjectIfPresent(server.Name); ok && entryMatches(entry, env, reg.Runtime, server) {
			continue
		}
		configured.Set(server.Name, servers.entry(env, reg.Runtime, server))
		changed = true
	}
	var result Result
	if !changed {
		return result, nil
	}
	if err := configfile.Write(path, doc.Bytes(), newFileMode); err != nil {
		return Result{}, err
	}
	result.changed(path, existed)
	result.Notes = append(result.Notes, notes...)
	return result, nil
}

// removeJSON deletes the servers' entries that still run the relay.
func removeJSON(path string, servers jsonServers, registered []domain.AgentMCPServer) (Result, error) {
	content, existed, err := configfile.ReadFile(path)
	if err != nil || !existed {
		return Result{}, err
	}
	doc, err := configfile.LoadJSONObject(content, true)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", path, err)
	}
	configured, ok := servers.objectIfPresent(doc.Root)
	if !ok {
		return Result{}, nil
	}
	changed := false
	for _, server := range registered {
		if entry, ok := configured.ObjectIfPresent(server.Name); ok && IsRelay(entry.String("command"), entry.Strings("args")) {
			configured.Delete(server.Name)
			changed = true
		}
	}
	if !changed {
		return Result{}, nil
	}
	if err := configfile.Write(path, doc.Bytes(), newFileMode); err != nil {
		return Result{}, err
	}
	var result Result
	result.changed(path, true)
	return result, nil
}

// snippet renders the servers block a person pastes when the CLI cannot
// edit the runtime's file.
func snippet(servers jsonServers, env Env, reg Registration) string {
	root := configfile.NewJSONObject()
	configured := servers.object(root)
	for _, server := range reg.Servers {
		configured.Set(server.Name, servers.entry(env, reg.Runtime, server))
	}
	return string(configfile.FormatJSON(root, "  "))
}
