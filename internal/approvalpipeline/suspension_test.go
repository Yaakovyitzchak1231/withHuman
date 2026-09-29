package approvalpipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

// suspensionFixture activates an organization pipeline of a non-matching
// CEL block, an asynchronous scripted block, and a trailing CEL approve,
// and returns an execution service whose scripted block answers from the
// replies.
func suspensionFixture(t *testing.T, now time.Time, check *scriptedModel) (*ExecutionService, *memoryPipelineRepository) {
	t.Helper()
	repository := newMemoryPipelineRepository()
	registry := NewBlockRegistry(withScripted(check), WithClock(fixedPipelineClock{now: now}))
	management := NewManagementService(repository, NewCompiledPipelineState(repository, registry), fixedPipelineClock{now: now})
	definitions := []approvalblock.Definition{
		pipelineDefinition("deny-other", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("other")),
		scriptedDefinition(),
		pipelineDefinition("approve-rest", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	}
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "", definitions, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), "", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	compiled := NewCompiledPipelineState(repository, registry)
	if err := compiled.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	agents := executionAgentRepositoryStub{agent: domain.Agent{ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing"}}
	return NewExecutionService(repository, agents, compiled, fixedPipelineClock{now: now.Add(time.Second)}), repository
}

func eventTypes(events []domain.AuditEvent) []string {
	types := make([]string, len(events))
	for index, event := range events {
		types[index] = event.EventType
	}
	return types
}

func TestEvaluateSuspendsInFrontOfTheFirstAsynchronousBlock(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockApprove))
	service, repository := suspensionFixture(t, now, check)

	input, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{"version":"1"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if input.Suspension == nil || input.Decision != nil || input.Routing != nil || input.Request.Status != domain.ApprovalPending {
		t.Fatalf("expected a pending suspended initialization: %#v", input)
	}
	if len(check.Inputs) != 0 {
		t.Fatal("the request path must never call the model")
	}
	want := []string{"approval.created", pipelineStartedEvent, blockEvaluatedEvent, pipelineSuspendedEvent}
	if got := eventTypes(input.AuditEvents); len(got) != len(want) || got[3] != want[3] || got[2] != want[2] {
		t.Fatalf("got events %v, want %v", got, want)
	}
	suspension := *input.Suspension
	if suspension.ResumePosition != 1 || suspension.AttemptedBlockCount != 1 || suspension.OrganizationPipelineID != repository.revisions[""][1].ID || suspension.AgentPipelineID != "" {
		t.Fatalf("unexpected suspension: %#v", suspension)
	}
	suspended := auditData(t, input.AuditEvents[3])
	if suspended["suspended_block_key"] != "refund-check" || suspended["resume_position"] != float64(1) {
		t.Fatalf("unexpected suspension evidence: %#v", suspended)
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("suspended initialization must validate: %v", err)
	}
}

func TestResumeRunsTheAsynchronousBlockAndDecides(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockDeny))
	service, _ := suspensionFixture(t, now, check)
	request := executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{"version":"1"}`))
	initialization, err := service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	resume, err := service.Resume(context.Background(), initialization.Request, *initialization.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if resume.Decision == nil || resume.Decision.Status != domain.ApprovalDenied || resume.Decision.Source != domain.DecisionSourcePipeline {
		t.Fatalf("expected an automatic denial: %#v", resume.Decision)
	}
	if resume.EvaluationID != initialization.Suspension.EvaluationID {
		t.Fatal("resume must continue the same evaluation")
	}
	want := []string{blockEvaluatedEvent, pipelineCompletedEvent, "approval.decided"}
	if got := eventTypes(resume.AuditEvents); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got events %v, want %v", got, want)
	}
	evaluated := auditData(t, resume.AuditEvents[0])
	if evaluated["block_key"] != "refund-check" || evaluated["position"] != float64(1) {
		t.Fatalf("resume must evaluate the suspended block at its combined position: %#v", evaluated)
	}
	completed := auditData(t, resume.AuditEvents[1])
	if completed["attempted_block_count"] != float64(2) || completed["terminal_block_key"] != "refund-check" {
		t.Fatalf("completion must carry the counters from both segments: %#v", completed)
	}
	if err := resume.Validate(); err != nil {
		t.Fatalf("resume input must validate: %v", err)
	}
}

func TestResumeContinuesPastAnAsynchronousBlockThatContinues(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockNext))
	service, _ := suspensionFixture(t, now, check)
	request := executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{"version":"1"}`))
	initialization, err := service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	resume, err := service.Resume(context.Background(), initialization.Request, *initialization.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if resume.Decision == nil || resume.Decision.Status != domain.ApprovalApproved {
		t.Fatalf("the trailing CEL block must decide after the asynchronous block continues: %#v", resume.Decision)
	}
	got := eventTypes(resume.AuditEvents)
	if len(got) != 4 || got[0] != blockEvaluatedEvent || got[1] != blockEvaluatedEvent {
		t.Fatalf("expected the asynchronous block and the trailing block to be evaluated: %v", got)
	}
	trailing := auditData(t, resume.AuditEvents[1])
	if trailing["block_key"] != "approve-rest" || trailing["position"] != float64(2) {
		t.Fatalf("unexpected trailing evaluation: %#v", trailing)
	}
}

func TestResumeHandsToAHumanWhenTheAsynchronousBlockSaysSo(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockHuman))
	service, _ := suspensionFixture(t, now, check)
	request := executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{"version":"1"}`))
	initialization, err := service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	resume, err := service.Resume(context.Background(), initialization.Request, *initialization.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if resume.Decision != nil {
		t.Fatalf("an escalation must not decide: %#v", resume.Decision)
	}
	completed := auditData(t, resume.AuditEvents[len(resume.AuditEvents)-1])
	if completed["outcome"] != "human" || completed["reason_code"] != "block_outcome" {
		t.Fatalf("unexpected completion: %#v", completed)
	}
}

func TestAbandonCompletesToAHumanWithoutRunningTheAsynchronousBlock(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockApprove))
	service, _ := suspensionFixture(t, now, check)
	request := executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{"version":"1"}`))
	initialization, err := service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := service.Abandon(context.Background(), initialization.Request, *initialization.Suspension, AbandonReasonCode)
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.Decision != nil || len(abandoned.AuditEvents) != 1 || abandoned.AuditEvents[0].EventType != pipelineCompletedEvent {
		t.Fatalf("abandon must complete without deciding: %#v", abandoned)
	}
	if data := auditData(t, abandoned.AuditEvents[0]); data["reason_code"] != AbandonReasonCode || data["outcome"] != "human" {
		t.Fatalf("unexpected abandon evidence: %#v", data)
	}
	if len(check.Inputs) != 0 {
		t.Fatal("abandon must not call the model")
	}
	var _ application.ResumeApprovalInput = abandoned
}
