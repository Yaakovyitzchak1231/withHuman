package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

// billingDelegate hands billing-agent requests to the billing pipeline.
func billingDelegate() approvalblock.Definition {
	return delegateDefinition("to-billing", "billing", approvalblock.Field("/agent/slug").Equals("billing"))
}

func TestADelegateIsSkippedByRequestsItsConditionDoesNotMatch(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	service, _ := branchFixture(t, now, NewBlockRegistry(),
		branchScope{key: "billing", definitions: []approvalblock.Definition{alwaysDefinition("billing-denies", approvalblock.Deny)}},
		branchScope{key: "entry", definitions: []approvalblock.Definition{
			delegateDefinition("refunds", "billing", approvalblock.Field("/request/tool").Equals("stripe.refund")),
			alwaysDefinition("everything-else", approvalblock.Approve),
		}},
	)
	input, err := service.Evaluate(context.Background(), branchRequest(now, "deploy", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	// refunds 0 | billing-denies 1 | everything-else 2: the target's blocks
	// sit inside the delegate's range and are skipped with it.
	if got := steps(evaluatedEvents(t, input.AuditEvents)); got != "refunds:next@0 everything-else:approve@2" || input.Decision == nil {
		t.Fatalf("a skipped delegate must not run its target: %s", got)
	}
	started := auditData(t, input.AuditEvents[1])
	pipelines := started["pipelines"].([]any)
	if len(pipelines) != 2 || started["block_count"] != float64(3) || pipelines[1].(map[string]any)["pipeline_key"] != "billing" {
		t.Fatalf("the run must pin every pipeline of the closure, entered or not: %#v", started)
	}
}

func TestBlocksInsideADelegatedPipelineDecide(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	service, repository := branchFixture(t, now, NewBlockRegistry(),
		branchScope{key: "billing", definitions: []approvalblock.Definition{
			pipelineDefinition("fraudster", approvalblock.Deny, approvalblock.Field("/request/arguments/customer").Equals("fraudster")),
		}},
		branchScope{key: "entry", definitions: []approvalblock.Definition{billingDelegate(), alwaysDefinition("everything-else", approvalblock.Approve)}},
	)
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{"customer":"fraudster"}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.Decision == nil || input.Decision.Status != domain.ApprovalDenied {
		t.Fatalf("the delegated pipeline's block did not decide: %#v", input.Decision)
	}
	evaluated := evaluatedEvents(t, input.AuditEvents)
	if got := steps(evaluated); got != "to-billing:enter@0 fraudster:deny@1" {
		t.Fatalf("got steps %s", got)
	}
	if evaluated[0]["target_pipeline_key"] != "billing" || evaluated[1]["pipeline_key"] != "billing" || evaluated[1]["pipeline_id"] != string(repository.revisions["billing"][1].ID) {
		t.Fatalf("evidence must name the delegated pipeline: %#v", evaluated)
	}
	completed := completedEvent(t, input.AuditEvents)
	if completed["terminal_block_pipeline_key"] != "billing" || completed["terminal_block_key"] != "fraudster" {
		t.Fatalf("unexpected terminal evidence: %#v", completed)
	}
}

// A delegated pipeline that runs out hands the request to a human on its
// own default, else the defaults of the pipelines that delegated into it,
// nearest first, else nobody in particular.
func TestADelegatedPipelineThatDecidesNothingRoutesByTheNearestDefault(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	tests := []struct {
		name           string
		billingPath    string
		entryPath      string
		wantPath       string
		wantReason     string
		wantRevisionOf string
	}{
		{"target default wins", "payments-review", "on-call", "payments-review", domain.RoutingReasonPipelineDefault, "billing"},
		{"entry default next", "", "on-call", "on-call", domain.RoutingReasonPipelineDefault, "entry"},
		{"no default anywhere", "", "", "", domain.RoutingReasonNoEscalation, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repository := branchFixture(t, now, NewBlockRegistry(),
				branchScope{key: "billing", defaultPath: test.billingPath, definitions: []approvalblock.Definition{
					pipelineDefinition("never", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("other")),
				}},
				branchScope{key: "entry", defaultPath: test.entryPath, definitions: []approvalblock.Definition{billingDelegate(), alwaysDefinition("everything-else", approvalblock.Approve)}},
			)
			input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
			if err != nil {
				t.Fatal(err)
			}
			if got := steps(evaluatedEvents(t, input.AuditEvents)); got != "to-billing:enter@0 never:next@1 to-billing:human@0" || input.Decision != nil {
				t.Fatalf("running out of the delegated pipeline must end the run: %s", got)
			}
			closing := evaluatedEvents(t, input.AuditEvents)[2]
			if closing["end_of_branch"] != true || closing["reason"] != "" || closing["attempt_count"] != float64(0) {
				t.Fatalf("unexpected closing event: %#v", closing)
			}
			completed := completedEvent(t, input.AuditEvents)
			if completed["reason_code"] != "end_of_branch" || completed["terminal_block_key"] != "to-billing" || completed["terminal_block_pipeline_key"] != "entry" {
				t.Fatalf("unexpected completion: %#v", completed)
			}
			route := input.HumanReview
			if route.PathKey != test.wantPath || route.Reason != test.wantReason || route.BlockID != "" {
				t.Fatalf("route = %#v", route)
			}
			if test.wantRevisionOf != "" && route.PipelineRevisionID != repository.revisions[test.wantRevisionOf][1].ID {
				t.Fatalf("the route must name the revision that supplied the default: %#v", route)
			}
		})
	}
}

func TestAHumanExitInsideADelegatedPipelineFollowsTheSameChain(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	service, repository := branchFixture(t, now, NewBlockRegistry(),
		branchScope{key: "billing", definitions: []approvalblock.Definition{
			pipelineDefinition("ask", approvalblock.Human, approvalblock.Field("/request/tool").Exists()),
		}},
		branchScope{key: "entry", defaultPath: "on-call", definitions: []approvalblock.Definition{billingDelegate()}},
	)
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	route := input.HumanReview
	if route.PathKey != "on-call" || route.Reason != domain.RoutingReasonPipelineDefault || route.PipelineRevisionID != repository.revisions["entry"][1].ID {
		t.Fatalf("a human exit without a path inside a delegated pipeline must inherit the enclosing default: %#v", route)
	}
}

// A delegate whose condition cannot be evaluated hands the request to a
// person on the default of the pipeline holding the block, never the
// target's, because the request never entered the target.
func TestAFailedDelegateConditionRoutesFromThePipelineHoldingIt(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	failing := delegateDefinition("to-billing", "billing", approvalblock.Field("/request/arguments/amount").LessThan(10))
	service, repository := branchFixture(t, now, NewBlockRegistry(),
		branchScope{key: "billing", defaultPath: "payments-review", definitions: []approvalblock.Definition{alwaysDefinition("billing-approves", approvalblock.Approve)}},
		branchScope{key: "entry", defaultPath: "on-call", definitions: []approvalblock.Definition{failing, alwaysDefinition("rest", approvalblock.Approve)}},
	)
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{"amount":"many"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, input.AuditEvents)); got != "to-billing:human@0" || input.Decision != nil {
		t.Fatalf("a failed condition must stop, never skip or enter: %s", got)
	}
	if route := input.HumanReview; route.PathKey != "on-call" || route.PipelineRevisionID != repository.revisions["entry"][1].ID {
		t.Fatalf("route = %#v", route)
	}
}

