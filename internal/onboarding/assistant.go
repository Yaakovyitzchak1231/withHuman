package onboarding

// The setup assistant. The person describes in their own words which
// calls should wait for them ("only kubectl commands that change the
// cluster, and anything that pushes to git") and a model turns that into
// each connected agent's approval pipeline through the same management
// tools an external agent would use over MCP, dispatched in process
// under the person's own principal so every write is authorized,
// validated, and audited as theirs. The conversation lives on the
// session so a reload shows it, and every tool call is persisted as it
// happens so the page can follow the work over the session events
// stream. The assistant's reach is deliberately narrow: the pipelines of
// the agents this setup connected, and the test prompt the last step
// shows, checked against the rules before it is accepted.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/agentruntime"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
	"github.com/withHumanAI/withHuman/internal/mcpreference"
)

const (
	// maxAssistantSteps bounds the tool calls one message may cause; a
	// turn that has not answered by then is reported as unfinished.
	maxAssistantSteps        = 16
	assistantMaxOutputTokens = 4096
	// assistantBusyWindow is how long a running message blocks the next
	// one before it is assumed abandoned (a server that died mid-turn).
	assistantBusyWindow = 5 * time.Minute
	// maxAssistantMessageBytes bounds what the person types.
	maxAssistantMessageBytes = 4000
	// assistantContextTurns is how many of the person's messages, with
	// everything the model did in answer, the model context keeps.
	assistantContextTurns = 6
	// maxPromptToolsPerRuntime bounds the tool inventory in the prompt.
	maxPromptToolsPerRuntime = 160
	maxPromptDescription     = 140
	maxPromptArguments       = 8
	// setTestPromptTool is the one tool the setup owns: the assistant
	// names the call the test step should provoke.
	setTestPromptTool = "set_test_prompt"
)

var (
	// ErrAssistantUnavailable means the deployment configured no model.
	ErrAssistantUnavailable = errors.New("the setup assistant has no model configured")
	// ErrAssistantBusy refuses a message while the previous turn runs.
	ErrAssistantBusy = errors.New("the setup assistant is still working on the previous message")
	// ErrAssistantFailed wraps a turn that ended without an answer; the
	// session carries the failed message with the reason.
	ErrAssistantFailed = errors.New("the setup assistant could not finish")
)

// AssistantTools is the management tool catalog the assistant draws on:
// the same tools the hosted MCP server offers, executed in process under
// the person's principal. Which of them the assistant sees is decided
// here, not by the catalog.
type AssistantTools interface {
	Tools() []application.ToolUseTool
	Call(ctx context.Context, principal domain.Principal, name string, input json.RawMessage) (AssistantToolResult, error)
	// Grammar is what the deployment's block kinds add to core's
	// pipeline_blocks reference (the hosted judge's section and model
	// list), the same appendix read_reference serves.
	Grammar() string
}

// AssistantToolResult is what one tool call answered: text for the
// model, marked as an error when the call was refused.
type AssistantToolResult struct {
	Content string
	IsError bool
}

// assistantToolNames are the catalog tools the assistant may use: the
// pipeline tools alone. Agents, escalation paths, teams and webhook
// endpoints are not the setup's to change.
var assistantToolNames = map[string]bool{
	"get_pipeline": true, "update_pipeline": true, "preview_pipeline": true,
	"list_pipeline_revisions": true, "activate_pipeline_revision": true,
}

// assistantStatuses are the statuses in which the agents exist, so their
// pipelines can be written.
var assistantStatuses = map[domain.OnboardingStatus]bool{
	domain.OnboardingProvisioning: true, domain.OnboardingEnrolled: true,
	domain.OnboardingInstalled: true, domain.OnboardingTested: true,
}

// RegisterAssistantModel installs the deployment's model; without one
// the assistant is absent and the page says so.
func (s *Service) RegisterAssistantModel(model application.ToolUseModel) { s.assistantModel = model }

// RegisterAssistantTools installs the management tool catalog.
func (s *Service) RegisterAssistantTools(tools AssistantTools) { s.assistantTools = tools }

// AssistantEnabled reports whether the setup assistant can answer.
func (s *Service) AssistantEnabled() bool { return s.assistantModel != nil && s.assistantTools != nil }

