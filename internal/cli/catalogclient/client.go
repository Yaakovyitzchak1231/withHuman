// Package catalogclient publishes definitions using the enrolled instance's
// credential. It never reads agent arguments or changes approval configuration.
package catalogclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/cli/credential"
	"github.com/withHumanAI/withHuman/internal/cli/discovery"
	"github.com/withHumanAI/withHuman/internal/cli/installation"
)

func Report(runtime string, env discovery.Env, found domain.OnboardingReportedTools) domain.ToolCatalogReport {
	cwd, _ := filepath.Abs(env.Cwd)
	digest := sha256.Sum256([]byte(env.Home + "\x00" + cwd))
	report := domain.ToolCatalogReport{Runtime: runtime, DiscoveryContext: hex.EncodeToString(digest[:]), Complete: true, Sources: []domain.ToolCatalogSourceReport{}}
	convert := func(tools []domain.OnboardingReportedTool, server string) []domain.ToolDefinition {
		out := []domain.ToolDefinition{}
		for _, t := range tools {
			// The server-defined name, which is what every runtime's hook
			// submits and what gate rules and the catalog must match.
			out = append(out, domain.ToolDefinition{Name: t.Name, DisplayName: t.DisplayName, Description: t.Description, InputSchema: t.InputSchema, Annotations: t.Annotations})
		}
		return out
	}
	report.Sources = append(report.Sources, domain.ToolCatalogSourceReport{Source: "builtin", Complete: true, Tools: convert(found.Builtin, "")})
	errors := map[string]bool{}
	for _, failure := range found.Errors {
		errors[failure.Server] = true
		report.Sources = append(report.Sources, domain.ToolCatalogSourceReport{Source: "mcp", Server: failure.Server, Complete: false, Error: failureReason(failure.Message), Tools: []domain.ToolDefinition{}})
	}
	servers := []string{}
	for server := range found.MCP {
		servers = append(servers, server)
	}
	sort.Strings(servers)
	for _, server := range servers {
		if !errors[server] {
			report.Sources = append(report.Sources, domain.ToolCatalogSourceReport{Source: "mcp", Server: server, Complete: true, Tools: convert(found.MCP[server], server)})
		}
	}
	return report
}
// failureReason names why a server failed to list its tools in one of a
// few fixed sentences. The raw message never leaves the machine: it can
// carry a server's URL, its arguments or a credential.
func failureReason(message string) string {
	lower := strings.ToLower(message)
	has := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(lower, part) {
				return true
			}
		}
		return false
	}
	switch {
	case has("not logged in", "mcp login", "http 401", "http 403", "unauthorized", "forbidden"):
		return "The server refused the runtime's sign-in. Sign in to it again in the runtime."
	case has("deadline exceeded", "timeout", "timed out"):
		return "The server did not respond in time."
	case has("executable file not found", "no such file or directory", "permission denied"):
		return "The server's command could not be started. Check that it is installed on this machine."
	case has("connection refused", "no such host", "dial ", "connection reset", "eof"):
		return "The server could not be reached. Check that it is running and its address is correct."
	}
	return "The server did not list its tools."
}

func Publish(ctx context.Context, httpClient *http.Client, cred credential.Credential, report domain.ToolCatalogReport) error {
	origin, err := installation.Origin(cred.ProviderURL)
	if err != nil {
		return err
	}
	if err := report.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, origin+"/api/v1/agent_instances/self/tool_catalog", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+cred.Token)
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 30 * time.Second}
	if httpClient != nil {
		client = *httpClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("tool catalogue could not reach the provider")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("tool catalogue report refused (HTTP %d); check the instance connection", response.StatusCode)
	}
	return nil
}
