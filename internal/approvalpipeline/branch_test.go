package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

func branchDefinition(id string, condition approvalblock.Condition, blocks ...approvalblock.Definition) approvalblock.Definition {
	condition.ID = uuid.NewString()
	return *approvalblock.DefineBranch(id, blocks,
		approvalblock.Name(id), approvalblock.When(condition), approvalblock.Reason("Nothing in "+id+" decided."))
}

func refundsBranch(blocks ...approvalblock.Definition) approvalblock.Definition {
	branch := branchDefinition("refunds", approvalblock.Field("/request/tool").Equals("stripe.refund"), blocks...)
	branch.Branch.EscalationPath = "payments-review"
	return branch
}

// refundsPipeline is the shape the feature exists for: a branch only
// refunds enter, holding checks that may decide, then a catch-all.
func refundsPipeline() []approvalblock.Definition {
	return []approvalblock.Definition{
		refundsBranch(
			pipelineDefinition("small-refunds", approvalblock.Approve, approvalblock.Field("/request/arguments/amount").LessThan(10)),
			pipelineDefinition("fraudster", approvalblock.Deny, approvalblock.Field("/request/arguments/customer").Equals("fraudster")),
		),
		alwaysDefinition("everything-else", approvalblock.Approve),
	}
}

type branchScope struct {
	slug        domain.AgentSlug
	definitions []approvalblock.Definition
	defaultPath string
	// askOwner makes the default ask the agent's owner, with defaultPath
	// as the fallback.
	askOwner bool
}