// Ask runs one turn of the assistant on the person's open session: their
// message is recorded, the model works through the tools, and the
// answer (or the failure) is recorded in its place. The updated session
// is returned with the error, so the page has the transcript either way.
func (s *Service) Ask(ctx context.Context, principal domain.Principal, message string) (domain.OnboardingSession, error) {
	if err := authorize(principal); err != nil {
		return domain.OnboardingSession{}, err
	}
	if !s.AssistantEnabled() {
		return domain.OnboardingSession{}, ErrAssistantUnavailable
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return domain.OnboardingSession{}, fmt.Errorf("message is required: %w", domain.ErrInvalid)
	}
	if len(message) > maxAssistantMessageBytes {
		return domain.OnboardingSession{}, fmt.Errorf("message must be at most %d bytes: %w", maxAssistantMessageBytes, domain.ErrInvalid)
	}
	session, err := s.Get(ctx, principal)
	if err != nil {
		return domain.OnboardingSession{}, err
	}
	if !assistantStatuses[session.Status] {
		return domain.OnboardingSession{}, fmt.Errorf("session is %s, the agents are not connected yet: %w", session.Status, domain.ErrOnboardingState)
	}
	now := s.clock.Now()
	if assistantBusy(session, now) {
		return domain.OnboardingSession{}, ErrAssistantBusy
	}

	messages := append(append([]domain.OnboardingAssistantMessage(nil), session.Assistant...),
		domain.OnboardingAssistantMessage{Role: domain.OnboardingAssistantUser, Text: message, Actions: []domain.OnboardingAssistantAction{}, Status: domain.OnboardingAssistantDone, At: now},
		domain.OnboardingAssistantMessage{Role: domain.OnboardingAssistantAssistant, Actions: []domain.OnboardingAssistantAction{}, Status: domain.OnboardingAssistantRunning, At: now},
	)
	// Persistence outlives the request: a person who closes the tab
	// mid-turn should find the outcome, not a message running forever.
	persistCtx := context.WithoutCancel(ctx)
	session, err = s.saveAssistant(persistCtx, session, messages, nil)
	if err != nil {
		return domain.OnboardingSession{}, err
	}
	running := &messages[len(messages)-1]

	history := decodeAssistantContext(session.AssistantContext)
	history = append(history, application.ToolUseMessage{Role: application.ToolUseRoleUser, Blocks: []application.ToolUseBlock{{Type: application.ToolUseText, Text: message}}})
	history = trimAssistantContext(history)
	turn := &assistantTurn{service: s, principal: principal, session: session, started: now}
	final, failure := turn.run(ctx, history, func(action domain.OnboardingAssistantAction) error {
		running.Actions = append(running.Actions, action)
		updated, err := s.saveAssistant(persistCtx, turn.session, messages, nil)
		if err != nil {
			return err
		}
		turn.session = updated
		return nil
	})
	summary := turn.logAttributes("actions", len(running.Actions), "duration_ms", s.clock.Now().Sub(now).Milliseconds())
	if failure != nil {
		running.Status = domain.OnboardingAssistantFailed
		running.Error = assistantFailureText(failure)
		s.logger.WarnContext(ctx, "setup assistant turn failed", append(summary, "error", failure)...)
	} else {
		running.Status = domain.OnboardingAssistantDone
		running.Text = final
		s.logger.InfoContext(ctx, "setup assistant turn finished", append(summary, "answer_bytes", len(final))...)
	}
	encoded, _ := json.Marshal(turn.history)
	context := json.RawMessage(encoded)
	updated, err := s.saveAssistant(persistCtx, turn.session, messages, &context)
	if err != nil {
		return domain.OnboardingSession{}, err
	}
	if failure != nil {
		return updated, fmt.Errorf("%w: %w", ErrAssistantFailed, failure)
	}
	return updated, nil
}

// ClearAssistant forgets the conversation, messages and model context
// alike, so the person can start over without the earlier turns in
// view. The rules stay as they are. A turn still running is left alone.
func (s *Service) ClearAssistant(ctx context.Context, principal domain.Principal) (domain.OnboardingSession, error) {
	if err := authorize(principal); err != nil {
		return domain.OnboardingSession{}, err
	}
	session, err := s.Get(ctx, principal)
	if err != nil {
		return domain.OnboardingSession{}, err
	}
	if assistantBusy(session, s.clock.Now()) {
		return domain.OnboardingSession{}, ErrAssistantBusy
	}
	empty := json.RawMessage(`[]`)
	return s.saveAssistant(ctx, session, []domain.OnboardingAssistantMessage{}, &empty)
}

