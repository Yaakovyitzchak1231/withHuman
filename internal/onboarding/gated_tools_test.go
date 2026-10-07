package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/domain"
)

// provisioned drives a session to provisioning with codex connected, so
// its agent exists for its rules to be written.
func provisioned(t *testing.T, h *harness) domain.OnboardingSession {
	t.Helper()
	ctx := context.Background()
	begun, err := h.service.Begin(ctx, h.principal)
	if err != nil {
		t.Fatal(err)
	}
	exchanged, err := h.service.Exchange(ctx, begun.SetupCode, domain.OnboardingMachine{Hostname: "box"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.service.ReportDetections(ctx, exchanged.Session, detections("codex"))
	if err != nil {
		t.Fatal(err)
	}
	session, err = h.service.SelectAgents(ctx, h.principal, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	session, err = h.service.ReportTools(ctx, session, map[string]domain.OnboardingReportedTools{"codex": {
		Builtin: []domain.OnboardingReportedTool{{Name: "Bash", InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"number"}},"required":["command"]}`)}, {Name: "Read"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.ConfirmChannels(ctx, h.principal); err != nil {
		t.Fatal(err)
	}
	session, err = h.service.Provision(ctx, h.principal)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSetGatedToolsRewritesTheStartingRules(t *testing.T) {
	ctx := context.Background()
	h := newHarness()
	if _, err := h.service.SetGatedTools(ctx, h.principal, "codex", []string{"Bash"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("without a session: %v", err)
	}
	provisioned(t, h)
	var step *StepError
	if _, err := h.service.SetGatedTools(ctx, h.principal, "codex", []string{"nope"}); !errors.As(err, &step) || step.Code != "tool_not_discovered" {
		t.Fatalf("unknown tool: %v", err)
	}
	if _, err := h.service.SetGatedTools(ctx, h.principal, "claude-code", []string{"Bash"}); !errors.As(err, &step) || step.Code != "runtime_not_selected" {
		t.Fatalf("foreign runtime: %v", err)
	}
	session, err := h.service.SetGatedTools(ctx, h.principal, "codex", []string{"Read", "Bash", "Read"})
	if err != nil {
		t.Fatalf("SetGatedTools: %v", err)
	}
	if got := GatedToolsOf(h.pipelines.entryBlocks(), "codex"); strings.Join(got, ",") != "Bash,Read" {
		t.Fatalf("gated tools: %v", got)
	}
	if h.pipelines.activated[EntryPipelineKey] != 2 {
		t.Fatalf("activation should carry the provisioned revision, got %v", h.pipelines.activated[EntryPipelineKey])
	}
	if command := session.TestCommands["codex"]; command.Tool != "Bash" {
		t.Fatalf("test command should follow the choice: %+v", command)
	}
	session, err = h.service.SetGatedTools(ctx, h.principal, "codex", nil)
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if _, ok := session.TestCommands["codex"]; ok || len(GatedToolsOf(h.pipelines.entryBlocks(), "codex")) != 0 {
		t.Fatalf("nothing gated should leave no test: %+v", session.TestCommands)
	}
}
