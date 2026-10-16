package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/telemetry"
)

const (
	pipelineStartedEvent   = "request_approval_pipeline.started"
	blockEvaluatedEvent    = "request_approval_block.evaluated"
	pipelineSuspendedEvent = "request_approval_pipeline.suspended"
	pipelineCompletedEvent = "request_approval_pipeline.completed"
)

// ExecutionService selects and executes the effective pipeline: the entry
// pipeline's active revision with every delegated pipeline's active
// revision spliced in. On the synchronous request path it runs blocks until
// the first asynchronous one and suspends there; a worker resumes the
// suspended pipeline on the pinned revisions and runs every remaining block
// inline. CompiledState is shared with startup readiness and the management
// control plane.
type ExecutionService struct {
	pipelines executionPipelineRepository
	agents    executionAgentRepository
	clock     application.Clock
	compiled  *CompiledPipelineState
}

type executionPipelineRepository interface {
	GetActiveRequestApprovalPipelineRevision(context.Context, application.GetActiveRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error)
	GetRequestApprovalPipelineRevisionByID(context.Context, application.GetRequestApprovalPipelineRevisionByIDInput) (domain.RequestApprovalPipelineRevision, error)
}

type executionAgentRepository interface {
	GetAgent(context.Context, application.GetAgentInput) (domain.Agent, error)
}

func NewExecutionService(pipelines executionPipelineRepository, agents executionAgentRepository, compiled *CompiledPipelineState, clock application.Clock) *ExecutionService {
	return &ExecutionService{
		pipelines: pipelines,
		agents:    agents,
		clock:     clock,
		compiled:  compiled,
	}
}

// terminalBlock identifies the block whose outcome ended execution and
// where it sits in the effective pipeline.
type terminalBlock struct {
	ID          domain.ID
	Key         string
	Name        string
	PipelineKey string
	Position    int
}

// AbandonReasonCode is the completion reason written when the asynchronous
// segment could not run at all.
const AbandonReasonCode = "block_error"

// Evaluate prepares the complete atomic initialization write without
// performing persistence or starting durable orchestration. The effective
// pipeline is the entry pipeline's blocks with every delegate's target
// spliced in; the first terminal outcome wins, and running out of the
// entry pipeline requires a human. Reaching an asynchronous block suspends
// the pipeline: the request is committed as pending with a suspension the
// worker resumes.
func (service *ExecutionService) Evaluate(ctx context.Context, request domain.ApprovalRequest) (application.InitializeApprovalInput, error) {
	started := time.Now()
	result, err := service.evaluate(ctx, request)
	outcome := pipelineOutcome(result.Decision)
	if result.Suspension != nil {
		outcome = pipelineSuspended
	}
	telemetry.PipelineEvaluated(ctx, pipelineStageRequest, outcome, pipelineErrorType(err), time.Since(started))
	return result, err
}

