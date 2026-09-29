package approvalpipeline

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

func TestExecutionSelectsAgentStopsAtFirstTerminalAndCachesRevision(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	pipelines := activeExecutionPipeline(t, now, "billing", []approvalblock.Definition{
		pipelineDefinition("skip", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("other.tool")),
		pipelineDefinition("deny", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("deploy")),
		pipelineDefinition("later", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	})
	management := NewManagementService(pipelines, NewCompiledPipelineState(pipelines, NewBlockRegistry()), fixedPipelineClock{now: now})
	release, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "release", []approvalblock.Definition{
		pipelineDefinition("approve", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), "release", release.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	agents := executionAgentRepositoryStub{agent: domain.Agent{
		ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing",
	}}
	service := initializedExecutionService(t, pipelines, agents, fixedPipelineClock{now: now.Add(time.Second)})

	request := executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{"version":"1"}`))
	input, err := service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if input.Request.Status != domain.ApprovalDenied || input.Decision == nil || input.Decision.Source != domain.DecisionSourcePipeline {
		t.Fatalf("pipeline denial was not prepared: %#v", input)
	}
	wantTypes := []string{
		"approval.created",
		pipelineStartedEvent,
		blockEvaluatedEvent,
		blockEvaluatedEvent,
		pipelineCompletedEvent,
		"approval.decided",
	}
	if len(input.AuditEvents) != len(wantTypes) {
		t.Fatalf("got %d audit events, want %d: %#v", len(input.AuditEvents), len(wantTypes), input.AuditEvents)
	}
	for index, want := range wantTypes {
		if input.AuditEvents[index].EventType != want {
			t.Fatalf("audit event %d = %q, want %q", index, input.AuditEvents[index].EventType, want)
		}
	}
	firstBlock := auditData(t, input.AuditEvents[2])
	secondBlock := auditData(t, input.AuditEvents[3])
	if firstBlock["block_key"] != "skip" || firstBlock["outcome"] != "next" || secondBlock["block_key"] != "deny" || secondBlock["outcome"] != "deny" {
		t.Fatalf("blocks were not evaluated in terminal order: first=%#v second=%#v", firstBlock, secondBlock)
	}
	// The configured reason and the block's name are what the review
	// context shows a reviewer, so the event has to carry them.
	if secondBlock["reason"] != "Configured reason." || secondBlock["block_name"] != "deny" {
		t.Fatalf("evaluated event lost the block's reason or name: %#v", secondBlock)
	}
	completed := auditData(t, input.AuditEvents[4])
	if completed["reason_code"] != "block_outcome" || completed["terminal_block_key"] != "deny" || completed["terminal_block_name"] != "deny" || completed["attempted_block_count"] != float64(2) {
		t.Fatalf("unexpected completion evidence: %#v", completed)
	}
	for _, event := range input.AuditEvents[1:5] {
		encoded := string(event.Data)
		if strings.Contains(encoded, "deploy") || strings.Contains(encoded, "version") {
			t.Fatalf("evaluation audit event leaked request values: %s", encoded)
		}
	}

	loadsAfterFirstRequest := pipelines.blockLoads
	second := executionRequest(now.Add(time.Second), "request-2", "key-2", "billing", json.RawMessage(`{"version":"2"}`))
	if _, err := service.Evaluate(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if pipelines.blockLoads != loadsAfterFirstRequest {
		t.Fatalf("compiled revision was loaded again: before=%d after=%d", loadsAfterFirstRequest, pipelines.blockLoads)
	}
}

func TestExecutionAutomaticallyApproves(t *testing.T) {
	now := time.Unix(1_500, 0).UTC()
	pipelines := activeExecutionPipeline(t, now, "billing", []approvalblock.Definition{
		pipelineDefinition("approve", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	})
	service := initializedExecutionService(t, pipelines, executionAgentRepositoryStub{agent: domain.Agent{
		ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing",
	}}, fixedPipelineClock{now: now.Add(time.Second)})

	initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if initialization.Request.Status != domain.ApprovalApproved || initialization.Decision == nil ||
		initialization.Decision.Source != domain.DecisionSourcePipeline || initialization.Decision.Auth.Assurance != "automatic" {
		t.Fatalf("pipeline approval was not prepared with automatic provenance: %#v", initialization)
	}
}

func TestExecutionFallsBackToHumanReview(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	tests := []struct {
		name        string
		definitions []approvalblock.Definition
		mutate      func(*memoryPipelineRepository)
		arguments   json.RawMessage
		wantReason  string
		wantEvents  int
	}{
		{
			name: "end of pipeline",
			definitions: []approvalblock.Definition{
				pipelineDefinition("does-not-match", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("other.tool")),
			},
			arguments: json.RawMessage(`{}`), wantReason: "end_of_pipeline", wantEvents: 4,
		},
		{
			name: "explicit human outcome",
			definitions: []approvalblock.Definition{
				pipelineDefinition("human", approvalblock.Human, approvalblock.Field("/request/tool").Equals("deploy")),
			},
			arguments: json.RawMessage(`{}`), wantReason: "block_outcome", wantEvents: 4,
		},
		{
			name: "runtime type error",
			definitions: []approvalblock.Definition{
				pipelineDefinition("number", approvalblock.Approve, approvalblock.Field("/request/arguments/amount").LessThan(100)),
			},
			arguments: json.RawMessage(`{"amount":"not-a-number"}`), wantReason: "block_error", wantEvents: 4,
		},
		{
			name: "compilation failure",
			definitions: []approvalblock.Definition{
				pipelineDefinition("corrupt", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
			},
			mutate: func(repository *memoryPipelineRepository) {
				revision := repository.revisions["billing"][1]
				block := repository.blocks[revision.BlockIDs[0]]
				block.Config = json.RawMessage(`{"unknown":true}`)
				repository.blocks[block.ID] = block
			},
			arguments: json.RawMessage(`{}`), wantReason: "compiled_cache_unavailable", wantEvents: 3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pipelines := activeExecutionPipeline(t, now, "billing", test.definitions)
			if test.mutate != nil {
				test.mutate(pipelines)
			}
			compiled := NewCompiledPipelineState(pipelines, NewBlockRegistry())
			initializeErr := compiled.Initialize(context.Background())
			if test.mutate == nil && initializeErr != nil {
				t.Fatal(initializeErr)
			}
			if test.mutate != nil && initializeErr == nil {
				t.Fatal("corrupt active revision unexpectedly passed startup compilation")
			}
			service := NewExecutionService(pipelines, executionAgentRepositoryStub{agent: domain.Agent{
				ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing",
			}}, compiled, fixedPipelineClock{now: now.Add(time.Second)})
			initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", test.arguments))
			if err != nil {
				t.Fatal(err)
			}
			if initialization.Request.Status != domain.ApprovalPending || initialization.Decision != nil {
				t.Fatalf("unsafe pipeline result did not require human review: %#v", initialization)
			}
			if len(initialization.AuditEvents) != test.wantEvents {
				t.Fatalf("got %d events, want %d", len(initialization.AuditEvents), test.wantEvents)
			}
			completed := auditData(t, initialization.AuditEvents[len(initialization.AuditEvents)-1])
			if completed["reason_code"] != test.wantReason || completed["outcome"] != "human" {
				t.Fatalf("unexpected safe fallback evidence: %#v", completed)
			}
		})
	}
}

func TestExecutionOrganizationSectionOutcomePreemptsAgentSection(t *testing.T) {
	now := time.Unix(4_000, 0).UTC()
	repository := activeExecutionPipeline(t, now, "", []approvalblock.Definition{
		pipelineDefinition("org-skip", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("other.tool")),
		pipelineDefinition("org-approve", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	})
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", []approvalblock.Definition{
		pipelineDefinition("deny", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("deploy")),
	}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	service := initializedExecutionService(t, repository, executionAgentRepositoryStub{agent: domain.Agent{
		ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing",
	}}, fixedPipelineClock{now: now.Add(time.Second)})

	initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if initialization.Request.Status != domain.ApprovalApproved || initialization.Decision == nil {
		t.Fatalf("organization approve did not preempt agent deny: %#v", initialization)
	}
	started := auditData(t, initialization.AuditEvents[1])
	if started["organization_revision"] != float64(2) || started["agent_revision"] != float64(2) || started["block_count"] != float64(3) {
		t.Fatalf("started event did not pin both sections: %#v", started)
	}
	completed := auditData(t, initialization.AuditEvents[4])
	if completed["terminal_block_key"] != "org-approve" || completed["terminal_block_scope"] != "organization" {
		t.Fatalf("unexpected terminal evidence: %#v", completed)
	}
}

func TestExecutionFallsThroughOrganizationSectionIntoAgentSection(t *testing.T) {
	now := time.Unix(4_500, 0).UTC()
	repository := activeExecutionPipeline(t, now, "", []approvalblock.Definition{
		pipelineDefinition("org-skip", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("other.tool")),
	})
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", []approvalblock.Definition{
		pipelineDefinition("deny", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("deploy")),
	}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	service := initializedExecutionService(t, repository, executionAgentRepositoryStub{agent: domain.Agent{
		ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing",
	}}, fixedPipelineClock{now: now.Add(time.Second)})

	initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if initialization.Request.Status != domain.ApprovalDenied {
		t.Fatalf("agent deny after organization fall-through was not prepared: %#v", initialization)
	}
	firstBlock := auditData(t, initialization.AuditEvents[2])
	secondBlock := auditData(t, initialization.AuditEvents[3])
	if firstBlock["scope"] != "organization" || firstBlock["position"] != float64(0) ||
		secondBlock["scope"] != "agent" || secondBlock["position"] != float64(1) {
		t.Fatalf("combined order was not organization first: first=%#v second=%#v", firstBlock, secondBlock)
	}
}

func TestExecutionOrganizationSectionDecidesForUnconfiguredAgent(t *testing.T) {
	now := time.Unix(5_000, 0).UTC()
	repository := activeExecutionPipeline(t, now, "", []approvalblock.Definition{
		pipelineDefinition("org-approve", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	})
	service := initializedExecutionService(t, repository, executionAgentRepositoryStub{agent: domain.Agent{
		ID: "agent-1", OrganizationID: "organization-1", Name: "support-agent", AgentSlug: "support",
	}}, fixedPipelineClock{now: now.Add(time.Second)})

	initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "support", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if initialization.Request.Status != domain.ApprovalApproved || initialization.Decision == nil {
		t.Fatalf("organization section did not decide for an unconfigured agent: %#v", initialization)
	}
	started := auditData(t, initialization.AuditEvents[1])
	if started["agent_pipeline_id"] != nil || started["organization_pipeline_id"] == nil {
		t.Fatalf("started event did not record the absent agent section: %#v", started)
	}
}

func TestExecutionWithoutActiveRevisionRequiresHumanReview(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	pipelines := newMemoryPipelineRepository()
	compiled := NewCompiledPipelineState(pipelines, NewBlockRegistry())
	if err := compiled.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	service := NewExecutionService(pipelines, executionAgentRepositoryStub{agent: domain.Agent{
		ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing",
	}}, compiled, fixedPipelineClock{now: now.Add(time.Second)})

	initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if initialization.Request.Status != domain.ApprovalPending || len(initialization.AuditEvents) != 2 {
		t.Fatalf("missing pipeline did not prepare human review: %#v", initialization)
	}
	completed := auditData(t, initialization.AuditEvents[1])
	if completed["reason_code"] != "no_active_revision" {
		t.Fatalf("unexpected completion evidence: %#v", completed)
	}
}

func TestCompiledPipelineCacheSharesConcurrentMiss(t *testing.T) {
	cache := newCompiledPipelineCache()
	key := compiledPipelineKey{RevisionID: "revision-1", RuntimeFingerprint: CELRuntimeFingerprint}
	var loads atomic.Int32
	loaderStarted := make(chan struct{})
	releaseLoader := make(chan struct{})
	loader := func() (*compiledPipeline, error) {
		if loads.Add(1) == 1 {
			close(loaderStarted)
		}
		<-releaseLoader
		return &compiledPipeline{}, nil
	}

	const callers = 8
	var wait sync.WaitGroup
	wait.Add(callers)
	errors := make(chan error, callers)
	for range callers {
		go func() {
			defer wait.Done()
			_, err := cache.load(context.Background(), key, loader)
			errors <- err
		}()
	}
	<-loaderStarted
	close(releaseLoader)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("concurrent cache miss compiled %d times, want 1", loads.Load())
	}
}

type executionAgentRepositoryStub struct{ agent domain.Agent }

func (repository executionAgentRepositoryStub) GetAgent(_ context.Context, input application.GetAgentInput) (domain.Agent, error) {
	if repository.agent.OrganizationID != input.OrganizationID || repository.agent.ID != input.AgentID {
		return domain.Agent{}, domain.ErrNotFound
	}
	return repository.agent, nil
}

func activeExecutionPipeline(t *testing.T, now time.Time, agentSlug domain.AgentSlug, definitions []approvalblock.Definition) *memoryPipelineRepository {
	t.Helper()
	repository := newMemoryPipelineRepository()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), agentSlug, definitions, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), agentSlug, created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	return repository
}

func initializedExecutionService(t *testing.T, pipelines *memoryPipelineRepository, agents executionAgentRepository, clock application.Clock) *ExecutionService {
	t.Helper()
	compiled := NewCompiledPipelineState(pipelines, NewBlockRegistry())
	if err := compiled.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewExecutionService(pipelines, agents, compiled, clock)
}

func executionRequest(now time.Time, id, idempotencyKey string, agentSlug domain.AgentSlug, arguments json.RawMessage) domain.ApprovalRequest {
	return domain.ApprovalRequest{
		ID: domain.ID(id), OrganizationID: "organization-1", AgentID: "agent-1", AgentInstanceID: "instance-1",
		AgentSlug: agentSlug, Tool: "deploy", Arguments: arguments, IdempotencyKey: idempotencyKey,
		SemanticHash: "semantic-1", Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}
}

func auditData(t *testing.T, event domain.AuditEvent) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(event.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data
}
