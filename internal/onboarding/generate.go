package onboarding

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/agentruntime"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/routing"
)

// The objects provisioning writes, built here as pure functions so their
// shape is tested without a database.

const (
	gateBlockID      = "gate-selected-tools"
	gateBlockName    = "Review tools chosen in setup"
	approveBlockID   = "approve-the-rest"
	approveBlockName = "Approve every other tool"
	pathKeyPrefix    = "notify-"
	pathEscalateAft  = "1h"
	maxPathSlug      = 50
)

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// PathSlug derives the escalation path key from the person's display
// name, falling back to a prefix of their membership id when the name has
// nothing usable in it.
func PathSlug(displayName string, membershipID domain.ID) string {
	slug := strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(displayName), "-"), "-")
	if len(slug) > maxPathSlug {
		slug = strings.Trim(slug[:maxPathSlug], "-")
	}
	if slug == "" {
		id := strings.ReplaceAll(string(membershipID), "-", "")
		if len(id) > 8 {
			id = id[:8]
		}
		slug = id
	}
	return pathKeyPrefix + slug
}

// PathName is the human label of the generated path.
func PathName(displayName string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return "Notify me"
	}
	return "Notify " + displayName
}

// PathDocument is the one-level path that notifies the person and, an
// hour later, escalates to nobody in particular: the request's own
// deadline is the real cap, and a level node must carry an escalation
// window.
func PathDocument(membershipID domain.ID) routing.PathDocument {
	return routing.PathDocument{
		WorkingHours: []routing.WorkingHoursSet{},
		Nodes: []routing.Node{{
			ID:            uuid.NewString(),
			Type:          routing.NodeLevel,
			Targets:       []routing.Target{{Type: routing.TargetUser, ID: string(membershipID)}},
			EscalateAfter: pathEscalateAft,
		}},
	}
}

// PathTargetsMembership reports whether a path document already sends
// requests to the membership, which is what makes an existing path
// reusable.
func PathTargetsMembership(document routing.PathDocument, membershipID domain.ID) bool {
	for _, target := range document.Targets() {
		if target.Type == routing.TargetUser && target.ID == string(membershipID) {
			return true
		}
	}
	return false
}

// PipelineBlocks builds the two-block starting pipeline for one agent
// type: the suggested tools go to a human through the path, and an
// always block approves everything else. The setup assistant replaces it
// with the person's own rules; without an assistant it is what the setup
// leaves behind. The explicit approve block matters because falling off
// the end of a pipeline is an implicit human outcome, and the adapter
// policy is shared across runtimes, so a tool gated for one runtime can
// reach another's pipeline.
//
// Block names are verb-first labels, like every other block, because the
// product quotes them inside sentences: the pipeline card lists them, and
// a request's review says `"<name>" asked for a human` or `"<name>"
// approved it`. Reasons are the full sentences shown under "Why this
// needs a human".
func PipelineBlocks(selected []string, pathKey string) []approvalblock.Definition {
	tools := append([]string(nil), selected...)
	sort.Strings(tools)
	gate := approvalblock.Define(gateBlockID,
		approvalblock.Name(gateBlockName),
		approvalblock.When(approvalblock.Field("/request/tool").In(tools...)),
		approvalblock.OnMatch(approvalblock.Human),
		approvalblock.Reason("This tool was suggested for human approval during the guided setup."),
	)
	gate.CEL.EscalationPath = pathKey
	rest := approvalblock.DefineAlways(approveBlockID, approvalblock.Approve,
		approvalblock.Name(approveBlockName),
		approvalblock.Reason("This tool was not suggested for human approval during the guided setup."),
	)
	return []approvalblock.Definition{*gate, *rest}
}

