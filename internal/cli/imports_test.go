package cli

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoServerImports keeps the CLI a client: it shares domain and
// agentruntime with the server and must never pull in the application
// layer, storage, HTTP framework, or Temporal.
func TestNoServerImports(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	forbidden := []string{
		"github.com/withHumanAI/withHuman/application",
		"github.com/withHumanAI/withHuman/authn",
		"github.com/withHumanAI/withHuman/internal/postgres",
		"github.com/withHumanAI/withHuman/internal/httpapi",
		"github.com/withHumanAI/withHuman/internal/onboarding",
		"github.com/withHumanAI/withHuman/internal/approvalpipeline",
		"github.com/withHumanAI/withHuman/internal/reviewrouting",
		"github.com/gin-gonic/gin",
		"github.com/jackc/pgx",
		"go.temporal.io",
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		for _, banned := range forbidden {
			if dep == banned || strings.HasPrefix(dep, banned+"/") {
				t.Errorf("internal/cli depends on %s", dep)
			}
		}
	}
}