// branchFixture activates each scope's document and returns an execution
// service for the billing agent.
func branchFixture(t *testing.T, now time.Time, registry *BlockRegistry, scopes ...branchScope) (*ExecutionService, *memoryPipelineRepository) {
	t.Helper()
	repository := newMemoryPipelineRepository()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, registry), fixedPipelineClock{now: now})
	for _, scope := range scopes {
		created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), scope.slug, scope.definitions, Route{EscalationPath: scope.defaultPath, AskAgentOwner: scope.askOwner})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), scope.slug, created.Revision.Revision, 1); err != nil {
			t.Fatal(err)
		}
	}
	compiled := NewCompiledPipelineState(repository, registry)
	if err := compiled.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	agents := executionAgentRepositoryStub{agent: domain.Agent{ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing"}}
	return NewExecutionService(repository, agents, compiled, fixedPipelineClock{now: now.Add(time.Second)}), repository
}

func branchRequest(now time.Time, tool, arguments string) domain.ApprovalRequest {
	request := executionRequest(now, "request-"+uuid.NewString(), "key-1", "billing", json.RawMessage(arguments))
	request.Tool = tool
	return request
}

func evaluatedEvents(t *testing.T, events []domain.AuditEvent) []map[string]any {
	t.Helper()
	var evaluated []map[string]any
	for _, event := range events {
		if event.EventType == blockEvaluatedEvent {
			evaluated = append(evaluated, auditData(t, event))
		}
	}
	return evaluated
}

func completedEvent(t *testing.T, events []domain.AuditEvent) map[string]any {
	t.Helper()
	for _, event := range events {
		if event.EventType == pipelineCompletedEvent {
			return auditData(t, event)
		}
	}
	t.Fatalf("no completed event in %v", eventTypes(events))
	return nil
}

// steps summarizes evaluated events as "key:outcome@position".
func steps(evaluated []map[string]any) string {
	parts := make([]string, len(evaluated))
	for index, event := range evaluated {
		parts[index] = fmt.Sprintf("%v:%v@%v", event["block_key"], event["outcome"], event["position"])
	}
	return strings.Join(parts, " ")
}

func snapshotIDOf(t *testing.T, repository *memoryPipelineRepository, slug domain.AgentSlug, key string) domain.ID {
	t.Helper()
	for _, revision := range repository.revisions[slug] {
		if !revision.IsActive {
			continue
		}
		for _, id := range revision.BlockIDs {
			if repository.blocks[id].BlockKey == key {
				return id
			}
		}
	}
	t.Fatalf("no active block %q in scope %q", key, slug)
	return ""
}

func TestBranchIsSkippedByRequestsItsConditionDoesNotMatch(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	service, _ := branchFixture(t, now, NewBlockRegistry(), branchScope{slug: "billing", definitions: refundsPipeline()})
	input, err := service.Evaluate(context.Background(), branchRequest(now, "deploy", `{"amount":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.Request.Status != domain.ApprovalApproved {
		t.Fatalf("the catch-all after the branch did not decide: %#v", input)
	}
	evaluated := evaluatedEvents(t, input.AuditEvents)
	if got := steps(evaluated); got != "refunds:next@0 everything-else:approve@3" {
		t.Fatalf("got steps %s", got)
	}
	if trace, _ := evaluated[0]["trace"].([]any); len(trace) == 0 || evaluated[0]["block_type"] != "branch" {
		t.Fatalf("a skipped branch must record its condition trace: %#v", evaluated[0])
	}
	if completed := completedEvent(t, input.AuditEvents); completed["attempted_block_count"] != float64(2) || completed["terminal_block_key"] != "everything-else" {
		t.Fatalf("unexpected completion: %#v", completed)
	}
}

func TestBlocksInsideAnEnteredBranchDecide(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	service, _ := branchFixture(t, now, NewBlockRegistry(), branchScope{slug: "billing", definitions: refundsPipeline()})
	for _, test := range []struct {
		arguments string
		status    domain.ApprovalStatus
		steps     string
	}{
		{`{"amount":5}`, domain.ApprovalApproved, "refunds:enter@0 small-refunds:approve@1"},
		{`{"amount":50,"customer":"fraudster"}`, domain.ApprovalDenied, "refunds:enter@0 small-refunds:next@1 fraudster:deny@2"},
	} {
		input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", test.arguments))
		if err != nil {
			t.Fatal(err)
		}
		if input.Request.Status != test.status || input.Decision == nil {
			t.Fatalf("%s: got %s", test.arguments, input.Request.Status)
		}
		if got := steps(evaluatedEvents(t, input.AuditEvents)); got != test.steps {
			t.Fatalf("%s: got steps %s, want %s", test.arguments, got, test.steps)
		}
	}
}

func TestABranchThatDecidesNothingAsksAHumanAndEndsThePipeline(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	service, repository := branchFixture(t, now, NewBlockRegistry(), branchScope{slug: "billing", definitions: refundsPipeline(), defaultPath: "on-call"})
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{"amount":50,"customer":"alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.Decision != nil || input.Request.Status != domain.ApprovalPending {
		t.Fatalf("a branch end must not decide: %#v", input.Decision)
	}
	evaluated := evaluatedEvents(t, input.AuditEvents)
	if got := steps(evaluated); got != "refunds:enter@0 small-refunds:next@1 fraudster:next@2 refunds:human@0" {
		t.Fatalf("the catch-all after the branch must not run: %s", got)
	}
	closing := evaluated[3]
	if closing["end_of_branch"] != true || closing["attempt_count"] != float64(0) || closing["cost"] != float64(0) ||
		closing["reason"] != "Nothing in refunds decided." || closing["error_code"] != "" {
		t.Fatalf("unexpected closing evidence: %#v", closing)
	}
	if trace, isList := closing["trace"].([]any); !isList || len(trace) != 0 {
		t.Fatalf("the closing event evaluates nothing: %#v", closing["trace"])
	}
	completed := completedEvent(t, input.AuditEvents)
	if completed["reason_code"] != "end_of_branch" || completed["terminal_block_key"] != "refunds" || completed["attempted_block_count"] != float64(3) {
		t.Fatalf("unexpected completion: %#v", completed)
	}
	route := input.HumanReview
	if route == nil || route.PathKey != "payments-review" || route.Reason != domain.RoutingReasonBlockEscalation ||
		route.BlockID != snapshotIDOf(t, repository, "billing", "refunds") {
		t.Fatalf("the branch's own path must take its end: %#v", route)
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("initialization must validate: %v", err)
	}
}

func TestABranchConditionThatFailsHandsTheRequestToTheDefault(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	branch := branchDefinition("large", approvalblock.Field("/request/arguments/amount").GreaterThan(10),
		alwaysDefinition("inside", approvalblock.Approve))
	branch.Branch.EscalationPath = "payments-review"
	service, _ := branchFixture(t, now, NewBlockRegistry(), branchScope{slug: "billing", definitions: []approvalblock.Definition{branch, alwaysDefinition("rest", approvalblock.Approve)}, defaultPath: "on-call"})
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{"amount":"12"}`))
	if err != nil {
		t.Fatal(err)
	}
	evaluated := evaluatedEvents(t, input.AuditEvents)
	if got := steps(evaluated); got != "large:human@0" || evaluated[0]["error_code"] != string(ErrorEvaluation) {
		t.Fatalf("a failed condition must stop, never skip: %s %#v", got, evaluated)
	}
	if completed := completedEvent(t, input.AuditEvents); completed["reason_code"] != "block_error" || completed["terminal_block_key"] != "large" {
		t.Fatalf("unexpected completion: %#v", completed)
	}
	if route := input.HumanReview; route == nil || route.PathKey != "on-call" || route.Reason != domain.RoutingReasonPipelineDefault {
		t.Fatalf("a failed condition must use the pipeline default, not the branch's path: %#v", route)
	}
}

func TestAnInnerBranchEndEndsThePipeline(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	outer := branchDefinition("outer", approvalblock.Field("/request/tool").Equals("stripe.refund"),
		branchDefinition("inner", approvalblock.Field("/request/arguments/amount").GreaterThan(100),
			pipelineDefinition("fraudster", approvalblock.Deny, approvalblock.Field("/request/arguments/customer").Equals("fraudster")),
		),
		pipelineDefinition("approve-rest", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("stripe.refund")),
	)
	service, _ := branchFixture(t, now, NewBlockRegistry(), branchScope{slug: "billing", definitions: []approvalblock.Definition{outer, alwaysDefinition("everything-else", approvalblock.Deny)}})

	large, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{"amount":500,"customer":"alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, large.AuditEvents)); got != "outer:enter@0 inner:enter@1 fraudster:next@2 inner:human@1" {
		t.Fatalf("an inner end must not fall back into the outer branch: %s", got)
	}
	if completed := completedEvent(t, large.AuditEvents); completed["terminal_block_key"] != "inner" || completed["reason_code"] != "end_of_branch" {
		t.Fatalf("unexpected completion: %#v", completed)
	}

	small, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{"amount":50}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, small.AuditEvents)); got != "outer:enter@0 inner:next@1 approve-rest:approve@3" || small.Request.Status != domain.ApprovalApproved {
		t.Fatalf("a skipped inner branch must continue with its sibling: %s", got)
	}
}