// GatedToolsOf reads the tool list back out of a pipeline's first block
// when that block is one onboarding wrote, so a second run unions rather
// than replaces. Any other first block yields nothing.
func GatedToolsOf(definitions []approvalblock.Definition) []string {
	if len(definitions) == 0 || definitions[0].ID != gateBlockID || definitions[0].CEL == nil {
		return nil
	}
	var tools []string
	if err := json.Unmarshal(definitions[0].CEL.When.Value, &tools); err != nil {
		return nil
	}
	return tools
}

func sortStrings(values []string) { sort.Strings(values) }

// Union merges tool lists into one sorted, deduplicated list.
func Union(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, tool := range list {
			if tool == "" || seen[tool] {
				continue
			}
			seen[tool] = true
			out = append(out, tool)
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// HookPlan is what the CLI needs to register one runtime's hook: the
// matcher (empty for runtimes whose registration has none) and the
// canonical tool names it gates.
type HookPlan struct {
	Matcher string   `json:"matcher"`
	Tools   []string `json:"tools"`
}

// PlanHooks derives the per-runtime hook plans: the catch-all matcher
// for runtimes whose registration takes one, and the canonical names of
// the suggested tools, which the pipeline (not the matcher) enforces.
func PlanHooks(session domain.OnboardingSession) map[string]HookPlan {
	plans := map[string]HookPlan{}
	for _, runtime := range session.SelectedRuntimes {
		var names []string
		for _, tool := range session.SuggestedTools(runtime) {
			names = append(names, tool.Name)
		}
		plans[runtime] = HookPlan{
			Matcher: agentruntime.HookMatcher(agentruntime.Key(runtime)),
			Tools:   Union(names),
		}
	}
	return plans
}

// SuggestedToolNames lists the names of one runtime's suggested tools:
// what the starting pipeline gates.
func SuggestedToolNames(session domain.OnboardingSession, runtime string) []string {
	var names []string
	for _, tool := range session.SuggestedTools(runtime) {
		names = append(names, tool.Name)
	}
	return Union(names)
}

// TestCommands picks, per runtime, the most demonstrable suggested tool
// (a shell built-in, then an edit built-in, then the first MCP tool, then
// anything) and phrases the command that makes the runtime call it. It
// fits the starting pipeline; the setup assistant replaces a runtime's
// command with one that fits the rules it wrote.
func TestCommands(session domain.OnboardingSession) map[string]domain.OnboardingTestCommand {
	commands := map[string]domain.OnboardingTestCommand{}
	for _, runtime := range session.SelectedRuntimes {
		if command, ok := testCommand(runtime, session.SuggestedTools(runtime)); ok {
			commands[runtime] = command
		}
	}
	return commands
}

// testCommand phrases the command for the most demonstrable of the gated
// tools, as TestCommands describes.
func testCommand(runtime string, tools []domain.OnboardingTool) (domain.OnboardingTestCommand, bool) {
	if len(tools) == 0 {
		return domain.OnboardingTestCommand{}, false
	}
	chosen := tools[0]
	rank := func(tool domain.OnboardingTool) int {
		switch {
		case tool.Source == string(agentruntime.SourceBuiltin) && tool.Kind == string(agentruntime.ToolShell):
			return 0
		case tool.Source == string(agentruntime.SourceBuiltin) && tool.Kind == string(agentruntime.ToolEdit):
			return 1
		case tool.Source == string(agentruntime.SourceMCP):
			return 2
		default:
			return 3
		}
	}
	for _, tool := range tools[1:] {
		if rank(tool) < rank(chosen) {
			chosen = tool
		}
	}
	command, ok := agentruntime.TestCommand(agentruntime.Key(runtime), agentruntime.SelectedTool{
		Name: chosen.Name, Source: agentruntime.Source(chosen.Source), Server: chosen.Server, Kind: agentruntime.ToolKind(chosen.Kind),
	})
	if !ok {
		return domain.OnboardingTestCommand{}, false
	}
	return domain.OnboardingTestCommand{Tool: chosen.Name, Command: command.Shell, Note: command.Note}, true
}