func TestAnEmptyDelegatedPipelineEndsAtOnce(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	repository := newMemoryPipelineRepository()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
	if _, err := management.Create(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", "Billing", nil, Route{EscalationPath: "payments-review"}); err != nil {
		t.Fatal(err)
	}
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "entry", "Entry", []approvalblock.Definition{billingDelegate(), alwaysDefinition("rest", approvalblock.Approve)}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), "entry", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	service := initializedExecutionService(t, repository, executionAgentRepositoryStub{agent: domain.Agent{ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing"}}, fixedPipelineClock{now: now})
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, input.AuditEvents)); got != "to-billing:enter@0 to-billing:human@0" {
		t.Fatalf("an entered empty pipeline must end at once: %s", got)
	}
	if route := input.HumanReview; route.PathKey != "payments-review" || route.PipelineRevisionID != repository.revisions["billing"][0].ID {
		t.Fatalf("the empty target's default must route the request: %#v", route)
	}
}

// Two delegates may name the same pipeline, and a pipeline may be reached
// two ways: the flattened program holds one copy per delegate, only one
// copy ever runs, and one revision is pinned for the key.
func TestADiamondPinsOneRevisionPerKey(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	service, _ := branchFixture(t, now, NewBlockRegistry(),
		branchScope{key: "checks", definitions: []approvalblock.Definition{alwaysDefinition("checked", approvalblock.Approve)}},
		branchScope{key: "billing", definitions: []approvalblock.Definition{
			delegateDefinition("billing-checks", "checks", approvalblock.Field("/request/tool").Exists()),
		}},
		branchScope{key: "entry", definitions: []approvalblock.Definition{
			delegateDefinition("entry-checks", "checks", approvalblock.Field("/request/tool").Equals("other")),
			billingDelegate(),
		}},
	)
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	// entry-checks 0, checked 1 | to-billing 2, billing-checks 3, checked 4
	if got := steps(evaluatedEvents(t, input.AuditEvents)); got != "entry-checks:next@0 to-billing:enter@2 billing-checks:enter@3 checked:approve@4" {
		t.Fatalf("got steps %s", got)
	}
	started := auditData(t, input.AuditEvents[1])
	if pipelines := started["pipelines"].([]any); len(pipelines) != 3 || started["block_count"] != float64(5) {
		t.Fatalf("a diamond lists each key once and counts each copy: %#v", started)
	}
}