func TestAnOrganizationBranchEndPreemptsTheAgentSection(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	service, _ := branchFixture(t, now, NewBlockRegistry(),
		branchScope{slug: "", definitions: []approvalblock.Definition{refundsBranch(
			pipelineDefinition("never", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("other")),
		)}},
		branchScope{slug: "billing", definitions: []approvalblock.Definition{alwaysDefinition("agent-approves", approvalblock.Approve)}},
	)
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, input.AuditEvents)); got != "refunds:enter@0 never:next@1 refunds:human@0" || input.Decision != nil {
		t.Fatalf("the agent section must not run after an organization branch end: %s", got)
	}
	other, err := service.Evaluate(context.Background(), branchRequest(now, "deploy", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, other.AuditEvents)); got != "refunds:next@0 agent-approves:approve@2" {
		t.Fatalf("a skipped organization branch must reach the agent section: %s", got)
	}
}

func TestBlocksInsideABranchDoNotInheritItsEscalationPath(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	service, _ := branchFixture(t, now, NewBlockRegistry(), branchScope{slug: "billing", defaultPath: "on-call", definitions: []approvalblock.Definition{
		refundsBranch(pipelineDefinition("ask", approvalblock.Human, approvalblock.Field("/request/tool").Equals("stripe.refund"))),
	}})
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if route := input.HumanReview; route == nil || route.PathKey != "on-call" || route.Reason != domain.RoutingReasonPipelineDefault {
		t.Fatalf("a human exit inside a branch that names no path must use the pipeline default: %#v", route)
	}
}