func (service *ExecutionService) evaluate(ctx context.Context, request domain.ApprovalRequest) (application.InitializeApprovalInput, error) {
	if err := (application.StartApprovalInput{Request: request}).Validate(); err != nil {
		return application.InitializeApprovalInput{}, err
	}
	agent, err := service.loadAgent(ctx, request)
	if err != nil {
		return application.InitializeApprovalInput{}, err
	}
	request.AgentName = agent.Name

	evaluationID := domain.ID(uuid.NewString())
	events, err := approvalCreatedEvents(request)
	if err != nil {
		return application.InitializeApprovalInput{}, err
	}
	startedAt := service.clock.Now()
	entry, effective, err := service.resolveEffective(ctx, request.OrganizationID, nil)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		// A defensive path the product never reaches: every organization
		// is seeded with an entry pipeline.
		completed, marshalErr := pipelineCompletedAuditEvent(request, evaluationID, nil, service.compiled.Fingerprint(), BlockHuman, "no_active_revision", terminalBlock{}, 0, 0, 0, startedAt, service.clock.Now())
		if marshalErr != nil {
			return application.InitializeApprovalInput{}, marshalErr
		}
		events = append(events, completed)
		result := initializationInput(request, nil, events)
		result.HumanReview = &application.HumanReviewRoute{Reason: domain.RoutingReasonNoEscalation}
		return result, nil
	case errors.Is(err, errCompiledPipelineCacheUnavailable):
		// Never execute a partial effective pipeline: a target that cannot
		// be resolved or compiled hands the whole request to a human.
		pins := []pinnedPipeline{{Key: entry.PipelineKey, ID: entry.ID, Revision: entry.Revision, Name: entry.Name}}
		started, marshalErr := pipelineStartedAuditEvent(request, evaluationID, pins, len(entry.BlockIDs), service.compiled.Fingerprint(), startedAt)
		if marshalErr != nil {
			return application.InitializeApprovalInput{}, marshalErr
		}
		completed, marshalErr := pipelineCompletedAuditEvent(request, evaluationID, pins, service.compiled.Fingerprint(), BlockHuman, "compiled_cache_unavailable", terminalBlock{}, 0, 0, 0, startedAt, service.clock.Now())
		if marshalErr != nil {
			return application.InitializeApprovalInput{}, marshalErr
		}
		events = append(events, started, completed)
		result := initializationInput(request, nil, events)
		result.HumanReview = entryDefaultRoute(entry)
		return result, nil
	case err != nil:
		return application.InitializeApprovalInput{}, err
	}

	started, err := pipelineStartedAuditEvent(request, evaluationID, effective.pins(), len(effective.Blocks), service.compiled.Fingerprint(), startedAt)
	if err != nil {
		return application.InitializeApprovalInput{}, err
	}
	events = append(events, started)
	run := &pipelineRun{
		request: request, evaluationID: evaluationID, effective: effective, startedAt: startedAt,
		input: newBlockInput(request, agent, nil),
	}
	run.input.EvaluationID = evaluationID
	suspended, err := service.runBlocks(ctx, run, 0, true)
	if err != nil {
		return application.InitializeApprovalInput{}, err
	}
	events = append(events, run.events...)
	if suspended != nil {
		suspendedEvent, err := pipelineSuspendedAuditEvent(request, run, *suspended, service.clock.Now())
		if err != nil {
			return application.InitializeApprovalInput{}, err
		}
		events = append(events, suspendedEvent)
		suspension := run.suspension(suspended.position, service.clock.Now())
		result := initializationInput(request, nil, events)
		result.Suspension = &suspension
		return result, nil
	}
	completed, decision, decided, err := service.completion(run)
	if err != nil {
		return application.InitializeApprovalInput{}, err
	}
	events = append(events, completed)
	if decision != nil {
		request.Status = decision.Status
		request.UpdatedAt = decision.DecidedAt
		events = append(events, decided)
	}
	result := initializationInput(request, decision, events)
	if decision == nil {
		result.HumanReview = humanReviewRoute(run.effective, run.terminal, run.route, run.routeFrom)
	}
	return result, nil
}

// Resume runs the remainder of a suspended pipeline, every block inline, and
// prepares the resume write. It never persists. The caller attaches routing
// when the outcome still needs a human.
func (service *ExecutionService) Resume(ctx context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension) (application.ResumeApprovalInput, error) {
	started := time.Now()
	result, err := service.resume(ctx, request, suspension)
	telemetry.PipelineEvaluated(ctx, pipelineStageResume, pipelineOutcome(result.Decision), pipelineErrorType(err), time.Since(started))
	return result, err
}

// The pipeline stages and outcomes the evaluation metric reports beside
// the decision statuses.
const (
	pipelineStageRequest = "request"
	pipelineStageResume  = "resume"
	pipelineHuman        = "human"
	pipelineSuspended    = "suspended"
)

// pipelineOutcome is the decision's status, or human when the run left
// the request for a person.
func pipelineOutcome(decision *domain.Decision) string {
	if decision == nil {
		return pipelineHuman
	}
	return string(decision.Status)
}

func pipelineErrorType(err error) string {
	if err == nil {
		return ""
	}
	return "_OTHER"
}

func (service *ExecutionService) resume(ctx context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension) (application.ResumeApprovalInput, error) {
	if request.ID != suspension.RequestID || request.OrganizationID != suspension.OrganizationID {
		return application.ResumeApprovalInput{}, fmt.Errorf("suspension does not belong to the request: %w", domain.ErrInvalid)
	}
	agent, err := service.loadAgent(ctx, request)
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	_, effective, err := service.resolveEffective(ctx, suspension.OrganizationID, suspension.PinnedRevisions)
	if errors.Is(err, errCompiledPipelineCacheUnavailable) {
		return service.Abandon(ctx, request, suspension, "compiled_cache_unavailable")
	}
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	run := &pipelineRun{
		request: request, evaluationID: suspension.EvaluationID, effective: effective, startedAt: suspension.StartedAt,
		input:           newBlockInput(request, agent, suspension.PreviousMetadata),
		attemptedBlocks: suspension.AttemptedBlockCount, totalAttempts: suspension.TotalAttempts, totalCost: suspension.TotalCost,
	}
	run.input.EvaluationID = suspension.EvaluationID
	if _, err := service.runBlocks(ctx, run, suspension.ResumePosition, false); err != nil {
		return application.ResumeApprovalInput{}, err
	}
	completed, decision, decided, err := service.completion(run)
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	events := append(run.events, completed)
	if decision != nil {
		events = append(events, decided)
	}
	result := application.ResumeApprovalInput{
		OrganizationID: request.OrganizationID, RequestID: request.ID, EvaluationID: suspension.EvaluationID,
		Decision: decision, AuditEvents: events, Now: service.clock.Now(),
	}
	if decision == nil {
		result.HumanReview = humanReviewRoute(run.effective, run.terminal, run.route, run.routeFrom)
	}
	return result, nil
}