// assistantBusy reports whether the last message is still running and
// recent enough to be believed.
func assistantBusy(session domain.OnboardingSession, now time.Time) bool {
	if len(session.Assistant) == 0 {
		return false
	}
	last := session.Assistant[len(session.Assistant)-1]
	return last.Role == domain.OnboardingAssistantAssistant && last.Status == domain.OnboardingAssistantRunning && now.Sub(last.At) < assistantBusyWindow
}

func (s *Service) saveAssistant(ctx context.Context, session domain.OnboardingSession, messages []domain.OnboardingAssistantMessage, history *json.RawMessage) (domain.OnboardingSession, error) {
	copied := append([]domain.OnboardingAssistantMessage(nil), messages...)
	return s.sessions.UpdateOnboardingSession(ctx, application.UpdateOnboardingSessionInput{
		OrganizationID: session.OrganizationID, SessionID: session.ID,
		Patch: domain.OnboardingSessionPatch{Assistant: &copied, AssistantContext: history},
		Now:   s.clock.Now(),
	})
}

func decodeAssistantContext(raw json.RawMessage) []application.ToolUseMessage {
	var history []application.ToolUseMessage
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &history)
	}
	return history
}

// trimAssistantContext keeps the most recent turns: everything from the
// nth-last message the person typed onward, so tool calls and their
// results always stay paired.
func trimAssistantContext(history []application.ToolUseMessage) []application.ToolUseMessage {
	var starts []int
	for index, message := range history {
		if message.Role == application.ToolUseRoleUser && len(message.Blocks) > 0 && message.Blocks[0].Type == application.ToolUseText {
			starts = append(starts, index)
		}
	}
	if len(starts) <= assistantContextTurns {
		return history
	}
	return history[starts[len(starts)-assistantContextTurns]:]
}

// assistantFailureText is the sentence the page shows in place of an
// answer.
func assistantFailureText(err error) string {
	switch {
	case errors.Is(err, application.ErrModelUnavailable):
		return "The model could not be reached. Try again in a moment."
	case errors.Is(err, context.DeadlineExceeded):
		return "The assistant took too long to answer. Try a shorter request, or say what to change."
	}
	return err.Error()
}

// assistantTurn is one message's worth of work: the loop between the
// model and the tools, and the session as the tools change it.
type assistantTurn struct {
	service   *Service
	principal domain.Principal
	session   domain.OnboardingSession
	history   []application.ToolUseMessage
	started   time.Time
	// What the turn spent, for its log lines.
	steps           int
	inputTokens     int64
	outputTokens    int64
	reasoningTokens int64
}

// logAttributes are the fields every assistant log line carries: which
// session and model, and what the turn has spent so far.
func (turn *assistantTurn) logAttributes(extra ...any) []any {
	identity := turn.service.assistantModel.Identity()
	return append([]any{
		"session_id", turn.session.ID, "provider", identity.Provider, "model", identity.Model,
		"steps", turn.steps, "input_tokens", turn.inputTokens, "output_tokens", turn.outputTokens,
		"reasoning_tokens", turn.reasoningTokens,
	}, extra...)
}

// logStep records one model call: how it stopped, what it cost, and
// which tools it asked for. A failed call is a warning with the error,
// since the provider's stop reason and token counts are what tell an
// empty answer from a cut-off one.
func (turn *assistantTurn) logStep(ctx context.Context, output application.ToolUseOutput, err error, promptBytes int, started time.Time) {
	var calls []string
	for _, block := range output.Blocks {
		if block.Type == application.ToolUseCall {
			calls = append(calls, block.Name)
		}
	}
	attributes := []any{
		"session_id", turn.session.ID, "step", turn.steps,
		"stop_reason", output.StopReason, "provider_stop", output.ProviderStop, "response_id", output.ResponseID,
		"step_input_tokens", output.InputTokens, "step_output_tokens", output.OutputTokens,
		"step_reasoning_tokens", output.ReasoningTokens, "max_output_tokens", assistantMaxOutputTokens,
		"context_messages", len(turn.history), "prompt_bytes", promptBytes,
		"tool_calls", strings.Join(calls, ","), "duration_ms", turn.service.clock.Now().Sub(started).Milliseconds(),
	}
	if err != nil {
		turn.service.logger.WarnContext(ctx, "setup assistant model call failed", append(attributes, "error", err)...)
		return
	}
	turn.service.logger.InfoContext(ctx, "setup assistant model call", attributes...)
}