func TestAnAlwaysInsideABranchStopsItsLaterBlocks(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	service, _ := branchFixture(t, now, NewBlockRegistry(), branchScope{slug: "billing", definitions: []approvalblock.Definition{
		refundsBranch(alwaysDefinition("stop", approvalblock.Human), pipelineDefinition("unreachable", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("stripe.refund"))),
		alwaysDefinition("everything-else", approvalblock.Approve),
	}})
	refund, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, refund.AuditEvents)); got != "refunds:enter@0 stop:human@1" {
		t.Fatalf("got steps %s", got)
	}
	other, err := service.Evaluate(context.Background(), branchRequest(now, "deploy", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if other.Request.Status != domain.ApprovalApproved {
		t.Fatalf("blocks after the branch must stay reachable: %s", other.Request.Status)
	}
}

// Branch conditions spend the same budget as cel blocks, and a branch
// that runs every one of its blocks and then ends still fits the audit
// cap once routing adds its event.
func TestABranchAtTheBudgetFitsTheAuditCap(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	children := make([]approvalblock.Definition, approvalblock.MaximumPipelineBlocks-1)
	for index := range children {
		children[index] = pipelineDefinition(fmt.Sprintf("check-%d", index), approvalblock.Deny, approvalblock.Field("/request/tool").Equals("other"))
	}
	service, _ := branchFixture(t, now, NewBlockRegistry(), branchScope{slug: "billing", definitions: []approvalblock.Definition{
		branchDefinition("everything", approvalblock.Field("/request/tool").Exists(), children...),
	}})
	input, err := service.Evaluate(context.Background(), branchRequest(now, "deploy", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	evaluated := evaluatedEvents(t, input.AuditEvents)
	if len(evaluated) != approvalblock.MaximumPipelineBlocks+1 || evaluated[len(evaluated)-1]["end_of_branch"] != true {
		t.Fatalf("got %d evaluated events", len(evaluated))
	}
	input.AuditEvents = append(input.AuditEvents, domain.AuditEvent{
		ID: domain.ID(uuid.NewString()), OrganizationID: input.Request.OrganizationID, EventType: "routing.evaluated",
		ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: input.Request.ID,
		Data: json.RawMessage(`{}`), OccurredAt: now,
	})
	if err := input.Validate(); err != nil {
		t.Fatalf("a full branch must fit the audit cap: %v", err)
	}
}

func TestBranchConditionEntersSkipsOrFailsClosed(t *testing.T) {
	branch := branchDefinition("large", approvalblock.Field("/request/arguments/amount").GreaterThan(10), alwaysDefinition("inside", approvalblock.Approve))
	compiled, err := CompileBranchBlock(branch)
	if err != nil {
		t.Fatal(err)
	}
	input := func(arguments string) RequestApprovalBlockInput {
		return RequestApprovalBlockInput{
			OrganizationID: "organization-1",
			Request:        BlockRequest{Tool: "stripe.refund", Arguments: json.RawMessage(arguments)},
			Agent:          BlockAgent{Slug: "billing", Name: "billing-agent"},
			Budget:         &BlockExecutionBudget{},
		}
	}
	if result := executeBranchCondition(context.Background(), compiled, input(`{"amount":12}`)); result.Outcome != BlockEnter || len(result.Evidence.Trace) == 0 {
		t.Fatalf("a match must enter: %#v", result)
	}
	if result := executeBranchCondition(context.Background(), compiled, input(`{"amount":2}`)); result.Outcome != BlockNext {
		t.Fatalf("a non-match must skip: %#v", result)
	}
	if result := executeBranchCondition(context.Background(), compiled, input(`{"amount":"12"}`)); result.Outcome != BlockHuman || result.ErrorCode != ErrorEvaluation {
		t.Fatalf("an evaluation error must fail closed: %#v", result)
	}
	exhausted := input(`{"amount":12}`)
	for range approvalblock.MaximumCELBlocksPerRequest {
		if err := exhausted.Budget.Consume(approvalblock.TypeCEL, approvalblock.MaximumCELBlocksPerRequest); err != nil {
			t.Fatal(err)
		}
	}
	if result := executeBranchCondition(context.Background(), compiled, exhausted); result.Outcome != BlockHuman || result.ErrorCode != ErrorResourceLimit {
		t.Fatalf("a branch condition must spend the condition budget: %#v", result)
	}
	// Enter is evidence, never an outcome an ordinary block may return.
	if result := ExecuteRequestApprovalBlock(context.Background(), compiled, input(`{"amount":12}`)); result.Outcome != BlockHuman || result.ErrorCode != ErrorInvalidBlockOutput {
		t.Fatalf("enter must not pass the ordinary block boundary: %#v", result)
	}
}

func TestCompilationEnforcesPipelineRulesAcrossBranches(t *testing.T) {
	registry := NewBlockRegistry()
	shared := approvalblock.Field("/request/tool").Equals("stripe.refund")
	withCondition := func(definition approvalblock.Definition, condition approvalblock.Condition) approvalblock.Definition {
		definition.CEL.When = condition
		return definition
	}
	nestedDisabled := alwaysDefinition("inside", approvalblock.Approve)
	nestedDisabled.Enabled = false
	tooMany := make([]approvalblock.Definition, approvalblock.MaximumPipelineBlocks)
	for index := range tooMany {
		tooMany[index] = alwaysDefinition(fmt.Sprintf("block-%d", index), approvalblock.Approve)
	}
	for name, definitions := range map[string][]approvalblock.Definition{
		"nested key repeats a top-level key": {
			branchDefinition("refunds", shared, alwaysDefinition("rest", approvalblock.Approve)),
			alwaysDefinition("rest", approvalblock.Approve),
		},
		"branch condition id repeats a nested one": {
			func() approvalblock.Definition {
				branch := branchDefinition("refunds", shared, withCondition(pipelineDefinition("inside", approvalblock.Approve, shared), shared))
				branch.Branch.When = shared
				return branch
			}(),
		},
		"nested block is disabled":       {branchDefinition("refunds", shared, nestedDisabled)},
		"branch is empty":                {branchDefinition("refunds", shared)},
		"nested blocks exceed the limit": {branchDefinition("refunds", shared, tooMany...)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registry.compileDefinitions(definitions); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("compiled an invalid document: %v", err)
			}
		})
	}
	if _, err := registry.compileDefinitions([]approvalblock.Definition{branchDefinition("refunds", shared, tooMany[:approvalblock.MaximumPipelineBlocks-1]...)}); err != nil {
		t.Fatalf("a document of exactly the limit, counting nested blocks, was refused: %v", err)
	}
}

func TestStoredBranchesNestInPreOrder(t *testing.T) {
	registry := NewBlockRegistry()
	document := []approvalblock.Definition{
		refundsBranch(
			pipelineDefinition("small-refunds", approvalblock.Approve, approvalblock.Field("/request/arguments/amount").LessThan(10)),
			branchDefinition("large", approvalblock.Field("/request/arguments/amount").GreaterThan(100), alwaysDefinition("ask", approvalblock.Human)),
		),
		alwaysDefinition("everything-else", approvalblock.Approve),
	}
	inputs, err := registry.snapshotInputs(document)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, len(inputs))
	snapshots := make([]domain.RequestApprovalBlock, len(inputs))
	for index, input := range inputs {
		keys[index] = input.BlockKey
		snapshots[index] = domain.RequestApprovalBlock{
			ID: input.ID, OrganizationID: "organization-1", BlockKey: input.BlockKey, Name: input.Name, BlockType: input.BlockType,
			ConfigVersion: input.ConfigVersion, Config: input.Config, TimeoutMS: input.TimeoutMS, MaxAttempts: input.MaxAttempts,
		}
	}
	if got := strings.Join(keys, " "); got != "refunds small-refunds large ask everything-else" {
		t.Fatalf("snapshots are not in pre-order: %s", got)
	}
	var stored map[string]any
	if err := json.Unmarshal(inputs[0].Config, &stored); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(stored["block_keys"]) != "[small-refunds large]" || stored["blocks"] != nil || stored["escalation_path"] != "payments-review" {
		t.Fatalf("a stored branch names its direct blocks by key: %s", inputs[0].Config)
	}

	nested, err := registry.nestSnapshots(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if len(nested) != 2 || len(nested[0].Blocks) != 2 || len(nested[0].Blocks[1].Blocks) != 1 || nested[0].Blocks[1].Blocks[0].SnapshotID != snapshots[3].ID {
		t.Fatalf("the tree did not read back: %#v", nested)
	}
	if definition := nested[0].Definition; len(definition.Branch.Blocks) != 2 || definition.Branch.Blocks[1].Branch.Blocks[0].ID != "ask" {
		t.Fatalf("the branch definition must carry its blocks: %#v", definition.Branch)
	}

	swapped := append([]domain.RequestApprovalBlock(nil), snapshots...)
	swapped[1], swapped[2] = swapped[2], swapped[1]
	if _, err := registry.nestSnapshots(swapped); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a stored tree out of order must be refused: %v", err)
	}
	if _, err := registry.nestSnapshots(snapshots[:3]); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a branch missing its blocks must be refused: %v", err)
	}
}

