package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

func TestCompiledPipelineStateInitializesEveryActiveRevisionBeforeReady(t *testing.T) {
	repository := newMemoryPipelineRepository()
	createActivePipeline(t, repository, "billing", "billing-approve")
	createActivePipeline(t, repository, "release", "release-approve")
	counter := &countingCompiledPipelineRepository{repository: repository}
	state := NewCompiledPipelineState(counter, NewBlockRegistry())

	if state.Readiness() == nil {
		t.Fatal("new compiled state reported ready before startup compilation")
	}
	if err := state.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := state.Readiness(); err != nil {
		t.Fatalf("compiled state stayed unready after startup: %v", err)
	}
	if got := counter.blockLoads.Load(); got != 2 {
		t.Fatalf("startup loaded %d active revisions, want 2", got)
	}

	if err := state.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := counter.blockLoads.Load(); got != 2 {
		t.Fatalf("repeat startup recompiled immutable revisions: %d loads", got)
	}
}

func TestCompiledPipelineStateStartupFailureKeepsReadinessClosedAndCanRecover(t *testing.T) {
	repository := newMemoryPipelineRepository()
	createActivePipeline(t, repository, "billing", "approve")
	revision := repository.revisions["billing"][1]
	block := repository.blocks[revision.BlockIDs[0]]
	originalConfig := append(json.RawMessage(nil), block.Config...)
	block.Config = json.RawMessage(`{"unsupported":true}`)
	repository.blocks[block.ID] = block
	state := NewCompiledPipelineState(repository, NewBlockRegistry())

	if err := state.Initialize(context.Background()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("startup returned %v, want invalid configuration", err)
	}
	if state.Readiness() == nil {
		t.Fatal("failed startup compilation opened readiness")
	}

	block.Config = originalConfig
	repository.blocks[block.ID] = block
	if err := state.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := state.Readiness(); err != nil {
		t.Fatalf("successful retry did not open readiness: %v", err)
	}
}