// run drives the model until it answers, calling record for every tool
// call so the page sees progress. It returns the answer, or the error
// that ended the turn.
func (turn *assistantTurn) run(ctx context.Context, history []application.ToolUseMessage, record func(domain.OnboardingAssistantAction) error) (string, error) {
	turn.history = history
	tools := turn.service.assistantToolCatalog()
	system := assistantSystemPrompt(turn.session, turn.service.assistantTools.Grammar())
	turn.service.logger.InfoContext(ctx, "setup assistant turn started", turn.logAttributes(
		"agents", strings.Join(turn.session.SelectedRuntimes, ","), "tools", len(tools),
		"system_prompt_bytes", len(system), "context_messages", len(turn.history))...)
	for step := 0; step < maxAssistantSteps; step++ {
		turn.steps++
		started := turn.service.clock.Now()
		output, err := turn.service.assistantModel.CompleteTurn(ctx, application.ToolUseInput{
			SystemPrompt: system, Messages: turn.history, Tools: tools, MaxOutputTokens: assistantMaxOutputTokens,
		})
		turn.inputTokens += output.InputTokens
		turn.outputTokens += output.OutputTokens
		turn.reasoningTokens += output.ReasoningTokens
		turn.logStep(ctx, output, err, len(system)+historyBytes(turn.history), started)
		if err != nil {
			return "", err
		}
		turn.history = append(turn.history, application.ToolUseMessage{Role: application.ToolUseRoleAssistant, Blocks: output.Blocks})
		if output.StopReason == application.ToolUseStopRefusal {
			return "", errors.New("the model declined to answer this request")
		}
		var calls []application.ToolUseBlock
		var text strings.Builder
		for _, block := range output.Blocks {
			switch block.Type {
			case application.ToolUseCall:
				calls = append(calls, block)
			case application.ToolUseText:
				text.WriteString(block.Text)
			}
		}
		if len(calls) == 0 {
			answer := strings.TrimSpace(text.String())
			if output.StopReason == application.ToolUseStopMaxTokens {
				return "", errors.New("the answer was cut short; ask again with a shorter request")
			}
			if answer == "" {
				return "", errors.New("the model answered with nothing")
			}
			return answer, nil
		}
		results := make([]application.ToolUseBlock, 0, len(calls))
		for _, call := range calls {
			started := turn.service.clock.Now()
			result, action := turn.call(ctx, call)
			turn.logToolCall(ctx, call, result, started)
			if err := record(action); err != nil {
				return "", err
			}
			results = append(results, application.ToolUseBlock{Type: application.ToolUseResult, ToolUseID: call.ID, Content: result.Content, IsError: result.IsError})
		}
		turn.history = append(turn.history, application.ToolUseMessage{Role: application.ToolUseRoleUser, Blocks: results})
	}
	return "", fmt.Errorf("it did not finish within %d steps; say what to change and it continues from here", maxAssistantSteps)
}

// logToolCall records one tool call the model made. A refused call is
// logged with the answer the model got, since that is what it acts on
// next.
func (turn *assistantTurn) logToolCall(ctx context.Context, call application.ToolUseBlock, result AssistantToolResult, started time.Time) {
	var scoped scopedCall
	_ = json.Unmarshal(call.Input, &scoped)
	attributes := []any{
		"session_id", turn.session.ID, "step", turn.steps, "tool", call.Name, "agent_slug", scoped.AgentSlug,
		"ok", !result.IsError, "input_bytes", len(call.Input), "duration_ms", turn.service.clock.Now().Sub(started).Milliseconds(),
	}
	if result.IsError {
		attributes = append(attributes, "result", firstLine(result.Content))
	}
	turn.service.logger.InfoContext(ctx, "setup assistant tool call", attributes...)
}

// historyBytes is roughly what the transcript adds to each request.
func historyBytes(history []application.ToolUseMessage) int {
	total := 0
	for _, message := range history {
		for _, block := range message.Blocks {
			total += len(block.Text) + len(block.Input) + len(block.Content)
		}
	}
	return total
}