func TestBranchCursorWalksTheTree(t *testing.T) {
	// refunds [ small, large [ ask ] ], rest
	blocks := []compiledBlock{
		{Key: "refunds", Branch: true, Parent: -1, End: 4},
		{Key: "small", Parent: 0, End: 2},
		{Key: "large", Branch: true, Parent: 0, End: 4},
		{Key: "ask", Parent: 2, End: 4},
		{Key: "rest", Parent: -1, End: 5},
	}
	resumed := newBranchCursor(blocks, 3)
	if step, index := resumed.next(); step != stepBlock || index != 3 {
		t.Fatalf("resume must start at its position: %v %d", step, index)
	}
	resumed.advance()
	if step, index := resumed.next(); step != stepBranchEnd || index != 2 {
		t.Fatalf("the innermost entered branch must end first: %v %d", step, index)
	}

	skipping := newBranchCursor(blocks, 0)
	skipping.skip()
	if step, index := skipping.next(); step != stepBlock || index != 4 {
		t.Fatalf("skipping must pass the whole subtree: %v %d", step, index)
	}
	skipping.advance()
	if step, _ := skipping.next(); step != stepDone {
		t.Fatalf("expected the section to end: %v", step)
	}
}

func scriptedBranchFixture(t *testing.T, now time.Time, check *scriptedModel, scopes ...branchScope) (*ExecutionService, *memoryPipelineRepository) {
	t.Helper()
	registry := NewBlockRegistry(withScripted(check), WithClock(fixedPipelineClock{now: now}))
	return branchFixture(t, now, registry, scopes...)
}

