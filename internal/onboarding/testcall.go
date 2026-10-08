package onboarding

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/withHumanAI/withHuman/agentruntime"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
	"github.com/withHumanAI/withHuman/internal/toolsuggest"
	"github.com/withHumanAI/withHuman/toolpresentation"
)

// maxTestCandidates bounds the previews one runtime's test choice runs:
// a preview really calls a webhook block's endpoint, so the setup never
// walks a long tool list through it.
const maxTestCandidates = 8

// chooseTestCommands picks, per runtime, a test call the active rules
// send to a person, and phrases the command that makes the runtime make
// it. Candidates are the runtime's own tools whose test arguments are
// known in advance (a shell command, then a file write, then MCP tools),
// each previewed against the active entry pipeline with those arguments.
// A runtime none of whose candidates reaches a person has no command:
// the rules let every simple call through, and the page says so.
func (s *Service) chooseTestCommands(ctx context.Context, principal domain.Principal, session domain.OnboardingSession) (map[string]domain.OnboardingTestCommand, error) {
	current, err := s.readEntry(ctx, principal)
	if err != nil {
		return nil, err
	}
	commands := map[string]domain.OnboardingTestCommand{}
	for _, runtime := range session.SelectedRuntimes {
		agent := approvalpipeline.BlockAgent{Slug: runtime, Name: runtimeName(runtime)}
		for _, enrollment := range session.Enrollments {
			if enrollment.Runtime == runtime {
				agent = approvalpipeline.BlockAgent{Slug: string(enrollment.AgentSlug), Name: enrollment.AgentName}
			}
		}
		for _, candidate := range testCandidates(runtime, session.Tools[runtime]) {
			preview, err := s.pipelines.Preview(ctx, principal, EntryPipelineKey, current.blocks, current.route(), approvalpipeline.PreviewSample{
				Request: approvalpipeline.BlockRequest{Tool: candidate.tool.Name, Server: candidate.tool.Server, Arguments: candidate.arguments},
				Agent:   agent,
			})
			if err != nil {
				return nil, err
			}
			if preview.Outcome != approvalpipeline.BlockHuman {
				continue
			}
			command, ok := agentruntime.TestCommand(agentruntime.Key(runtime), agentruntime.SelectedTool{
				Name: candidate.tool.Name, Source: agentruntime.Source(candidate.tool.Source), Server: candidate.tool.Server, Kind: agentruntime.ToolKind(candidate.tool.Kind),
			})
			if !ok {
				break
			}
			commands[runtime] = domain.OnboardingTestCommand{Tool: candidate.tool.Name, Command: command.Shell, Note: command.Note}
			break
		}
	}
	return commands, nil
}

// testCandidate is a tool the test prompt can make the runtime call,
// with the arguments that call will carry.
type testCandidate struct {
	tool      domain.OnboardingTool
	arguments json.RawMessage
	rank      int
}

// testCandidates orders a runtime's tools by how plainly a test shows
// them: the shell, then a tool that writes a file, then MCP tools, whose
// arguments are the agent's own choice and so previewed as none. A
// built-in tool of any other kind has no test prompt, and an MCP tool
// whose call cannot be undone is never a test: the person might approve
// it.
func testCandidates(runtime string, tools []domain.OnboardingTool) []testCandidate {
	var candidates []testCandidate
	for _, tool := range tools {
		switch {
		case tool.Source == string(agentruntime.SourceBuiltin) && tool.Kind == string(agentruntime.ToolShell):
			if arguments, ok := builtinTestArguments(runtime, tool.Name, toolpresentation.KindShell); ok {
				candidates = append(candidates, testCandidate{tool: tool, arguments: arguments, rank: 0})
			}
		case tool.Source == string(agentruntime.SourceBuiltin) && tool.Kind == string(agentruntime.ToolEdit):
			if arguments, ok := builtinTestArguments(runtime, tool.Name, toolpresentation.KindCode); ok {
				candidates = append(candidates, testCandidate{tool: tool, arguments: arguments, rank: 1})
			}
		case tool.Source == string(agentruntime.SourceMCP) && !irreversible(tool):
			candidates = append(candidates, testCandidate{tool: tool, arguments: json.RawMessage(`{}`), rank: 2})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].rank < candidates[j].rank })
	if len(candidates) > maxTestCandidates {
		candidates = candidates[:maxTestCandidates]
	}
	return candidates
}

// builtinTestArguments builds the arguments the stock test prompt makes
// a built-in tool carry, from the fields the shipped presentation catalog
// names for it: the command for a shell tool; the path and content for a
// tool that writes a file. A tool the catalog does not describe, or one
// without the field the prompt fills (an edit that patches rather than
// writes), is not a candidate.
func builtinTestArguments(runtime, tool string, required toolpresentation.Kind) (json.RawMessage, bool) {
	entry, ok := toolpresentation.Resolve(toolpresentation.Builtin(), toolpresentation.Selector{Runtime: runtime, Tool: tool})
	if !ok {
		return nil, false
	}
	arguments := map[string]string{}
	found := false
	for _, field := range entry.Spec.Fields {
		name := strings.TrimPrefix(field.Path, "/")
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		switch field.Kind {
		case toolpresentation.KindShell:
			arguments[name] = agentruntime.TestShellCommand
		case toolpresentation.KindPath:
			arguments[name] = agentruntime.TestFileName
		case toolpresentation.KindCode:
			arguments[name] = agentruntime.TestFileContent
		default:
			continue
		}
		if field.Kind == required {
			found = true
		}
	}
	if !found {
		return nil, false
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// irreversible asks toolsuggest whether a discovered MCP tool's call
// could not be undone.
func irreversible(tool domain.OnboardingTool) bool {
	var annotations *toolsuggest.Annotations
	if tool.Annotations != nil {
		annotations = &toolsuggest.Annotations{DestructiveHint: tool.Annotations.DestructiveHint}
	}
	return toolsuggest.Irreversible(toolsuggest.Tool{Name: tool.Name, Source: agentruntime.SourceMCP, Server: tool.Server, Annotations: annotations})
}