// assistantToolCatalog is what the model may call: the pipeline tools
// from the management catalog plus the setup's own test prompt tool.
func (s *Service) assistantToolCatalog() []application.ToolUseTool {
	var tools []application.ToolUseTool
	for _, tool := range s.assistantTools.Tools() {
		if assistantToolNames[tool.Name] {
			tools = append(tools, tool)
		}
	}
	return append(tools, application.ToolUseTool{
		Name: setTestPromptTool,
		Description: "Record, for one agent, the instruction the person will give it in the test step so that the rules you wrote send the resulting call to a person. " +
			"Call it once per agent after the pipelines are saved. The sample arguments are run through the agent's active rules first: a call the rules would approve or deny is refused, so pick one they hand to a person, and keep it harmless (a dry run, a read that still matches, an echo).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"agent_slug":{"type":"string","description":"The agent, by slug, exactly as listed."},` +
			`"tool":{"type":"string","description":"The tool the instruction makes the agent call, exactly as listed for that agent."},` +
			`"prompt":{"type":"string","description":"One short instruction to the agent in plain language that makes it perform that call, for example: Run kubectl delete pod demo --dry-run=client and show me the output."},` +
			`"sample_arguments":{"type":"object","description":"The arguments the agent would send for that call, for example {\"command\": \"kubectl delete pod demo --dry-run=client\"}."}},` +
			`"required":["agent_slug","tool","prompt","sample_arguments"],"additionalProperties":false}`),
	})
}

// scopedCall is the part of a pipeline tool's input the guard reads.
type scopedCall struct {
	AgentSlug string `json:"agent_slug"`
}

// call executes one tool call within the setup's scope and describes it
// for the page.
func (turn *assistantTurn) call(ctx context.Context, call application.ToolUseBlock) (AssistantToolResult, domain.OnboardingAssistantAction) {
	if call.Name == setTestPromptTool {
		return turn.setTestPrompt(ctx, call.Input)
	}
	if !assistantToolNames[call.Name] {
		return refuse(call.Name, "", fmt.Sprintf("%s is not available here.", call.Name))
	}
	var scoped scopedCall
	_ = json.Unmarshal(call.Input, &scoped)
	if scoped.AgentSlug == "" {
		return refuse(call.Name, "", "Name agent_slug: the organization pipeline is not part of this setup. Choose one of "+strings.Join(turn.session.SelectedRuntimes, ", ")+".")
	}
	if !turn.session.Selected(scoped.AgentSlug) {
		return refuse(call.Name, scoped.AgentSlug, fmt.Sprintf("Only the agents this setup connected can be changed here: %s.", strings.Join(turn.session.SelectedRuntimes, ", ")))
	}
	input := call.Input
	if call.Name == "update_pipeline" {
		input = withDefaultPath(input, turn.session.Generated.EscalationPathKey)
	}
	result, err := turn.service.assistantTools.Call(ctx, turn.principal, call.Name, input)
	if err != nil {
		result = AssistantToolResult{Content: err.Error(), IsError: true}
	}
	return result, turn.describe(call.Name, scoped.AgentSlug, input, result)
}

// withDefaultPath makes every pipeline the assistant saves hand human
// exits to the person: the setup's path is the default unless the model
// named one.
func withDefaultPath(input json.RawMessage, pathKey string) json.RawMessage {
	if pathKey == "" {
		return input
	}
	var body map[string]any
	if err := json.Unmarshal(input, &body); err != nil || body == nil {
		return input
	}
	if current, _ := body["default_escalation_path"].(string); current != "" {
		return input
	}
	body["default_escalation_path"] = pathKey
	encoded, err := json.Marshal(body)
	if err != nil {
		return input
	}
	return encoded
}

func refuse(tool, agentSlug, message string) (AssistantToolResult, domain.OnboardingAssistantAction) {
	summary := "Refused a " + tool + " call"
	if agentSlug != "" {
		summary += " for " + agentSlug
	}
	return AssistantToolResult{Content: message, IsError: true},
		domain.OnboardingAssistantAction{Tool: tool, Summary: summary + ": " + firstLine(message), OK: false}
}