func TestAnAsynchronousBlockInsideAMatchedBranchSuspendsAndResumesInside(t *testing.T) {
	now := time.Unix(4_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockApprove))
	service, _ := scriptedBranchFixture(t, now, check, branchScope{slug: "", definitions: []approvalblock.Definition{
		branchDefinition("deploys", approvalblock.Field("/request/tool").Equals("deploy"),
			scriptedDefinition()),
	}})
	initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if initialization.Suspension == nil || initialization.Suspension.ResumePosition != 1 || len(check.Inputs) != 0 {
		t.Fatalf("the request path must suspend in front of the asynchronous block inside the branch: %#v", initialization.Suspension)
	}
	if got := steps(evaluatedEvents(t, initialization.AuditEvents)); got != "deploys:enter@0" {
		t.Fatalf("got steps %s", got)
	}
	resume, err := service.Resume(context.Background(), initialization.Request, *initialization.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, resume.AuditEvents)); got != "refund-check:approve@1" {
		t.Fatalf("the resume must continue inside the branch without evaluating it again: %s", got)
	}
	if resume.Decision == nil || resume.Decision.Status != domain.ApprovalApproved {
		t.Fatalf("expected the asynchronous block to decide: %#v", resume.Decision)
	}
}

func TestAnAsynchronousBlockInASkippedBranchIsNeverCalled(t *testing.T) {
	now := time.Unix(4_000, 0).UTC()
	check := newScripted()
	service, _ := scriptedBranchFixture(t, now, check, branchScope{slug: "", definitions: []approvalblock.Definition{
		branchDefinition("refunds", approvalblock.Field("/request/tool").Equals("stripe.refund"), scriptedDefinition()),
		alwaysDefinition("everything-else", approvalblock.Approve),
	}})
	input, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if input.Suspension != nil || input.Request.Status != domain.ApprovalApproved || len(check.Inputs) != 0 {
		t.Fatalf("a skipped branch must not suspend or call its asynchronous block: %#v", input)
	}
}

