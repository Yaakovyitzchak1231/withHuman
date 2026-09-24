package mcpclient

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/internal/cli/clitest"
)

func TestMain(m *testing.M) {
	clitest.MaybeRunMCPFixture()
	os.Exit(m.Run())
}

func TestListToolsFollowsPagesOverBothTransports(t *testing.T) {
	command, env := clitest.MCPFixtureCommand()
	httpServer := httptest.NewServer(clitest.MCPHTTPHandler())
	defer httpServer.Close()

	specs := map[string]Spec{
		"stdio": {Name: "fixture", Command: command, Env: env},
		"http":  {Name: "fixture", URL: httpServer.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer x"}},
	}
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			tools, err := ListTools(context.Background(), spec)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			if len(tools) != 4 {
				t.Fatalf("got %d tools, want 4 across two pages: %+v", len(tools), tools)
			}
			byName := map[string]Tool{}
			for _, tool := range tools {
				byName[tool.Name] = tool
			}
			get := byName["get_customer"]
			if get.Annotations == nil || get.Annotations.ReadOnlyHint == nil || !*get.Annotations.ReadOnlyHint {
				t.Fatalf("get_customer annotations = %+v, want readOnlyHint true", get.Annotations)
			}
			del := byName["delete_customer"]
			if del.Annotations == nil || del.Annotations.DestructiveHint == nil || !*del.Annotations.DestructiveHint {
				t.Fatalf("delete_customer annotations = %+v, want destructiveHint true", del.Annotations)
			}
			if byName["echo"].Annotations != nil {
				t.Fatalf("echo should carry no annotations, got %+v", byName["echo"].Annotations)
			}
			fields := InputFields(del)
			if len(fields) != 2 || fields[0] != "customer_id" || fields[1] != "reason" {
				t.Fatalf("InputFields = %v", fields)
			}
		})
	}
}

func TestListToolsReportsChildFailure(t *testing.T) {
	_, err := ListTools(context.Background(), Spec{Name: "missing", Command: "/nonexistent/withhuman-mcp-server"})
	if err == nil {
		t.Fatal("expected an error for a missing command")
	}
	_, err = ListTools(context.Background(), Spec{Name: "sh", Command: "sh", Args: []string{"-c", "echo boom >&2; exit 3"}})
	if err == nil {
		t.Fatal("expected an error for a crashing server")
	}
	if got := err.Error(); !strings.Contains(got, "boom") {
		t.Fatalf("error should carry stderr, got %q", got)
	}
	// The crash message must survive even when the child's output closes
	// before it writes to stderr: that is the ordering a loaded runner
	// produces for any crashing server.
	_, err = ListTools(context.Background(), Spec{Name: "sh", Command: "sh", Args: []string{"-c", "exec 1>&-; sleep 0.2; echo late boom >&2; exit 3"}})
	if err == nil {
		t.Fatal("expected an error for a server that closes its output")
	}
	if got := err.Error(); !strings.Contains(got, "late boom") {
		t.Fatalf("error should carry late stderr, got %q", got)
	}
}

func TestSpecValidation(t *testing.T) {
	if _, err := ListTools(context.Background(), Spec{}); err == nil {
		t.Fatal("empty spec should fail")
	}
	if _, err := ListTools(context.Background(), Spec{Command: "x", URL: "http://x"}); err == nil {
		t.Fatal("ambiguous spec should fail")
	}
}