// Abandon prepares the resume write for a suspended pipeline whose
// asynchronous segment could not run: the pipeline completes with the
// reason code and the request goes to a human on the entry pipeline's
// default, the only default known without flattening the run.
func (service *ExecutionService) Abandon(ctx context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension, reasonCode string) (application.ResumeApprovalInput, error) {
	entry, pins, err := service.pinnedPipelines(ctx, suspension)
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	completed, err := pipelineCompletedAuditEvent(request, suspension.EvaluationID, pins, service.compiled.Fingerprint(), BlockHuman, reasonCode, terminalBlock{},
		suspension.AttemptedBlockCount, suspension.TotalAttempts, suspension.TotalCost, suspension.StartedAt, service.clock.Now())
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	return application.ResumeApprovalInput{
		OrganizationID: request.OrganizationID, RequestID: request.ID, EvaluationID: suspension.EvaluationID,
		AuditEvents: []domain.AuditEvent{completed}, Now: service.clock.Now(),
		HumanReview: entryDefaultRoute(entry),
	}, nil
}

func (service *ExecutionService) loadAgent(ctx context.Context, request domain.ApprovalRequest) (domain.Agent, error) {
	agent, err := service.agents.GetAgent(ctx, application.GetAgentInput{
		OrganizationID: request.OrganizationID,
		AgentID:        request.AgentID,
	})
	if err != nil {
		return domain.Agent{}, err
	}
	if agent.AgentSlug != request.AgentSlug {
		return domain.Agent{}, fmt.Errorf("authenticated agent slug changed before approval initialization: %w", domain.ErrInvalid)
	}
	return agent, nil
}

// resolveEffective loads the entry revision and then the closure of every
// delegate target, flattened into one effective pipeline. With pinned nil
// it selects each key's active revision; otherwise every key must be
// pinned and each pinned revision must belong to its key. The entry
// revision comes back even when the closure fails, so the caller can
// route by its default. A missing entry is domain.ErrNotFound when
// selecting actives and domain.ErrInvalid for a pin; a target that is
// missing, archived, uncompilable, cyclic, too deep, or too large is
// errCompiledPipelineCacheUnavailable, so a run never executes a partial
// effective pipeline.
func (service *ExecutionService) resolveEffective(ctx context.Context, organizationID domain.ID, pinned map[string]domain.ID) (domain.RequestApprovalPipelineRevision, *effectivePipeline, error) {
	load := func(ctx context.Context, key string) (domain.RequestApprovalPipelineRevision, error) {
		if pinned == nil {
			return service.pipelines.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
				OrganizationID: organizationID, PipelineKey: key,
			})
		}
		id, ok := pinned[key]
		if !ok {
			return domain.RequestApprovalPipelineRevision{}, fmt.Errorf("suspension pins no revision of pipeline %q: %w", key, domain.ErrNotFound)
		}
		return service.pipelines.GetRequestApprovalPipelineRevisionByID(ctx, application.GetRequestApprovalPipelineRevisionByIDInput{
			OrganizationID: organizationID, RevisionID: id,
		})
	}
	entry, err := load(ctx, domain.EntryPipelineKey)
	if err != nil {
		if pinned != nil && errors.Is(err, domain.ErrNotFound) {
			return domain.RequestApprovalPipelineRevision{}, nil, fmt.Errorf("suspension pins no entry pipeline revision: %w", domain.ErrInvalid)
		}
		return domain.RequestApprovalPipelineRevision{}, nil, err
	}
	root, err := service.compiled.Get(ctx, entry)
	if err != nil {
		return entry, nil, err
	}
	effective, err := service.compiled.Effective(ctx, root, load)
	if err != nil {
		if ctx.Err() != nil {
			return entry, nil, ctx.Err()
		}
		var tooDeep domain.PipelineDelegationTooDeepError
		var cycle domain.PipelineDelegationCycleError
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, errCompiledPipelineCacheUnavailable) ||
			errors.Is(err, domain.ErrEffectivePipelineTooLarge) || errors.As(err, &tooDeep) || errors.As(err, &cycle) || errors.Is(err, domain.ErrInvalid) {
			return entry, nil, fmt.Errorf("%v: %w", err, errCompiledPipelineCacheUnavailable)
		}
		return entry, nil, err
	}
	return entry, effective, nil
}