func TestAnAsynchronousBlockThatContinuesAtTheEndOfItsBranchEndsTheBranch(t *testing.T) {
	now := time.Unix(4_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockNext))
	service, _ := scriptedBranchFixture(t, now, check, branchScope{slug: "", definitions: []approvalblock.Definition{
		branchDefinition("deploys", approvalblock.Field("/request/tool").Equals("deploy"),
			scriptedDefinition()),
		alwaysDefinition("everything-else", approvalblock.Approve),
	}})
	initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	resume, err := service.Resume(context.Background(), initialization.Request, *initialization.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, resume.AuditEvents)); got != "refund-check:next@1 deploys:human@0" || resume.Decision != nil {
		t.Fatalf("the branch must end in the resumed segment: %s", got)
	}
	if completed := completedEvent(t, resume.AuditEvents); completed["reason_code"] != "end_of_branch" || completed["attempted_block_count"] != float64(2) {
		t.Fatalf("unexpected completion: %#v", completed)
	}
	if err := resume.Validate(); err != nil {
		t.Fatalf("resume input must validate: %v", err)
	}
}

func TestResumeReentersNestedBranchesAcrossSections(t *testing.T) {
	now := time.Unix(4_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockNext))
	service, _ := scriptedBranchFixture(t, now, check,
		branchScope{slug: "", definitions: []approvalblock.Definition{
			pipelineDefinition("deny-other", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("other")),
		}},
		branchScope{slug: "billing", definitions: []approvalblock.Definition{
			branchDefinition("outer", approvalblock.Field("/request/tool").Equals("deploy"),
				branchDefinition("inner", approvalblock.Field("/request/tool").Exists(),
					scriptedDefinition()),
				alwaysDefinition("after-inner", approvalblock.Approve),
			),
		}},
	)
	initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	// deny-other 0 | outer 1, inner 2, refund-judge 3, after-inner 4
	if initialization.Suspension == nil || initialization.Suspension.ResumePosition != 3 {
		t.Fatalf("unexpected suspension: %#v", initialization.Suspension)
	}
	if got := steps(evaluatedEvents(t, initialization.AuditEvents)); got != "deny-other:next@0 outer:enter@1 inner:enter@2" {
		t.Fatalf("got steps %s", got)
	}
	resume, err := service.Resume(context.Background(), initialization.Request, *initialization.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, resume.AuditEvents)); got != "refund-check:next@3 inner:human@2" {
		t.Fatalf("the resumed segment must end the inner branch, not reach after-inner: %s", got)
	}
}