func TestExecutionRequestPathSharesBoundedCompilationMiss(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	repository := newMemoryPipelineRepository()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", []approvalblock.Definition{
		pipelineDefinition("approve", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	counter := &countingCompiledPipelineRepository{
		repository: repository, loadStarted: loadStarted, releaseLoad: releaseLoad,
	}
	state := NewCompiledPipelineState(counter, NewBlockRegistry())
	if err := state.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateRequestApprovalPipelineRevision(context.Background(), application.ActivateRequestApprovalPipelineRevisionInput{
		OrganizationID: "organization-1", AgentSlug: "billing", Revision: created.Revision.Revision,
		ExpectedActiveRevision: 1, ActorID: "membership-1", Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	service := NewExecutionService(repository, executionAgentRepositoryStub{agent: domain.Agent{
		ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing",
	}}, state, fixedPipelineClock{now: now.Add(time.Second)})
	const callers = 12
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(callers)
	errorsChannel := make(chan error, callers)
	for index := range callers {
		go func() {
			defer wait.Done()
			<-start
			request := executionRequest(now, "request-"+string(rune('a'+index)), "key", "billing", json.RawMessage(`{}`))
			initialization, evaluateErr := service.Evaluate(context.Background(), request)
			if evaluateErr == nil && initialization.Request.Status != domain.ApprovalApproved {
				evaluateErr = errors.New("request did not use compiled approval pipeline")
			}
			errorsChannel <- evaluateErr
		}()
	}
	close(start)
	<-loadStarted
	close(releaseLoad)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := counter.blockLoads.Load(); got != 1 {
		t.Fatalf("concurrent request miss loaded blocks %d times, want 1", got)
	}
}

func TestCompiledPipelineStateBoundsCompilationAndFailsRequestToHuman(t *testing.T) {
	now := time.Unix(11_000, 0).UTC()
	repository := activeExecutionPipeline(t, now, "billing", []approvalblock.Definition{
		pipelineDefinition("approve", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	})
	state := NewCompiledPipelineState(repository, NewBlockRegistry())
	state.compilationTimeout = 5 * time.Millisecond
	compileStarted := make(chan struct{})
	releaseCompile := make(chan struct{})
	state.compilePipeline = func(domain.RequestApprovalPipelineRevision, []domain.RequestApprovalBlock) (*compiledPipeline, error) {
		close(compileStarted)
		<-releaseCompile
		return &compiledPipeline{}, nil
	}
	startedAt := time.Now()
	if err := state.Initialize(context.Background()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("startup timeout returned %v, want invalid", err)
	}
	<-compileStarted
	close(releaseCompile)
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("compilation was not bounded: %s", elapsed)
	}
	service := NewExecutionService(repository, executionAgentRepositoryStub{agent: domain.Agent{
		ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing",
	}}, state, fixedPipelineClock{now: now.Add(time.Second)})
	initialization, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if initialization.Request.Status != domain.ApprovalPending || initialization.Decision != nil {
		t.Fatalf("unavailable compiled state did not fail to human: %#v", initialization)
	}
	completed := auditData(t, initialization.AuditEvents[len(initialization.AuditEvents)-1])
	if completed["reason_code"] != "compiled_cache_unavailable" {
		t.Fatalf("unexpected safe failure evidence: %#v", completed)
	}
}

func TestManagementRefreshesActivationRollbackAndDeactivation(t *testing.T) {
	now := time.Unix(12_000, 0).UTC()
	repository := newMemoryPipelineRepository()
	counter := &countingCompiledPipelineRepository{repository: repository}
	state := NewCompiledPipelineState(counter, NewBlockRegistry())
	if err := state.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	management := NewManagementService(repository, state, fixedPipelineClock{now: now})
	principal := pipelineManagerPrincipal("organization-1")
	first, err := management.CreateRevision(context.Background(), principal, "billing", []approvalblock.Definition{
		pipelineDefinition("first", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := management.CreateRevision(context.Background(), principal, "billing", []approvalblock.Definition{
		pipelineDefinition("second", approvalblock.Human, approvalblock.Field("/request/tool").Equals("deploy")),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), principal, "billing", first.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), principal, "billing", second.Revision.Revision, first.Revision.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), principal, "billing", first.Revision.Revision, second.Revision.Revision); err != nil {
		t.Fatal(err)
	}
	if got := counter.blockLoads.Load(); got != 2 {
		t.Fatalf("activation and rollback loaded immutable targets %d times, want 2", got)
	}
}

func TestCompiledPipelineStateSeparatesRuntimeFingerprints(t *testing.T) {
	repository := newMemoryPipelineRepository()
	createActivePipeline(t, repository, "billing", "approve")
	counter := &countingCompiledPipelineRepository{repository: repository}
	state := NewCompiledPipelineState(counter, NewBlockRegistry())
	if err := state.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	revision, err := repository.GetActiveRequestApprovalPipelineRevision(context.Background(), application.GetActiveRequestApprovalPipelineRevisionInput{
		OrganizationID: "organization-1", AgentSlug: "billing",
	})
	if err != nil {
		t.Fatal(err)
	}
	nextState := NewCompiledPipelineState(counter, NewBlockRegistry())
	nextState.cache = state.cache
	nextState.runtimeFingerprint = CELRuntimeFingerprint + ";deployment-next"
	if nextState.Readiness() == nil {
		t.Fatal("new runtime reported ready before recompiling active revisions")
	}
	if err := nextState.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := nextState.Get(context.Background(), revision); err != nil {
		t.Fatal(err)
	}
	if got := counter.blockLoads.Load(); got != 2 {
		t.Fatalf("new runtime fingerprint reused old compiled program: %d loads", got)
	}
}

func TestCompiledPipelineStateRejectsTenantOrRevisionIdentityMismatch(t *testing.T) {
	repository := newMemoryPipelineRepository()
	createActivePipeline(t, repository, "billing", "approve")
	state := NewCompiledPipelineState(repository, NewBlockRegistry())
	if err := state.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	revision, err := repository.GetActiveRequestApprovalPipelineRevision(context.Background(), application.GetActiveRequestApprovalPipelineRevisionInput{
		OrganizationID: "organization-1", AgentSlug: "billing",
	})
	if err != nil {
		t.Fatal(err)
	}

	wrongTenant := revision
	wrongTenant.OrganizationID = "organization-2"
	if _, err := state.Get(context.Background(), wrongTenant); !errors.Is(err, errCompiledPipelineCacheUnavailable) {
		t.Fatalf("cross-tenant cache identity returned %v, want unavailable", err)
	}

	if err := state.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	wrongRevision := revision
	wrongRevision.Revision++
	if _, err := state.Get(context.Background(), wrongRevision); !errors.Is(err, errCompiledPipelineCacheUnavailable) {
		t.Fatalf("mutable revision identity returned %v, want unavailable", err)
	}
}

type countingCompiledPipelineRepository struct {
	repository  *memoryPipelineRepository
	blockLoads  atomic.Int32
	loadOnce    sync.Once
	loadStarted chan struct{}
	releaseLoad chan struct{}
}

func (repository *countingCompiledPipelineRepository) ListActiveRequestApprovalPipelineRevisions(ctx context.Context, input application.ListActiveRequestApprovalPipelineRevisionsInput) ([]domain.RequestApprovalPipelineRevision, error) {
	return repository.repository.ListActiveRequestApprovalPipelineRevisions(ctx, input)
}

// GetRequestApprovalBlocks counts and gates loads of authored revisions.
// The empty revision every scope is born with has nothing to load, so it
// is neither counted nor held at the gate.
func (repository *countingCompiledPipelineRepository) GetRequestApprovalBlocks(ctx context.Context, input application.GetRequestApprovalBlocksInput) ([]domain.RequestApprovalBlock, error) {
	if len(input.BlockIDs) == 0 {
		return repository.repository.GetRequestApprovalBlocks(ctx, input)
	}
	repository.blockLoads.Add(1)
	if repository.loadStarted != nil {
		repository.loadOnce.Do(func() { close(repository.loadStarted) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-repository.releaseLoad:
		}
	}
	return repository.repository.GetRequestApprovalBlocks(ctx, input)
}

func createActivePipeline(t *testing.T, repository *memoryPipelineRepository, agentSlug domain.AgentSlug, blockKey string) {
	t.Helper()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(9_000, 0).UTC()})
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), agentSlug, []approvalblock.Definition{
		pipelineDefinition(blockKey, approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), agentSlug, created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
}