func TestAnAsynchronousBlockInsideADelegatedPipelineResumesThere(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockNext))
	service, repository := scriptedBranchFixture(t, now, check,
		branchScope{key: "billing", definitions: []approvalblock.Definition{scriptedDefinition(), alwaysDefinition("billing-approves", approvalblock.Approve)}},
		branchScope{key: "entry", definitions: []approvalblock.Definition{billingDelegate(), alwaysDefinition("rest", approvalblock.Deny)}},
	)
	initialization, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if initialization.Suspension == nil || initialization.Suspension.ResumePosition != 1 {
		t.Fatalf("unexpected suspension: %#v", initialization.Suspension)
	}
	if pins := initialization.Suspension.PinnedRevisions; pins["billing"] != repository.revisions["billing"][1].ID || pins["entry"] != repository.revisions["entry"][1].ID {
		t.Fatalf("unexpected pins: %#v", pins)
	}
	suspended := auditData(t, initialization.AuditEvents[len(initialization.AuditEvents)-1])
	if suspended["suspended_block_pipeline_key"] != "billing" {
		t.Fatalf("unexpected suspension evidence: %#v", suspended)
	}
	resume, err := service.Resume(context.Background(), initialization.Request, *initialization.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(evaluatedEvents(t, resume.AuditEvents)); got != "refund-check:next@1 billing-approves:approve@2" || resume.Decision == nil {
		t.Fatalf("the resumed segment must continue inside the delegated pipeline: %s", got)
	}
	if len(check.Inputs) != 1 || check.Inputs[0].PipelineKey != "billing" {
		t.Fatalf("the block must know which pipeline it ran in: %#v", check.Inputs)
	}
}

