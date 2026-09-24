package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/agentruntime"
	"github.com/withHumanAI/withHuman/internal/cli/clitest"
	"github.com/withHumanAI/withHuman/internal/cli/mcpclient"
)

func TestMain(m *testing.M) {
	clitest.MaybeRunCodexAppServerFixture()
	clitest.MaybeRunMCPFixture()
	os.Exit(m.Run())
}

func TestCodexDiscoverAsksAppServer(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(clitest.CodexAppServerFixtureEnv, "1")
	env, _ := fixtureEnv(t, "codex", agentruntime.Codex)

	got, err := codex{}.Discover(context.Background(), env, Detection{BinaryPath: exe})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got.Builtin) == 0 {
		t.Fatal("builtin tools missing")
	}
	if len(got.MCP) != 3 {
		t.Fatalf("mcp servers = %v, want crm, github and codex_apps (the disabled and unauthenticated ones left out)", got.MCP)
	}
	crm := got.MCP["crm"]
	if len(crm) != 2 || crm[0].Name != "delete_customer" || crm[1].Name != "get_customer" {
		t.Fatalf("crm tools = %+v, want delete_customer then get_customer", crm)
	}
	if crm[0].Annotations == nil || crm[0].Annotations.DestructiveHint == nil || !*crm[0].Annotations.DestructiveHint {
		t.Fatalf("delete_customer annotations = %+v, want destructiveHint true", crm[0].Annotations)
	}
	if fields := mcpclient.InputFields(mcpclient.Tool{InputSchema: crm[0].InputSchema}); len(fields) != 2 || fields[0] != "customer_id" || fields[1] != "reason" {
		t.Fatalf("delete_customer input fields = %v", fields)
	}
	if github := got.MCP["github"]; len(github) != 1 || github[0].Name != "create_issue" || github[0].Description != "Open an issue." {
		t.Fatalf("github tools = %+v", github)
	}
	// Codex Apps tools are reported under the names Codex's hook spells,
	// not the connector's own "gmail.send_email": that is what a gate rule
	// has to match.
	apps := got.MCP["codex_apps"]
	if len(apps) != 2 || apps[0].Name != "gmail__search_emails" || apps[1].Name != "gmail__send_email" {
		t.Fatalf("codex_apps tools = %+v, want gmail__search_emails then gmail__send_email", apps)
	}
	if apps[1].Description != "Send a Gmail message now from the authenticated account." || apps[1].Annotations == nil || apps[1].Annotations.ReadOnlyHint == nil || *apps[1].Annotations.ReadOnlyHint {
		t.Fatalf("gmail__send_email = %+v, want its description and readOnlyHint false kept", apps[1])
	}
	if len(got.Errors) != 1 || got.Errors[0].Server != "tracker" {
		t.Fatalf("errors = %+v, want only tracker", got.Errors)
	}
	if msg := got.Errors[0].Message; !strings.Contains(msg, "OAuth authorization required") || !strings.Contains(msg, "codex mcp login tracker") {
		t.Fatalf("tracker error = %q, want Codex's reason and the login hint", msg)
	}
}

