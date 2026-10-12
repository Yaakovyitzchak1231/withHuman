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
	gateBlockName    = "Review tools chosen in setup"
	askBlockName     = "Ask about these tools"
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

// EntryPipelineKey is the pipeline the setup writes into: every request
// starts there, and each connected runtime gets a branch of its own.
const EntryPipelineKey = domain.EntryPipelineKey

const (
	gateBlockSuffix    = "-gate-selected-tools"
	askBlockSuffix     = "-ask-about-selected-tools"
	approveBlockSuffix = "-approve-the-rest"
	conditionNamespace = "withhuman/onboarding/"
)

// RuntimeBranchKey is the entry block id of one runtime's branch: the
// runtime key itself.
func RuntimeBranchKey(runtime string) string { return runtime }

// conditionID is the stable id of one of the setup's conditions, so two
// runs that gate the same tools produce byte-identical documents and a
// rerun that changes nothing writes no revision.
func conditionID(runtime, purpose string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(conditionNamespace+runtime+"/"+purpose)).String()
}

// StartingBlocks builds the starting body for one runtime: a branch
// whose outcome block sends the tools the person picked by hand to a
// human through the path, and an outcome block that approves everything
// else, until the person or their coding agent writes their own rules. The explicit
// approve block matters because running out of a branch is an implicit
// human outcome, and the adapter policy is shared across runtimes, so a
// tool gated for one runtime can reach another's branch. An empty tool list leaves the gate out: every call runs.
//
// Block ids carry the runtime, because keys are unique across the whole
// entry pipeline and two runtimes each get a branch. Block names are
// verb-first labels, like every other block, because the product quotes
// them inside sentences. Reasons are the full sentences shown under "Why
// this needs a human".
func StartingBlocks(runtime string, selected []string, pathKey string) []approvalblock.Definition {
	tools := Union(selected)
	rest := approvalblock.DefineOutcome(runtime+approveBlockSuffix, approvalblock.Approve,
		approvalblock.Name(approveBlockName),
		approvalblock.Reason("This tool was not chosen for human approval during the guided setup."),
	)
	if len(tools) == 0 {
		return []approvalblock.Definition{*rest}
	}
	ask := approvalblock.DefineOutcome(runtime+askBlockSuffix, approvalblock.Human,
		approvalblock.Name(askBlockName),
		approvalblock.Reason("This tool was chosen for human approval during the guided setup."),
	)
	ask.Outcome.EscalationPath = pathKey
	ask.Outcome.AskAgentOwner = true
	gate := approvalblock.DefineBranch(runtime+gateBlockSuffix, []approvalblock.Definition{*ask},
		approvalblock.Name(gateBlockName),
		approvalblock.When(approvalblock.ConditionID(conditionID(runtime, "gate"), approvalblock.Field("/request/tool").In(tools...))),
	)
	return []approvalblock.Definition{*gate, *rest}
}

// RuntimeBranch wraps a body in the runtime's branch of the entry
// pipeline: requests from the runtime's agent enter it, and a request the
// body does not decide goes to the person through the path. Blocks inside
// a branch never inherit its path, so the gate names the path itself.
func RuntimeBranch(runtime string, body []approvalblock.Definition, pathKey string) approvalblock.Definition {
	name := runtimeName(runtime)
	branch := approvalblock.DefineBranch(RuntimeBranchKey(runtime), body,
		approvalblock.Name(name),
		approvalblock.When(approvalblock.ConditionID(conditionID(runtime, "branch"), approvalblock.Field("/agent/slug").Equals(runtime))),
		approvalblock.Reason("No rule for "+name+" decided this request, so it waits for a person."),
	)
	branch.Branch.EscalationPath = pathKey
	branch.Branch.AskAgentOwner = true
	return *branch
}

// runtimeName is the display name the branch carries.
func runtimeName(runtime string) string {
	if known, ok := agentruntime.Lookup(agentruntime.Key(runtime)); ok {
		return known.DisplayName
	}
	return runtime
}

// EntryBlocks upserts the runtime branches into an entry document: a
// top-level block whose id equals a branch's id is replaced in place; a
// new branch is inserted before the first top-level outcome block (which
// would make it unreachable) or appended. Every other block stays where
// it was.
func EntryBlocks(existing []approvalblock.Definition, branches ...approvalblock.Definition) []approvalblock.Definition {
	blocks := append([]approvalblock.Definition(nil), existing...)
	for _, branch := range branches {
		replaced := false
		for index := range blocks {
			if blocks[index].ID == branch.ID {
				blocks[index] = branch
				replaced = true
				break
			}
		}
		if replaced {
			continue
		}
		at := len(blocks)
		for index, block := range blocks {
			if block.Type == approvalblock.TypeOutcome {
				at = index
				break
			}
		}
		blocks = append(blocks[:at], append([]approvalblock.Definition{branch}, blocks[at:]...)...)
	}
	if blocks == nil {
		blocks = []approvalblock.Definition{}
	}
	return blocks
}

// RuntimeBranchOf finds the runtime's top-level branch in an entry
// document.
func RuntimeBranchOf(entry []approvalblock.Definition, runtime string) (approvalblock.Definition, bool) {
	for _, block := range entry {
		if block.ID == RuntimeBranchKey(runtime) && block.Type == approvalblock.TypeBranch && block.Branch != nil {
			return block, true
		}
	}
	return approvalblock.Definition{}, false
}

// StartingShape reports whether the branch body is still what the setup
// wrote: the gate followed by the approve block, or the approve block
// alone. A body the person or their coding agent rewrote is left alone by a
// later run.
func StartingShape(runtime string, branch approvalblock.Definition) bool {
	if branch.Branch == nil {
		return false
	}
	body := branch.Branch.Blocks
	switch len(body) {
	case 1:
		return body[0].ID == runtime+approveBlockSuffix
	case 2:
		return body[0].ID == runtime+gateBlockSuffix && body[0].Branch != nil && body[1].ID == runtime+approveBlockSuffix
	}
	return false
}

// GatedToolsOf reads the tool list back out of the runtime's branch when
// it is still in the starting shape, so a second run unions rather than
// replaces: nil when the branch is absent or rewritten, an empty list
// when the body is the approve block alone.
func GatedToolsOf(entry []approvalblock.Definition, runtime string) []string {
	branch, ok := RuntimeBranchOf(entry, runtime)
	if !ok || !StartingShape(runtime, branch) {
		return nil
	}
	if len(branch.Branch.Blocks) == 1 {
		return []string{}
	}
	var tools []string
	if err := json.Unmarshal(branch.Branch.Blocks[0].Branch.When.Value, &tools); err != nil {
		return nil
	}
	return tools
}

// SameDocument reports whether two documents are byte-identical once
// encoded, which the deterministic condition ids make meaningful.
func SameDocument(a, b []approvalblock.Definition) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(left) == string(right)
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