// A resume whose pins no longer resolve, or resolve to another key's
// revision, abandons the run to a person instead of executing a partial
// effective pipeline.
func TestResumeWithAMissingOrForeignPinAbandonsToAHuman(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockApprove))
	service, repository := scriptedBranchFixture(t, now, check,
		branchScope{key: "billing", definitions: []approvalblock.Definition{scriptedDefinition()}},
		branchScope{key: "entry", defaultPath: "on-call", definitions: []approvalblock.Definition{billingDelegate()}},
	)
	initialization, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]domain.ID){
		"missing": func(pins map[string]domain.ID) { delete(pins, "billing") },
		"foreign": func(pins map[string]domain.ID) { pins["billing"] = repository.revisions["entry"][1].ID },
	} {
		t.Run(name, func(t *testing.T) {
			suspension := *initialization.Suspension
			suspension.PinnedRevisions = map[string]domain.ID{}
			for key, id := range initialization.Suspension.PinnedRevisions {
				suspension.PinnedRevisions[key] = id
			}
			mutate(suspension.PinnedRevisions)
			resume, err := service.Resume(context.Background(), initialization.Request, suspension)
			if err != nil {
				t.Fatal(err)
			}
			if resume.Decision != nil || len(resume.AuditEvents) != 1 || auditData(t, resume.AuditEvents[0])["reason_code"] != "compiled_cache_unavailable" {
				t.Fatalf("expected an abandoned resume: %#v", resume)
			}
			if resume.HumanReview.PathKey != "on-call" {
				t.Fatalf("an abandoned run routes by the entry default: %#v", resume.HumanReview)
			}
		})
	}
	if len(check.Inputs) != 0 {
		t.Fatal("an abandoned resume must not call the model")
	}
}

// The memory repository, like the store, cannot activate a cycle; a cycle
// that appears anyway (here written straight into the repository) is
// refused when the run is flattened, never partially executed.
func TestACycleOrDepthOverTheLimitHandsTheRequestToAHuman(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	service, repository := branchFixture(t, now, NewBlockRegistry(),
		branchScope{key: "billing", definitions: []approvalblock.Definition{alwaysDefinition("billing-approves", approvalblock.Approve)}},
		branchScope{key: "checks", definitions: []approvalblock.Definition{delegateDefinition("back", "billing", approvalblock.Field("/request/tool").Exists())}},
		branchScope{key: "entry", defaultPath: "on-call", definitions: []approvalblock.Definition{billingDelegate()}},
	)
	loop := delegateDefinition("loop", "checks", approvalblock.Field("/request/tool").Exists())
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", "", []approvalblock.Definition{loop}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateRequestApprovalPipelineRevision(context.Background(), application.ActivateRequestApprovalPipelineRevisionInput{
		OrganizationID: "organization-1", PipelineKey: "billing", Revision: created.Revision.Revision, ExpectedActiveRevision: 2, ActorID: "membership-1", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.Decision != nil || len(evaluatedEvents(t, input.AuditEvents)) != 0 {
		t.Fatalf("a cyclic graph must never run: %#v", input)
	}
	completed := completedEvent(t, input.AuditEvents)
	if completed["reason_code"] != "compiled_cache_unavailable" || input.HumanReview.PathKey != "on-call" {
		t.Fatalf("unexpected fallback: %#v %#v", completed, input.HumanReview)
	}
}