func TestCodexDiscoverReportsAppServerFailure(t *testing.T) {
	env, _ := fixtureEnv(t, "codex", agentruntime.Codex)
	crashing := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(crashing, []byte("#!/bin/sh\necho 'app-server exploded' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := codex{}.Discover(context.Background(), env, Detection{BinaryPath: crashing})
	if err == nil || !strings.Contains(err.Error(), "app-server exploded") {
		t.Fatalf("Discover error = %v, want the child's stderr", err)
	}
}

func TestCodexDiscoverWithoutBinaryProbesConfig(t *testing.T) {
	env, _ := fixtureEnv(t, "codex", agentruntime.Codex)
	command, fixtureEnv := clitest.MCPFixtureCommand()
	config := "[mcp_servers.fixture]\ncommand = " + tomlQuote(command) + "\n[mcp_servers.fixture.env]\n"
	for key, value := range fixtureEnv {
		config += key + " = " + tomlQuote(value) + "\n"
	}
	config += "\n[mcp_servers.parked]\ncommand = \"parked-mcp\"\nenabled = false\n"
	if err := os.WriteFile(filepath.Join(env.Home, ".codex", "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := codex{}.Discover(context.Background(), env, Detection{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got.Errors) != 0 {
		t.Fatalf("errors = %+v, want none: the disabled server must not be started", got.Errors)
	}
	if len(got.MCP) != 1 || len(got.MCP["fixture"]) != len(clitest.MCPFixtureTools) {
		t.Fatalf("mcp = %+v, want the fixture's %d tools and nothing else", got.MCP, len(clitest.MCPFixtureTools))
	}
}

// The names below are what the AAP Codex hook submits: the server-defined
// name for a server it can list, else what Codex 0.154 hands its
// PreToolUse hook as tool_name minus the mcp__<server>__ prefix.
func TestCodexCanonicalTool(t *testing.T) {
	gmail := &mcpclient.ToolMeta{ConnectorID: "connector_2128aebfecb84f64a069897515042a44", ConnectorName: "Gmail"}
	cases := []struct {
		server    string
		sanitised bool
		tool      mcpclient.Tool
		want      string
	}{
		{"github", false, mcpclient.Tool{Name: "create_issue"}, "create_issue"},
		// A server the hook can list resolves to the server-defined name.
		{"google-mail", false, mcpclient.Tool{Name: "gmail.send_email"}, "gmail.send_email"},
		{"stripe", false, mcpclient.Tool{Name: "_get.balance"}, "_get.balance"},
		// A server signed in through Codex's keychain cannot be listed by
		// the hook, so the sanitised spelling is what it submits.
		{"google-mail", true, mcpclient.Tool{Name: "gmail.send_email"}, "gmail_send_email"},
		{"stripe", true, mcpclient.Tool{Name: "_get.balance"}, "get_balance"},
		// Codex Apps: per-connector namespace, connector stripped off the
		// tool, joined with the hook delimiter.
		{"codex_apps", false, mcpclient.Tool{Name: "gmail.send_email", Meta: gmail}, "gmail__send_email"},
		{"codex_apps", false, mcpclient.Tool{Name: "Gmail.List-Labels", Meta: gmail}, "gmail__list_labels"},
		// The connector id is the fallback prefix when the name does not
		// start with the connector's slug.
		{"codex_apps", false, mcpclient.Tool{Name: "connector_abc.send_message", Meta: &mcpclient.ToolMeta{ConnectorID: "connector_abc", ConnectorName: "Slack"}}, "slack__send_message"},
		// A name that carries neither keeps its whole sanitised self.
		{"codex_apps", false, mcpclient.Tool{Name: "send_message", Meta: &mcpclient.ToolMeta{ConnectorName: "Slack"}}, "slack__send_message"},
		// No connector metadata at all: the server is the namespace.
		{"codex_apps", false, mcpclient.Tool{Name: "sites.publish"}, "sites_publish"},
	}
	for _, tc := range cases {
		if got := codexCanonicalTool(tc.server, tc.sanitised, tc.tool); got != tc.want {
			t.Errorf("%s/%s: got %q, want %q", tc.server, tc.tool.Name, got, tc.want)
		}
	}
}

func TestCodexReportToolsKeepsFirstOfCollidingNames(t *testing.T) {
	got := codexReportTools("crm", true, map[string]mcpclient.Tool{
		"get.customer":    {Name: "get.customer", Description: "dotted"},
		"get_customer":    {Name: "get_customer", Description: "plain"},
		"delete_customer": {Name: "delete_customer"},
	})
	if len(got) != 2 || got[0].Name != "delete_customer" || got[1].Name != "get_customer" || got[1].Description != "dotted" {
		t.Fatalf("reported = %+v, want delete_customer then one get_customer (the first by raw name)", got)
	}
}

func tomlQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