// describe phrases one call for the page from its input and answer.
func (turn *assistantTurn) describe(tool, agentSlug string, input json.RawMessage, result AssistantToolResult) domain.OnboardingAssistantAction {
	agent := turn.agentName(agentSlug)
	var answer struct {
		Revision struct {
			Revision int64 `json:"revision"`
		} `json:"revision"`
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal([]byte(result.Content), &answer)
	var summary string
	switch tool {
	case "get_pipeline":
		summary = "Read " + agent + "'s current rules"
	case "update_pipeline":
		summary = "Wrote " + agent + "'s rules"
		if answer.Revision.Revision > 0 {
			summary += fmt.Sprintf(" (revision %d)", answer.Revision.Revision)
		}
	case "preview_pipeline":
		var sample struct {
			Sample struct {
				Tool string `json:"tool"`
			} `json:"sample"`
		}
		_ = json.Unmarshal(input, &sample)
		summary = "Tested a " + firstNonEmpty(sample.Sample.Tool, "sample") + " call for " + agent
		if answer.Outcome != "" {
			summary += ": " + assistantOutcomeWord(answer.Outcome)
		}
	case "list_pipeline_revisions":
		summary = "Listed " + agent + "'s rule revisions"
	case "activate_pipeline_revision":
		var revision struct {
			Revision int64 `json:"revision"`
		}
		_ = json.Unmarshal(input, &revision)
		summary = fmt.Sprintf("Activated revision %d of %s's rules", revision.Revision, agent)
	default:
		summary = "Called " + tool
	}
	if result.IsError {
		summary += ", refused: " + firstLine(result.Content)
	}
	return domain.OnboardingAssistantAction{Tool: tool, Summary: summary, OK: !result.IsError}
}

func assistantOutcomeWord(outcome string) string {
	switch outcome {
	case "human":
		return "waits for a person"
	case "approve":
		return "runs on its own"
	case "deny":
		return "is refused"
	}
	return outcome
}

// agentName is how the page names the runtime's agent.
func (turn *assistantTurn) agentName(slug string) string {
	for _, enrollment := range turn.session.Enrollments {
		if string(enrollment.AgentSlug) == slug && enrollment.AgentName != "" {
			return enrollment.AgentName
		}
	}
	if known, ok := agentruntime.Lookup(agentruntime.Key(slug)); ok {
		return known.DisplayName
	}
	return slug
}

// setTestPromptInput is the setup's own tool's input.
type setTestPromptInput struct {
	AgentSlug       string          `json:"agent_slug"`
	Tool            string          `json:"tool"`
	Prompt          string          `json:"prompt"`
	SampleArguments json.RawMessage `json:"sample_arguments"`
}

// setTestPrompt records the test command for one runtime once the call
// it provokes is shown to reach a person under the active rules.
func (turn *assistantTurn) setTestPrompt(ctx context.Context, raw json.RawMessage) (AssistantToolResult, domain.OnboardingAssistantAction) {
	var input setTestPromptInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return refuse(setTestPromptTool, "", "The input could not be read: "+err.Error())
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.AgentSlug == "" || input.Tool == "" || input.Prompt == "" {
		return refuse(setTestPromptTool, input.AgentSlug, "agent_slug, tool, and prompt are all required.")
	}
	if !turn.session.Selected(input.AgentSlug) {
		return refuse(setTestPromptTool, input.AgentSlug, fmt.Sprintf("Only the agents this setup connected have a test: %s.", strings.Join(turn.session.SelectedRuntimes, ", ")))
	}
	agent := turn.agentName(input.AgentSlug)
	var tool *domain.OnboardingTool
	for index := range turn.session.Tools[input.AgentSlug] {
		if turn.session.Tools[input.AgentSlug][index].Name == input.Tool {
			tool = &turn.session.Tools[input.AgentSlug][index]
			break
		}
	}
	if tool == nil {
		return refuse(setTestPromptTool, input.AgentSlug, fmt.Sprintf("%s is not a tool %s can call; use a name from the list.", input.Tool, agent))
	}
	arguments := input.SampleArguments
	if len(arguments) == 0 || string(arguments) == "null" {
		arguments = json.RawMessage(`{}`)
	}
	active, err := turn.service.pipelines.GetActive(ctx, turn.principal, domain.AgentSlug(input.AgentSlug))
	if err != nil {
		return refuse(setTestPromptTool, input.AgentSlug, "The active rules could not be read: "+err.Error())
	}
	definitions := make([]approvalblock.Definition, len(active.Blocks))
	for index, block := range active.Blocks {
		definitions[index] = block.Definition
	}
	preview, err := turn.service.pipelines.Preview(ctx, turn.principal, domain.AgentSlug(input.AgentSlug), definitions, active.Revision.DefaultEscalationPath, approvalpipeline.PreviewSample{
		Request: approvalpipeline.BlockRequest{Tool: tool.Name, Server: tool.Server, Arguments: arguments},
		Agent:   approvalpipeline.BlockAgent{Slug: input.AgentSlug, Name: agent},
	})
	if err != nil {
		return refuse(setTestPromptTool, input.AgentSlug, "The sample could not be checked against the rules: "+err.Error())
	}
	if preview.Outcome != approvalpipeline.BlockHuman {
		return refuse(setTestPromptTool, input.AgentSlug, fmt.Sprintf("With the active rules this call %s (%s), so it would not show up in the test. Pick a call the rules send to a person, or change the rules first.",
			assistantOutcomeWord(string(preview.Outcome)), firstNonEmpty(preview.TerminalBlockKey, "end of pipeline")))
	}
	command, ok := agentruntime.TestCommandWithPrompt(agentruntime.Key(input.AgentSlug), agentruntime.SelectedTool{
		Name: tool.Name, Source: agentruntime.Source(tool.Source), Server: tool.Server, Kind: agentruntime.ToolKind(tool.Kind),
	}, input.Prompt)
	if !ok {
		return refuse(setTestPromptTool, input.AgentSlug, "This runtime has no test command.")
	}
	commands := map[string]domain.OnboardingTestCommand{}
	for runtime, existing := range turn.session.TestCommands {
		commands[runtime] = existing
	}
	commands[input.AgentSlug] = domain.OnboardingTestCommand{Tool: tool.Name, Command: command.Shell, Note: command.Note}
	updated, err := turn.service.sessions.UpdateOnboardingSession(context.WithoutCancel(ctx), application.UpdateOnboardingSessionInput{
		OrganizationID: turn.session.OrganizationID, SessionID: turn.session.ID,
		Patch: domain.OnboardingSessionPatch{TestCommands: &commands},
		Now:   turn.service.clock.Now(),
	})
	if err != nil {
		return refuse(setTestPromptTool, input.AgentSlug, "The test command could not be recorded: "+err.Error())
	}
	turn.session = updated
	answer, _ := json.Marshal(map[string]any{"ok": true, "agent_slug": input.AgentSlug, "tool": tool.Name, "command": command.Shell})
	return AssistantToolResult{Content: string(answer)},
		domain.OnboardingAssistantAction{Tool: setTestPromptTool, Summary: "Chose " + agent + "'s test: a " + tool.Name + " call", OK: true}
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	if len(text) > 160 {
		text = text[:157] + "..."
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// assistantSystemPrompt is the stable prefix of every turn: who the
// assistant is, the agents and their tools, how rules are written, and
// the grammar itself, so no turn is spent reading it through a tool.
func assistantSystemPrompt(session domain.OnboardingSession, editionGrammar string) string {
	var prompt strings.Builder
	prompt.WriteString(`You are the setup assistant inside withHuman's guided setup. withHuman decides which of an AI agent's tool calls wait for a human's approval: every call an agent makes goes through its approval pipeline, and the first block that decides wins (approve, deny, or human). The person has just connected the agents below and now tells you, in their own words, which calls should wait for them. You turn that into each agent's approval pipeline with the tools you have, check it, and explain what you did in plain language.

## The agents

`)
	for _, runtime := range session.SelectedRuntimes {
		fmt.Fprintf(&prompt, "- %s: agent slug `%s`.\n", runtimeDisplayName(session, runtime), runtime)
	}
	if session.Generated.EscalationPathKey != "" {
		fmt.Fprintf(&prompt, "\nEvery human exit reaches the person through the escalation path `%s`; it is set as the default path of every pipeline you save, so leave escalation_path off individual blocks.\n", session.Generated.EscalationPathKey)
	}
	prompt.WriteString("\n## The tools each agent can call\n\nTool names are exact: /request/tool carries them as written here. \"Waits\" marks the tools the starting rules already send to a person.\n")
	for _, runtime := range session.SelectedRuntimes {
		fmt.Fprintf(&prompt, "\n### %s (`%s`)\n", runtimeDisplayName(session, runtime), runtime)
		tools := session.Tools[runtime]
		for index, tool := range tools {
			if index == maxPromptToolsPerRuntime {
				fmt.Fprintf(&prompt, "- ...and %d more.\n", len(tools)-index)
				break
			}
			prompt.WriteString(describeToolForPrompt(tool))
		}
		if len(tools) == 0 {
			prompt.WriteString("- No tools were discovered; write rules on tool names the person gives you.\n")
		}
	}
	prompt.WriteString(`
## How to work

1. Read the agent's current rules with get_pipeline first: the person may have refined them in an earlier message, and update_pipeline replaces the whole block list.
2. Write the rules as blocks. Put the blocks that send calls to a person first, then end with an always block whose outcome is approve when everything else should run without asking; when the person wants everything reviewed, end with always human instead. A request no block decides waits for a person.
3. Save with update_pipeline for each agent the request concerns. Do not touch an agent the person did not mention.
4. Check with preview_pipeline: at least one sample that should wait for a person and one that should run. Fix and save again when an outcome is wrong.
5. Call set_test_prompt once per agent you changed, with a harmless call your rules send to a person. It is refused if the sample would not reach a person.
6. Answer the person.

Writing rules:
- Conditions test the request. /request/tool is the tool name; /request/arguments/<field> are the call's arguments, with the argument names listed above (a shell tool's command text is usually /request/arguments/command). Use regex (RE2) for command patterns, in for lists of tool names, starts_with for prefixes.
- A shell rule like "kubectl commands that change the cluster" is a cel block on the shell tool with a regex over the command, for example ` + "`kubectl\\s+(apply|delete|scale|rollout|patch|edit|create|replace)`" + `. Read-only verbs (get, describe, logs) stay out of it.
- Name blocks as short verb-first labels ("Review kubectl changes"); reasons are one full sentence a reviewer reads ("kubectl commands that change the cluster wait for a person.").
- Every block id is key-shaped and unique; every condition id is a UUID you generate, unique across the revision.
- Rules reflect what the person asked, nothing more. If the request is too vague to act on, ask one short question instead of guessing.
- Never use em dashes.

## Answering

Reply in plain language, two to six sentences: what now waits for the person and what runs on its own, per agent, and what the test step will ask the agent to do. No JSON and no block ids. When something was refused and you could not fix it, say so plainly.

## Grammar

`)
	prompt.WriteString(mcpreference.MustRead("pipeline_blocks"))
	prompt.WriteString(editionGrammar)
	prompt.WriteString("\n\n")
	prompt.WriteString(mcpreference.MustRead("conditions"))
	return prompt.String()
}

func runtimeDisplayName(session domain.OnboardingSession, runtime string) string {
	for _, enrollment := range session.Enrollments {
		if enrollment.Runtime == runtime && enrollment.AgentName != "" {
			return enrollment.AgentName
		}
	}
	if known, ok := agentruntime.Lookup(agentruntime.Key(runtime)); ok {
		return known.DisplayName
	}
	return runtime
}

// describeToolForPrompt is one tool's line in the inventory.
func describeToolForPrompt(tool domain.OnboardingTool) string {
	var line strings.Builder
	fmt.Fprintf(&line, "- %s", tool.Name)
	switch {
	case tool.Server != "":
		fmt.Fprintf(&line, " (MCP server %s)", tool.Server)
	case tool.Kind != "" && tool.Kind != string(agentruntime.ToolOther):
		fmt.Fprintf(&line, " (built-in, %s)", tool.Kind)
	default:
		line.WriteString(" (built-in)")
	}
	if description := strings.TrimSpace(strings.SplitN(tool.Description, "\n", 2)[0]); description != "" {
		if len(description) > maxPromptDescription {
			description = description[:maxPromptDescription-3] + "..."
		}
		line.WriteString(": " + description)
	}
	if names := argumentNames(tool.InputSchema); len(names) > 0 {
		line.WriteString(". Arguments: " + strings.Join(names, ", "))
	}
	if tool.Suggested {
		line.WriteString(". Waits")
	}
	line.WriteString("\n")
	return line.String()
}

// argumentNames lists a tool's top-level argument names from its input
// schema, required ones first.
func argumentNames(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil || len(parsed.Properties) == 0 {
		return nil
	}
	required := map[string]bool{}
	var names []string
	for _, name := range parsed.Required {
		if _, ok := parsed.Properties[name]; ok && !required[name] {
			required[name] = true
			names = append(names, name)
		}
	}
	var optional []string
	for name := range parsed.Properties {
		if !required[name] {
			optional = append(optional, name)
		}
	}
	sort.Strings(optional)
	names = append(names, optional...)
	if len(names) > maxPromptArguments {
		names = append(names[:maxPromptArguments], "...")
	}
	return names
}