func TestPreviewExpandsDelegatesAndRefusesSelfDelegation(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	repository := newMemoryPipelineRepository()
	registry := NewBlockRegistry()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, registry), fixedPipelineClock{now: now})
	principal := pipelineManagerPrincipal("organization-1")
	if _, err := management.Create(context.Background(), principal, "billing", "Billing", []approvalblock.Definition{
		pipelineDefinition("small", approvalblock.Approve, approvalblock.Field("/request/arguments/amount").LessThan(10)),
	}, Route{EscalationPath: "payments-review"}); err != nil {
		t.Fatal(err)
	}
	compiled := NewCompiledPipelineState(repository, registry)
	if err := compiled.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	management = NewManagementService(repository, compiled, fixedPipelineClock{now: now})
	document := []approvalblock.Definition{billingDelegate(), alwaysDefinition("rest", approvalblock.Deny)}
	sample := func(tool, arguments string) PreviewSample {
		return PreviewSample{Request: BlockRequest{Tool: tool, Arguments: json.RawMessage(arguments)}, Agent: BlockAgent{Slug: "billing", Name: "Billing"}}
	}

	result, err := management.Preview(context.Background(), principal, "entry", document, Route{EscalationPath: "on-call"}, sample("stripe.refund", `{"amount":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BlockApprove || len(result.Blocks) != 2 || result.Blocks[1].PipelineKey != "billing" || result.Blocks[1].BlockKey != "small" {
		t.Fatalf("preview did not walk the delegated pipeline: %+v", result)
	}
	result, err = management.Preview(context.Background(), principal, "entry", document, Route{EscalationPath: "on-call"}, sample("stripe.refund", `{"amount":50}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BlockHuman || result.ReasonCode != "end_of_branch" || result.TerminalBlockKey != "to-billing" ||
		result.EscalationPath != "payments-review" || result.EscalationPathPipelineKey != "billing" {
		t.Fatalf("running out of the delegated pipeline must route by its default: %+v", result)
	}
	other := PreviewSample{Request: BlockRequest{Tool: "deploy", Arguments: json.RawMessage(`{}`)}, Agent: BlockAgent{Slug: "support", Name: "Support"}}
	result, err = management.Preview(context.Background(), principal, "entry", document, Route{EscalationPath: "on-call"}, other)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BlockDeny || len(result.Blocks) != 2 || result.Blocks[1].BlockKey != "rest" {
		t.Fatalf("a skipped delegate must reach the block after it: %+v", result)
	}

	self := []approvalblock.Definition{delegateDefinition("me", "entry", approvalblock.Field("/request/tool").Exists())}
	if _, err := management.Preview(context.Background(), principal, "entry", self, Route{}, sample("deploy", `{}`)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("delegating to the entry pipeline previewed: %v", err)
	}
	loop := []approvalblock.Definition{delegateDefinition("me", "billing", approvalblock.Field("/request/tool").Exists())}
	if _, err := management.Preview(context.Background(), principal, "billing", loop, Route{}, sample("deploy", `{}`)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a pipeline delegating to itself previewed: %v", err)
	}
	if _, err := management.CreateRevision(context.Background(), principal, "billing", "", loop, Route{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a pipeline delegating to itself was stored: %v", err)
	}
	missing := []approvalblock.Definition{delegateDefinition("gone", "nowhere", approvalblock.Field("/request/tool").Exists())}
	if _, err := management.Preview(context.Background(), principal, "entry", missing, Route{}, sample("deploy", `{}`)); !errors.Is(err, domain.ErrRequestApprovalPipelineNotFound) {
		t.Fatalf("a missing target previewed: %v", err)
	}
}

// Delegate conditions spend the same budget as cel blocks, so an entry
// pipeline of 999 non-matching conditions and one delegate whose target
// holds one more still fits the audit cap.
func TestADelegateAtTheBudgetFitsTheAuditCap(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	checks := make([]approvalblock.Definition, approvalblock.MaximumPipelineBlocks-1)
	for index := range checks {
		checks[index] = pipelineDefinition(fmt.Sprintf("check-%d", index), approvalblock.Deny, approvalblock.Field("/request/tool").Equals("other"))
	}
	service, _ := branchFixture(t, now, NewBlockRegistry(),
		branchScope{key: "billing", definitions: []approvalblock.Definition{
			pipelineDefinition("last", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("other")),
		}},
		branchScope{key: "entry", definitions: append(checks, billingDelegate())},
	)
	input, err := service.Evaluate(context.Background(), branchRequest(now, "deploy", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	evaluated := evaluatedEvents(t, input.AuditEvents)
	last := evaluated[len(evaluated)-1]
	if len(evaluated) != approvalblock.MaximumPipelineBlocks+1 || last["error_code"] != string(ErrorResourceLimit) {
		t.Fatalf("got %d evaluated events, last %#v", len(evaluated), last)
	}
	input.AuditEvents = append(input.AuditEvents, domain.AuditEvent{
		ID: domain.ID(uuid.NewString()), OrganizationID: input.Request.OrganizationID, EventType: "routing.evaluated",
		ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: input.Request.ID,
		Data: json.RawMessage(`{}`), OccurredAt: now,
	})
	if err := input.Validate(); err != nil {
		t.Fatalf("a run at the budget must fit the audit cap: %v", err)
	}
}

func TestManagementReferencesArchiveAndUsesForDelegates(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	repository := newMemoryPipelineRepository()
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now}, WithPipelineKeyResolver(repository))
	principal := pipelineManagerPrincipal("organization-1")
	if _, err := service.Create(context.Background(), principal, "billing", "Billing", nil, Route{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), principal, "entry", "Entry", nil, Route{}); !errors.Is(err, domain.ErrRequestApprovalPipelineEntry) {
		t.Fatalf("creating the entry pipeline = %v", err)
	}
	if _, err := service.CreateRevision(context.Background(), principal, "entry", "Entry", []approvalblock.Definition{
		delegateDefinition("gone", "nowhere", approvalblock.Field("/request/tool").Exists()),
	}, Route{}); !errors.Is(err, domain.ErrRequestApprovalPipelineNotFound) {
		t.Fatalf("a delegate to a missing pipeline was stored: %v", err)
	}
	created, err := service.CreateRevision(context.Background(), principal, "entry", "Entry", []approvalblock.Definition{billingDelegate()}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	references, err := service.References(context.Background(), principal, "entry", created.Revision.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(references.Pipelines) != 1 || references.Pipelines[0].PipelineKey != "billing" || references.Pipelines[0].Name != "Billing" || !references.Pipelines[0].Active || !references.Activatable {
		t.Fatalf("references = %+v", references)
	}
	if _, err := service.Activate(context.Background(), principal, "entry", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	uses, err := service.Uses(context.Background(), principal, "billing")
	if err != nil || len(uses) != 1 || uses[0].PipelineKey != "entry" || uses[0].BlockKey != "to-billing" {
		t.Fatalf("uses = %+v, %v", uses, err)
	}
	var inUse domain.RequestApprovalPipelineInUseError
	if _, err := service.Archive(context.Background(), principal, "billing"); !errors.As(err, &inUse) || len(inUse.Uses) != 1 {
		t.Fatalf("archiving a delegated pipeline = %v", err)
	}
	if _, err := service.Archive(context.Background(), principal, "entry"); !errors.Is(err, domain.ErrRequestApprovalPipelineEntry) {
		t.Fatalf("archiving the entry pipeline = %v", err)
	}
	if _, err := service.Activate(context.Background(), principal, "entry", 1, created.Revision.Revision); err != nil {
		t.Fatal(err)
	}
	archived, err := service.Archive(context.Background(), principal, "billing")
	if err != nil || len(archived) != 1 || archived[0].ArchivedAt == nil {
		t.Fatalf("archive = %+v, %v", archived, err)
	}
	references, err = service.References(context.Background(), principal, "entry", created.Revision.Revision)
	if err != nil || references.Activatable || references.Pipelines[0].Exists {
		t.Fatalf("references after archive = %+v, %v", references, err)
	}
	scoped := principal.WithGrants([]domain.Grant{{Permission: domain.PermissionPipelineWrite, ScopeKind: domain.ScopePipeline, ScopeID: "billing"}})
	if _, err := service.Create(context.Background(), scoped, "payments", "Payments", nil, Route{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a pipeline-scoped grant created a pipeline: %v", err)
	}
	if _, err := service.CreateRevision(context.Background(), scoped, "entry", "", nil, Route{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a billing-scoped grant wrote the entry pipeline: %v", err)
	}
}