// pinnedPipelines reloads the revisions a suspension pinned, the entry
// first, for the record of a run that could not be flattened.
func (service *ExecutionService) pinnedPipelines(ctx context.Context, suspension domain.PipelineSuspension) (domain.RequestApprovalPipelineRevision, []pinnedPipeline, error) {
	if suspension.EntryPipelineID() == "" {
		return domain.RequestApprovalPipelineRevision{}, nil, fmt.Errorf("suspension pins no entry pipeline revision: %w", domain.ErrInvalid)
	}
	keys := make([]string, 0, len(suspension.PinnedRevisions))
	for key := range suspension.PinnedRevisions {
		if key != domain.EntryPipelineKey {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	keys = append([]string{domain.EntryPipelineKey}, keys...)
	var entry domain.RequestApprovalPipelineRevision
	pins := make([]pinnedPipeline, 0, len(keys))
	for _, key := range keys {
		revision, err := service.pipelines.GetRequestApprovalPipelineRevisionByID(ctx, application.GetRequestApprovalPipelineRevisionByIDInput{
			OrganizationID: suspension.OrganizationID, RevisionID: suspension.PinnedRevisions[key],
		})
		if err != nil {
			return domain.RequestApprovalPipelineRevision{}, nil, err
		}
		if key == domain.EntryPipelineKey {
			entry = revision
		}
		pins = append(pins, pinnedPipeline{Key: key, ID: revision.ID, Revision: revision.Revision, Name: revision.Name})
	}
	return entry, pins, nil
}

func newBlockInput(request domain.ApprovalRequest, agent domain.Agent, previous map[string]json.RawMessage) RequestApprovalBlockInput {
	metadata := make(map[string]json.RawMessage, len(previous))
	for key, value := range previous {
		metadata[key] = value
	}
	return RequestApprovalBlockInput{
		OrganizationID: request.OrganizationID,
		Request: BlockRequest{
			ID: request.ID, Tool: request.Tool, Server: request.Server, Arguments: request.Arguments, AgentReasoning: request.AgentReasoning, Context: request.Context,
			CreatedAt: request.CreatedAt, DeadlineAt: request.DeadlineAt,
		},
		Agent:            BlockAgent{Slug: string(request.AgentSlug), Name: agent.Name},
		PreviousMetadata: metadata,
		Budget:           &BlockExecutionBudget{},
	}
}

// pipelineRun accumulates one segment of an evaluation: the events it
// produced, the counters the completion event reports, and where it ended.
type pipelineRun struct {
	request      domain.ApprovalRequest
	evaluationID domain.ID
	effective    *effectivePipeline
	startedAt    time.Time
	input        RequestApprovalBlockInput

	events     []domain.AuditEvent
	outcome    BlockOutcome
	reasonCode string
	terminal   terminalBlock
	route      Route
	// routeFrom indexes the section whose default, then the defaults of
	// the pipelines that delegated into it, a human outcome without a
	// path of its own inherits.
	routeFrom       int
	attemptedBlocks int
	totalAttempts   int
	totalCost       uint64
}

// suspensionPoint is the block execution stopped in front of.
type suspensionPoint struct {
	position    int
	block       domain.RequestApprovalBlock
	pipelineKey string
}

// runBlocks executes the effective pipeline in pre-order starting at
// startPosition. A branch or delegate whose condition matches is entered
// and one that does not is skipped whole; a branch that runs out of blocks
// ends the run with a human, and so does a delegated pipeline that runs
// out. With suspendAsync, execution stops in front of the first
// asynchronous block it reaches and reports it; otherwise every block runs
// inline.
func (service *ExecutionService) runBlocks(ctx context.Context, run *pipelineRun, startPosition int, suspendAsync bool) (*suspensionPoint, error) {
	run.outcome = BlockHuman
	run.reasonCode = "end_of_pipeline"
	run.routeFrom = 0
	effective := run.effective
	cursor := newBranchCursor(effective.Blocks, startPosition)
	for {
		step, index := cursor.next()
		if step == stepDone {
			return nil, nil
		}
		block, snapshot := effective.Blocks[index], effective.Snapshots[index]
		if step == stepBranchEnd {
			if block.Delegate {
				return nil, service.endDelegate(run, snapshot, index)
			}
			return nil, service.endBranch(run, snapshot, block, index)
		}
		run.input.PipelineKey = effective.keyAt(index)
		if block.container() {
			started := time.Now()
			evaluated := executeBranchCondition(ctx, block.Executor, run.input)
			telemetry.PipelineBlockEvaluated(ctx, snapshot.BlockType, string(evaluated.Outcome), string(evaluated.ErrorCode), time.Since(started))
			if err := service.record(run, snapshot, index, evaluated); err != nil {
				return nil, err
			}
			switch evaluated.Outcome {
			case BlockEnter:
				cursor.enter()
			case BlockNext:
				cursor.skip()
			default:
				// A condition that cannot be evaluated hands the request
				// to a person on the default of the pipeline holding the
				// block: a branch's own path is for its end only, and a
				// delegate's target is never entered.
				run.finish(snapshot, index, evaluated, Route{})
				return nil, nil
			}
			continue
		}
		if suspendAsync && block.Asynchronous {
			return &suspensionPoint{position: index, block: snapshot, pipelineKey: effective.keyAt(index)}, nil
		}
		started := time.Now()
		evaluated := ExecuteRequestApprovalBlock(ctx, block.Executor, run.input)
		telemetry.PipelineBlockEvaluated(ctx, snapshot.BlockType, string(evaluated.Outcome), string(evaluated.ErrorCode), time.Since(started))
		if err := service.record(run, snapshot, index, evaluated); err != nil {
			return nil, err
		}
		if evaluated.Outcome == BlockNext {
			if len(evaluated.Metadata) > 0 {
				metadata, err := json.Marshal(evaluated.Metadata)
				if err != nil {
					return nil, err
				}
				run.input.PreviousMetadata[effective.keyAt(index)+"/"+snapshot.BlockKey] = metadata
			}
			cursor.advance()
			continue
		}
		run.finish(snapshot, index, evaluated, block.route())
		return nil, nil
	}
}

// record counts one attempted block and appends its evaluated event.
func (service *ExecutionService) record(run *pipelineRun, snapshot domain.RequestApprovalBlock, position int, evaluated RequestApprovalBlockResult) error {
	run.attemptedBlocks++
	run.totalAttempts += attemptCount(evaluated)
	run.totalCost += evaluated.Evidence.Cost
	event, err := blockEvaluatedAuditEvent(run.request, run.evaluationID, run.effective, snapshot, position, evaluated, service.clock.Now())
	if err != nil {
		return err
	}
	run.events = append(run.events, event)
	return nil
}

// endBranch finishes a run that an entered branch could not decide: the
// request goes to a human with the branch's reason and escalation path. The
// closing event is evidence rather than an attempt, so the counters the
// completion reports are unchanged.
func (service *ExecutionService) endBranch(run *pipelineRun, snapshot domain.RequestApprovalBlock, block compiledBlock, position int) error {
	event, err := branchEndedAuditEvent(run.request, run.evaluationID, run.effective, snapshot, position, block.Reason, service.clock.Now())
	if err != nil {
		return err
	}
	run.events = append(run.events, event)
	run.outcome = BlockHuman
	run.reasonCode = "end_of_branch"
	run.terminal = run.effective.terminalAt(snapshot, position)
	run.route = block.route()
	run.routeFrom = run.effective.Owner[position]
	return nil
}

// endDelegate finishes a run that a delegated pipeline could not decide:
// the request goes to a human on the target pipeline's default, else the
// defaults of the pipelines that delegated into it. The delegate block is
// the terminal block and its closing event carries no reason, because the
// target owns its end.
func (service *ExecutionService) endDelegate(run *pipelineRun, snapshot domain.RequestApprovalBlock, position int) error {
	event, err := branchEndedAuditEvent(run.request, run.evaluationID, run.effective, snapshot, position, "", service.clock.Now())
	if err != nil {
		return err
	}
	run.events = append(run.events, event)
	run.outcome = BlockHuman
	run.reasonCode = "end_of_branch"
	run.terminal = run.effective.terminalAt(snapshot, position)
	run.route = Route{}
	run.routeFrom = run.effective.target(position)
	return nil
}

// finish records the block whose outcome ended the run.
func (run *pipelineRun) finish(snapshot domain.RequestApprovalBlock, position int, evaluated RequestApprovalBlockResult, route Route) {
	run.outcome = evaluated.Outcome
	run.reasonCode = completionReason(evaluated)
	run.terminal = run.effective.terminalAt(snapshot, position)
	run.route = route
	run.routeFrom = run.effective.Owner[position]
}

func (effective *effectivePipeline) terminalAt(snapshot domain.RequestApprovalBlock, position int) terminalBlock {
	return terminalBlock{ID: snapshot.ID, Key: snapshot.BlockKey, Name: snapshot.Name, PipelineKey: effective.keyAt(position), Position: position}
}

func (run *pipelineRun) suspension(position int, now time.Time) domain.PipelineSuspension {
	return domain.PipelineSuspension{
		RequestID: run.request.ID, OrganizationID: run.request.OrganizationID, EvaluationID: run.evaluationID,
		PinnedRevisions: run.effective.pinned(),
		ResumePosition:  position, PreviousMetadata: run.input.PreviousMetadata,
		AttemptedBlockCount: run.attemptedBlocks, TotalAttempts: run.totalAttempts, TotalCost: run.totalCost,
		StartedAt: run.startedAt, SuspendedAt: now,
	}
}

// completion builds the completed event and, for an automatic outcome, the
// decision and its audit event.
func (service *ExecutionService) completion(run *pipelineRun) (domain.AuditEvent, *domain.Decision, domain.AuditEvent, error) {
	completedAt := service.clock.Now()
	completed, err := pipelineCompletedAuditEvent(run.request, run.evaluationID, run.effective.pins(), service.compiled.Fingerprint(), run.outcome, run.reasonCode, run.terminal,
		run.attemptedBlocks, run.totalAttempts, run.totalCost, run.startedAt, completedAt)
	if err != nil {
		return domain.AuditEvent{}, nil, domain.AuditEvent{}, err
	}
	decision := pipelineDecision(run.request, run.evaluationID, run.outcome, completedAt)
	if decision == nil {
		return completed, nil, domain.AuditEvent{}, nil
	}
	decided, err := approvalDecidedAuditEvent(run.request, run.evaluationID, decision, completedAt)
	if err != nil {
		return domain.AuditEvent{}, nil, domain.AuditEvent{}, err
	}
	return completed, decision, decided, nil
}

func initializationInput(request domain.ApprovalRequest, decision *domain.Decision, events []domain.AuditEvent) application.InitializeApprovalInput {
	return application.InitializeApprovalInput{
		Request:     request,
		Decision:    decision,
		AuditEvents: events,
	}
}

// humanReviewRoute resolves where a request that needs a human goes: the
// escalation path the terminal block named, else the default of the
// pipeline the request was in when it needed a human, then the defaults
// of the pipelines that delegated into it, nearest first, else none, which
// leaves the request to any authorized reviewer.
func humanReviewRoute(effective *effectivePipeline, terminal terminalBlock, route Route, routeFrom int) *application.HumanReviewRoute {
	resolved, _ := routeSection(effective, terminal, route, routeFrom)
	return resolved
}

// routeSection is humanReviewRoute plus the index of the section that
// supplied the path, -1 for none.
// A route that asks the agent's owner without a path of its own falls back
// to the path the defaults would have given it, their own owner flags
// aside because the owner is already the first choice.
func routeSection(effective *effectivePipeline, terminal terminalBlock, route Route, routeFrom int) (*application.HumanReviewRoute, int) {
	if route.EscalationPath != "" || route.AskAgentOwner {
		owner := effective.Owner[terminal.Position]
		path := route.EscalationPath
		if path == "" {
			path = defaultPath(effective, routeFrom)
		}
		return &application.HumanReviewRoute{
			PathKey:            path,
			AskAgentOwner:      route.AskAgentOwner,
			Reason:             domain.RoutingReasonBlockEscalation,
			PipelineRevisionID: effective.Sections[owner].Revision.ID,
			BlockID:            terminal.ID,
		}, owner
	}
	for index := routeFrom; index >= 0; index = effective.Sections[index].Enclosing {
		revision := effective.Sections[index].Revision
		if revision.DefaultEscalationPath == "" && !revision.DefaultAskAgentOwner {
			continue
		}
		path := revision.DefaultEscalationPath
		if path == "" {
			path = defaultPath(effective, effective.Sections[index].Enclosing)
		}
		return &application.HumanReviewRoute{
			PathKey: path, AskAgentOwner: revision.DefaultAskAgentOwner,
			Reason:             domain.RoutingReasonPipelineDefault,
			PipelineRevisionID: revision.ID,
		}, index
	}
	return &application.HumanReviewRoute{Reason: domain.RoutingReasonNoEscalation}, -1
}

// defaultPath is the first default path from the section at from outward,
// "" when none names one.
func defaultPath(effective *effectivePipeline, from int) string {
	for index := from; index >= 0; index = effective.Sections[index].Enclosing {
		if path := effective.Sections[index].Revision.DefaultEscalationPath; path != "" {
			return path
		}
	}
	return ""
}

// entryDefaultRoute is the entry pipeline's default alone: what a request
// inherits when the run never entered another pipeline, or could not be
// flattened at all.
func entryDefaultRoute(entry domain.RequestApprovalPipelineRevision) *application.HumanReviewRoute {
	if entry.DefaultEscalationPath == "" && !entry.DefaultAskAgentOwner {
		return &application.HumanReviewRoute{Reason: domain.RoutingReasonNoEscalation}
	}
	return &application.HumanReviewRoute{
		PathKey: entry.DefaultEscalationPath, AskAgentOwner: entry.DefaultAskAgentOwner,
		Reason:             domain.RoutingReasonPipelineDefault,
		PipelineRevisionID: entry.ID,
	}
}

func approvalCreatedEvents(request domain.ApprovalRequest) ([]domain.AuditEvent, error) {
	data, err := json.Marshal(map[string]any{
		"agent_instance_id": request.AgentInstanceID,
		"tool":              request.Tool,
		"deadline_at":       request.DeadlineAt,
	})
	if err != nil {
		return nil, err
	}
	return []domain.AuditEvent{{
		ID: domain.ID(uuid.NewString()), OrganizationID: request.OrganizationID,
		EventType: "approval.created", ActorType: domain.ActorAgent, ActorID: request.AgentID,
		SubjectType: "approval_request", SubjectID: request.ID, Data: data, OccurredAt: request.CreatedAt,
	}}, nil
}

// pipelinesAuditData records the run's pipelines: the entry revision and
// every pipeline a delegate names, whether or not the request entered it.
func pipelinesAuditData(data map[string]any, pins []pinnedPipeline) {
	pipelines := make([]map[string]any, 0, len(pins))
	for _, pin := range pins {
		if pin.Key == domain.EntryPipelineKey {
			data["entry_pipeline_id"] = pin.ID
			data["entry_revision"] = pin.Revision
		}
		pipelines = append(pipelines, map[string]any{
			"pipeline_key": pin.Key, "pipeline_id": pin.ID, "revision": pin.Revision, "name": pin.Name,
		})
	}
	data["pipelines"] = pipelines
}

func pipelineStartedAuditEvent(request domain.ApprovalRequest, evaluationID domain.ID, pins []pinnedPipeline, blockCount int, runtimeFingerprint string, occurredAt time.Time) (domain.AuditEvent, error) {
	data := map[string]any{
		"pipeline_evaluation_id": evaluationID,
		"agent_slug":             request.AgentSlug,
		"block_count":            blockCount,
		"runtime_fingerprint":    runtimeFingerprint,
	}
	pipelinesAuditData(data, pins)
	encoded, err := json.Marshal(data)
	return systemApprovalAuditEvent(request, pipelineStartedEvent, encoded, occurredAt), err
}

func blockEvaluatedAuditEvent(request domain.ApprovalRequest, evaluationID domain.ID, effective *effectivePipeline, block domain.RequestApprovalBlock, position int, result RequestApprovalBlockResult, occurredAt time.Time) (domain.AuditEvent, error) {
	fields := blockEvaluatedFields(evaluationID, effective, block, position, result.Outcome, result.Reason)
	fields["attempt_count"] = attemptCount(result)
	fields["cost"] = result.Evidence.Cost
	fields["error_code"] = result.ErrorCode
	fields["metadata"] = result.Metadata
	fields["trace"] = result.Evidence.Trace
	if result.Evidence.Delivery != nil {
		fields["delivery"] = result.Evidence.Delivery
	}
	data, err := json.Marshal(fields)
	return systemApprovalAuditEvent(request, blockEvaluatedEvent, data, occurredAt), err
}

// branchEndedAuditEvent is the closing evaluated event of a branch, or a
// delegate whose target, a request entered and ran out of: a human outcome
// with the branch's reason (none for a delegate), made without evaluating
// anything.
func branchEndedAuditEvent(request domain.ApprovalRequest, evaluationID domain.ID, effective *effectivePipeline, block domain.RequestApprovalBlock, position int, reason string, occurredAt time.Time) (domain.AuditEvent, error) {
	fields := blockEvaluatedFields(evaluationID, effective, block, position, BlockHuman, reason)
	fields["attempt_count"] = 0
	fields["cost"] = 0
	fields["error_code"] = ""
	fields["metadata"] = nil
	fields["trace"] = []NodeTrace{}
	fields["end_of_branch"] = true
	data, err := json.Marshal(fields)
	return systemApprovalAuditEvent(request, blockEvaluatedEvent, data, occurredAt), err
}

func blockEvaluatedFields(evaluationID domain.ID, effective *effectivePipeline, block domain.RequestApprovalBlock, position int, outcome BlockOutcome, reason string) map[string]any {
	section := effective.Sections[effective.Owner[position]]
	fields := map[string]any{
		"pipeline_evaluation_id": evaluationID,
		"pipeline_key":           section.Key,
		"pipeline_id":            section.Revision.ID,
		"revision":               section.Revision.Revision,
		"block_id":               block.ID,
		"block_key":              block.BlockKey,
		"block_name":             block.Name,
		"block_type":             block.BlockType,
		"position":               position,
		"outcome":                outcome,
		// The block's configured reason: the sentence a pipeline author
		// wrote for the reviewer, recorded so the review context can
		// say why a human was asked without reading the pipeline.
		"reason": reason,
	}
	if target := effective.target(position); target >= 0 {
		fields["target_pipeline_key"] = effective.Sections[target].Key
		fields["target_pipeline_id"] = effective.Sections[target].Revision.ID
		fields["target_revision"] = effective.Sections[target].Revision.Revision
	}
	return fields
}

// pipelineSuspendedAuditEvent records where the synchronous segment stopped
// and which block the worker will run first.
func pipelineSuspendedAuditEvent(request domain.ApprovalRequest, run *pipelineRun, point suspensionPoint, occurredAt time.Time) (domain.AuditEvent, error) {
	data := map[string]any{
		"pipeline_evaluation_id":       run.evaluationID,
		"agent_slug":                   request.AgentSlug,
		"resume_position":              point.position,
		"suspended_block_id":           point.block.ID,
		"suspended_block_key":          point.block.BlockKey,
		"suspended_block_type":         point.block.BlockType,
		"suspended_block_pipeline_key": point.pipelineKey,
		"attempted_block_count":        run.attemptedBlocks,
	}
	pipelinesAuditData(data, run.effective.pins())
	encoded, err := json.Marshal(data)
	return systemApprovalAuditEvent(request, pipelineSuspendedEvent, encoded, occurredAt), err
}

func pipelineCompletedAuditEvent(request domain.ApprovalRequest, evaluationID domain.ID, pins []pinnedPipeline, runtimeFingerprint string, outcome BlockOutcome, reasonCode string, terminal terminalBlock, attemptedBlocks, totalAttempts int, totalCost uint64, startedAt, completedAt time.Time) (domain.AuditEvent, error) {
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	data := map[string]any{
		"pipeline_evaluation_id": evaluationID,
		"agent_slug":             request.AgentSlug,
		"outcome":                outcome,
		"reason_code":            reasonCode,
		"attempted_block_count":  attemptedBlocks,
		"total_attempts":         totalAttempts,
		"total_cost":             totalCost,
		"duration_ms":            duration.Milliseconds(),
	}
	if len(pins) > 0 {
		pipelinesAuditData(data, pins)
		data["runtime_fingerprint"] = runtimeFingerprint
	}
	if terminal.ID != "" {
		data["terminal_block_id"] = terminal.ID
		data["terminal_block_key"] = terminal.Key
		data["terminal_block_name"] = terminal.Name
		data["terminal_block_pipeline_key"] = terminal.PipelineKey
	}
	encoded, err := json.Marshal(data)
	return systemApprovalAuditEvent(request, pipelineCompletedEvent, encoded, completedAt), err
}

func pipelineDecision(request domain.ApprovalRequest, evaluationID domain.ID, outcome BlockOutcome, decidedAt time.Time) *domain.Decision {
	status := domain.ApprovalStatus("")
	switch outcome {
	case BlockApprove:
		status = domain.ApprovalApproved
	case BlockDeny:
		status = domain.ApprovalDenied
	default:
		return nil
	}
	return &domain.Decision{
		ID: domain.ID(uuid.NewString()), RequestID: request.ID, OrganizationID: request.OrganizationID,
		Source: domain.DecisionSourcePipeline, Status: status, Channel: "pipeline",
		Auth:           domain.AuthContext{Method: "pipeline", Assurance: "automatic", AuthenticatedAt: decidedAt},
		IdempotencyKey: "pipeline:" + string(evaluationID), DecidedAt: decidedAt,
	}
}

func approvalDecidedAuditEvent(request domain.ApprovalRequest, evaluationID domain.ID, decision *domain.Decision, occurredAt time.Time) (domain.AuditEvent, error) {
	data, err := json.Marshal(map[string]any{
		"status":                 decision.Status,
		"source":                 decision.Source,
		"channel":                decision.Channel,
		"pipeline_evaluation_id": evaluationID,
	})
	return systemApprovalAuditEvent(request, "approval.decided", data, occurredAt), err
}

func systemApprovalAuditEvent(request domain.ApprovalRequest, eventType string, data json.RawMessage, occurredAt time.Time) domain.AuditEvent {
	return domain.AuditEvent{
		ID: domain.ID(uuid.NewString()), OrganizationID: request.OrganizationID,
		EventType: eventType, ActorType: domain.ActorSystem,
		SubjectType: "approval_request", SubjectID: request.ID, Data: data, OccurredAt: occurredAt,
	}
}

func attemptCount(result RequestApprovalBlockResult) int {
	if result.Attempts <= 0 {
		return 1
	}
	return result.Attempts
}

func completionReason(result RequestApprovalBlockResult) string {
	if result.ErrorCode == "" {
		return "block_outcome"
	}
	if result.Attempts > 1 {
		return "retries_exhausted"
	}
	switch result.ErrorCode {
	case ErrorTimeout:
		return "block_timeout"
	case ErrorInvalidBlockOutput:
		return "invalid_block_output"
	default:
		return "block_error"
	}
}