func TestAWebhookInsideABranchIsCalledOnlyForMatchingRequests(t *testing.T) {
	now := time.Unix(5_000, 0).UTC()
	deliverer := &fakeDeliverer{script: []scriptedDelivery{ok(`{"outcome":"next","metadata":{"risk_score":12}}`)}}
	endpoints := &fakeEndpoints{endpoint: domain.WebhookEndpointRevision{ID: testEndpointRevisionID, EndpointKey: testEndpointKey, Revision: 1, IsActive: true, OrganizationID: "organization-1", Name: "fraud-service", URL: "https://fraud.example.com/hooks/approval"}, secret: "whsec_test"}
	registry := NewBlockRegistry(WithWebhooks(WebhookRuntime{Endpoints: endpoints, Deliverer: deliverer, Backoff: func(int) time.Duration { return 0 }}), WithClock(fixedPipelineClock{now: now}))
	service, _ := branchFixture(t, now, registry, branchScope{slug: "", definitions: []approvalblock.Definition{
		refundsBranch(
			webhookDefinition(),
			pipelineDefinition("approve-low-risk", approvalblock.Approve, approvalblock.Field("/previous_metadata/organization/fraud-check/risk_score").LessThan(50)),
		),
		alwaysDefinition("everything-else", approvalblock.Deny),
	}})

	other, err := service.Evaluate(context.Background(), branchRequest(now, "deploy", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if other.Suspension != nil || other.Request.Status != domain.ApprovalDenied || len(deliverer.deliveries) != 0 {
		t.Fatalf("a skipped branch must not deliver: %#v", other)
	}

	refund, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{"amount":900}`))
	if err != nil {
		t.Fatal(err)
	}
	resume, err := service.Resume(context.Background(), refund.Request, *refund.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, resume.AuditEvents)); got != "fraud-check:next@1 approve-low-risk:approve@2" || resume.Decision == nil {
		t.Fatalf("a later block in the branch must read the webhook's metadata: %s", got)
	}
	if len(deliverer.deliveries) != 1 {
		t.Fatalf("got %d deliveries, want 1", len(deliverer.deliveries))
	}
}

func TestPreviewReportsBranchSteps(t *testing.T) {
	repository := newMemoryPipelineRepository()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(6_000, 0).UTC()})
	sample := func(tool, arguments string) PreviewSample {
		return PreviewSample{Request: BlockRequest{Tool: tool, Arguments: json.RawMessage(arguments)}, Agent: BlockAgent{Slug: "billing", Name: "Billing"}}
	}
	preview := func(definitions []approvalblock.Definition, tool, arguments string) PreviewResult {
		t.Helper()
		result, err := management.Preview(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", definitions, Route{EscalationPath: "on-call"}, sample(tool, arguments))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	summary := func(result PreviewResult) string {
		parts := make([]string, len(result.Blocks))
		for index, block := range result.Blocks {
			parts[index] = fmt.Sprintf("%s:%s@%d", block.BlockKey, block.Outcome, block.Position)
			if block.EndOfBranch {
				parts[index] += ":end"
			}
		}
		return strings.Join(parts, " ")
	}

	ended := preview(refundsPipeline(), "stripe.refund", `{"amount":50}`)
	if got := summary(ended); got != "refunds:enter@0 small-refunds:next@1 fraudster:next@2 refunds:human@0:end" {
		t.Fatalf("got %s", got)
	}
	if ended.ReasonCode != "end_of_branch" || ended.TerminalBlockKey != "refunds" || ended.EscalationPath != "payments-review" ||
		ended.EscalationPathReason != domain.RoutingReasonBlockEscalation || ended.Blocks[3].Reason != "Nothing in refunds decided." {
		t.Fatalf("unexpected branch end: %#v", ended)
	}

	skipped := preview(refundsPipeline(), "deploy", `{}`)
	if got := summary(skipped); got != "refunds:next@0 everything-else:approve@3" || skipped.Outcome != BlockApprove {
		t.Fatalf("got %s", got)
	}

	failing := refundsPipeline()
	failing[0].Branch.When = approvalblock.ConditionID(uuid.NewString(), approvalblock.Field("/request/arguments/amount").GreaterThan(10))
	failed := preview(failing, "stripe.refund", `{"amount":"12"}`)
	if failed.ReasonCode != "block_error" || failed.TerminalBlockKey != "refunds" || failed.EscalationPath != "on-call" || failed.EscalationPathReason != domain.RoutingReasonPipelineDefault {
		t.Fatalf("a failed condition must use the default path: %#v", failed)
	}
}

func TestManagementStoresReadsAndChecksNestedBlocks(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	repository := newMemoryPipelineRepository()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
	document := refundsPipeline()
	inner := pipelineDefinition("ask", approvalblock.Human, approvalblock.Field("/request/arguments/amount").GreaterThan(1000))
	inner.CEL.EscalationPath = "fraud-team"
	document[0].Branch.Blocks = append(document[0].Branch.Blocks, inner)
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", document, Route{EscalationPath: "on-call"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Revision.BlockIDs) != 5 {
		t.Fatalf("every block is its own snapshot: %d", len(created.Revision.BlockIDs))
	}
	if top := created.Blocks; len(top) != 2 || len(top[0].Blocks) != 3 || top[0].Blocks[2].SnapshotID == "" || top[0].Blocks[2].Definition.ID != "ask" {
		t.Fatalf("reads must nest blocks with their snapshots: %#v", top)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	references, err := management.References(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", created.Revision.Revision)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, path := range references.EscalationPaths {
		paths[path.PathKey] = strings.Join(path.BlockKeys, ",")
	}
	if paths["payments-review"] != "refunds" || paths["fraud-team"] != "ask" {
		t.Fatalf("references must cover a branch's path and nested blocks' paths: %#v", references.EscalationPaths)
	}

	gated := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry(withScripted(newScripted()), WithEntitlements(&denyingEntitlements{}))), fixedPipelineClock{now: now})
	_, err = gated.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", []approvalblock.Definition{
		refundsBranch(scriptedDefinition()),
	}, Route{})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a gated kind inside a branch must be gated on the plan: %v", err)
	}
}
