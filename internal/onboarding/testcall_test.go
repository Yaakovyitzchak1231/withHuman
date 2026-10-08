package onboarding

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
)

// installed drives a session to installed with codex connected, so the
// person can leave the rules step.
func installed(t *testing.T, h *harness) domain.OnboardingSession {
	t.Helper()
	ctx := context.Background()
	enrolled, err := h.service.Enroll(ctx, provisioned(t, h))
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.service.ReportInstall(ctx, enrolled.Session, []domain.OnboardingInstallResult{{Runtime: "codex", OK: true}})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// A coding agent's rules often hold the shell only for some commands, so
// the starting test call (an echo) runs straight through. The test then
// offers no command for that agent rather than one that never arrives.
func TestRecordRulesChoosesATestCallTheRulesSendToAPerson(t *testing.T) {
	ctx := context.Background()
	h := newHarness()
	installed(t, h)
	h.pipelines.outcome = func(sample approvalpipeline.PreviewSample) approvalpipeline.BlockOutcome {
		if sample.Request.Tool == "Bash" && strings.Contains(string(sample.Request.Arguments), "git push") {
			return approvalpipeline.BlockHuman
		}
		return approvalpipeline.BlockApprove
	}
	session, err := h.service.RecordRules(ctx, h.principal)
	if err != nil {
		t.Fatalf("RecordRules: %v", err)
	}
	if _, ok := session.TestCommands["codex"]; ok {
		t.Fatalf("no simple call reaches a person, so there is no test: %+v", session.TestCommands)
	}
	if len(h.pipelines.previews) != 1 {
		t.Fatalf("only the shell is a candidate: %+v", h.pipelines.previews)
	}
	sample := h.pipelines.previews[0]
	if sample.Request.Tool != "Bash" || string(sample.Request.Arguments) != `{"command":"echo hello from withHuman"}` || sample.Agent.Slug != "codex" || sample.Agent.Name != "Codex" {
		t.Fatalf("the sample should be the call the test prompt makes: %+v", sample)
	}

	// Coming back to change the rules chooses again and keeps the first
	// stamp, so a request decided since still counts.
	earlier := time.Date(2026, 9, 8, 11, 0, 0, 0, time.UTC)
	if _, err := h.sessions.UpdateOnboardingSession(ctx, application.UpdateOnboardingSessionInput{
		OrganizationID: session.OrganizationID, SessionID: session.ID,
		Patch: domain.OnboardingSessionPatch{RulesSetAt: &earlier}, Now: earlier,
	}); err != nil {
		t.Fatal(err)
	}
	h.pipelines.outcome = nil
	session, err = h.service.RecordRules(ctx, h.principal)
	if err != nil {
		t.Fatalf("RecordRules again: %v", err)
	}
	if command := session.TestCommands["codex"]; command.Tool != "Bash" || !strings.HasPrefix(command.Command, "codex exec ") {
		t.Fatalf("the shell now reaches a person: %+v", session.TestCommands)
	}
	if session.RulesSetAt == nil || !session.RulesSetAt.Equal(earlier) {
		t.Fatalf("the first stamp should stand: %v", session.RulesSetAt)
	}
}

func TestTestCandidatesComeWithTheTestPromptsArguments(t *testing.T) {
	destructive := true
	tools := []domain.OnboardingTool{
		{Name: "Edit", Source: "builtin", Kind: "edit"},
		{Name: "create_refund", Source: "mcp", Server: "stripe"},
		{Name: "list_customers", Source: "mcp", Server: "stripe"},
		{Name: "archive_customer", Source: "mcp", Server: "stripe", Annotations: &domain.OnboardingToolAnnotations{DestructiveHint: &destructive}},
		{Name: "Bash", Source: "builtin", Kind: "shell"},
		{Name: "Read", Source: "builtin", Kind: "read"},
		{Name: "Write", Source: "builtin", Kind: "edit"},
	}
	candidates := testCandidates("claude-code", tools)
	got := []string{}
	for _, candidate := range candidates {
		got = append(got, candidate.tool.Name+" "+string(candidate.arguments))
	}
	want := []string{
		`Bash {"command":"echo hello from withHuman"}`,
		`Write {"content":"hello from withHuman","file_path":"withhuman-test.txt"}`,
		`list_customers {}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("candidates:\n%s", strings.Join(got, "\n"))
	}

	var many []domain.OnboardingTool
	for index := range 12 {
		many = append(many, domain.OnboardingTool{Name: fmt.Sprintf("tool_%d", index), Source: "mcp", Server: "github"})
	}
	if candidates := testCandidates("claude-code", many); len(candidates) != maxTestCandidates {
		t.Fatalf("previews should be bounded: %d", len(candidates))
	}
}
