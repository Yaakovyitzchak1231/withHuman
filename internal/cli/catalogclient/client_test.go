package catalogclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/cli/credential"
	"github.com/withHumanAI/withHuman/internal/cli/discovery"
)

func TestReportPreservesSourcesSchemasAndApprovalNames(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"amount":{"type":"integer"}},"required":["amount"]}`)
	found := domain.OnboardingReportedTools{Builtin: []domain.OnboardingReportedTool{{Name: "Bash", Description: "Run a command"}}, MCP: map[string][]domain.OnboardingReportedTool{
		"payments": {{Name: "refund", InputSchema: schema}}, "other": {{Name: "refund", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, Errors: []domain.OnboardingDiscoveryError{{Server: "offline", Message: "A private URL or credential must not be published"}}}
	env := discovery.Env{Home: "/private/home", Cwd: "/private/project-one"}
	for _, runtime := range []string{"claude-code", "codex", "hermes", "deepseek", "pi", "openclaw"} {
		report := Report(runtime, env, found)
		if err := report.Validate(); err != nil {
			t.Fatal(err)
		}
		if len(report.Sources) != 4 || report.Sources[0].Tools[0].Description != "Run a command" {
			t.Fatal(report)
		}
		for _, source := range report.Sources {
			if source.Server == "payments" {
				want := "refund"
				if source.Tools[0].Name != want || string(source.Tools[0].InputSchema) != string(schema) {
					t.Fatalf("%s: %+v", runtime, source.Tools)
				}
			}
		}
		raw, _ := json.Marshal(report)
		if strings.Contains(string(raw), "/private/") || strings.Contains(string(raw), "private URL") {
			t.Fatal("report leaked local discovery details")
		}
		other := env
		other.Cwd = "/private/project-two"
		if report.DiscoveryContext == Report(runtime, other, found).DiscoveryContext {
			t.Fatal("project contexts collided")
		}
	}
}

func TestReportNamesFailureReasonsWithoutTheirDetail(t *testing.T) {
	cases := map[string]string{
		`initialize: HTTP 401`: "refused the runtime's sign-in",
		`Codex is not logged in to this server (run: codex mcp login linear)`: "refused the runtime's sign-in",
		`initialize: Post "https://mcp.example.com/?key=secret": context deadline exceeded`: "did not respond in time",
		`exec: "npx": executable file not found in $PATH`: "command could not be started",
		`initialize: Post "http://127.0.0.1:9/": dial tcp 127.0.0.1:9: connect: connection refused`: "could not be reached",
		`tools/list: invalid params`: "did not list its tools",
	}
	for message, want := range cases {
		found := domain.OnboardingReportedTools{Errors: []domain.OnboardingDiscoveryError{{Server: "linear", Message: message}}}
		report := Report("codex", discovery.Env{Cwd: t.TempDir()}, found)
		if err := report.Validate(); err != nil {
			t.Fatal(err)
		}
		reason := report.Sources[1].Error
		if !strings.Contains(reason, want) || strings.Contains(reason, "secret") || strings.Contains(reason, "example.com") || strings.Contains(reason, "npx") {
			t.Fatalf("%q: %q", message, reason)
		}
	}
}

func TestPublishUsesInstanceCredentialAndRefusesRedirects(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	redirect := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v1/agent_instances/self/tool_catalog" || r.Header.Get("Authorization") != "Bearer whc_test" {
			t.Error("invalid report request")
		}
		if redirect {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return
		}
		var report domain.ToolCatalogReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil || report.Validate() != nil {
			t.Error("invalid report")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cred := credential.Credential{ProviderURL: server.URL, Token: "whc_test"}
	report := Report("claude-code", discovery.Env{Cwd: t.TempDir()}, domain.OnboardingReportedTools{})
	if err := Publish(context.Background(), server.Client(), cred, report); err != nil {
		t.Fatal(err)
	}
	redirect = true
	if err := Publish(context.Background(), server.Client(), cred, report); err == nil || redirected {
		t.Fatal("redirect must not receive an instance credential")
	}
}
