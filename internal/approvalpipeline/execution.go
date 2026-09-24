package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

const (
	pipelineStartedEvent   = "request_approval_pipeline.started"
	blockEvaluatedEvent    = "request_approval_block.evaluated"
	pipelineSuspendedEvent = "request_approval_pipeline.suspended"
	pipelineCompletedEvent = "request_approval_pipeline.completed"
)

// ExecutionService selects and executes the active immutable pipeline. On
// the synchronous request path it runs blocks until the first asynchronous
// one and suspends there; a worker resumes the suspended pipeline and runs
// every remaining block inline. CompiledState is shared with startup
// readiness and the management control plane.
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

// executionSection is one selected scope of the effective pipeline in
// execution order: the organization section first, then the agent-type
// section for the authenticated agent type.
type executionSection struct {
	Scope    string
	Revision domain.RequestApprovalPipelineRevision
	Compiled *compiledPipeline
}

// terminalBlock identifies the block whose outcome ended execution.
type terminalBlock struct {
	ID    domain.ID
	Key   string
	Name  string
	Scope string
}

// AbandonReasonCode is the completion reason written when the asynchronous
// segment could not run at all.
const AbandonReasonCode = "block_error"

// Evaluate prepares the complete atomic initialization write without
// performing persistence or starting durable orchestration. The effective
// pipeline is the organization section's blocks followed by the agent-type
// section's blocks; either section may be absent, the first terminal outcome
// wins, and falling through the combined list requires a human. Reaching an
// asynchronous block suspends the pipeline: the request is committed as
// pending with a suspension the worker resumes.
func (service *ExecutionService) Evaluate(ctx context.Context, request domain.ApprovalRequest) (application.InitializeApprovalInput, error) {
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
	sections := make([]executionSection, 0, 2)
	for _, scope := range []domain.AgentSlug{"", request.AgentSlug} {
		revision, err := service.pipelines.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
			OrganizationID: request.OrganizationID,
			AgentSlug:      scope,
		})
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return application.InitializeApprovalInput{}, err
		}
		sections = append(sections, executionSection{Scope: domain.PipelineScopeName(scope), Revision: revision})
	}
	if len(sections) == 0 {
		completed, marshalErr := pipelineCompletedAuditEvent(request, evaluationID, nil, service.compiled.Fingerprint(), BlockHuman, "no_active_revision", terminalBlock{}, 0, 0, 0, startedAt, service.clock.Now())
		if marshalErr != nil {
			return application.InitializeApprovalInput{}, marshalErr
		}
		events = append(events, completed)
		result := initializationInput(request, nil, events)
		result.HumanReview = humanReviewRoute(nil, terminalBlock{}, "")
		return result, nil
	}

	started, err := pipelineStartedAuditEvent(request, evaluationID, sections, service.compiled.Fingerprint(), startedAt)
	if err != nil {
		return application.InitializeApprovalInput{}, err
	}
	events = append(events, started)
	if err := service.loadCompiled(ctx, sections); err != nil {
		if !errors.Is(err, errCompiledPipelineCacheUnavailable) {
			return application.InitializeApprovalInput{}, err
		}
		// Never execute a partial effective pipeline: any section that
		// cannot load compiled state hands the whole request to a human.
		completed, marshalErr := pipelineCompletedAuditEvent(request, evaluationID, sections, service.compiled.Fingerprint(), BlockHuman, "compiled_cache_unavailable", terminalBlock{}, 0, 0, 0, startedAt, service.clock.Now())
		if marshalErr != nil {
			return application.InitializeApprovalInput{}, marshalErr
		}
		events = append(events, completed)
		result := initializationInput(request, nil, events)
		result.HumanReview = humanReviewRoute(sections, terminalBlock{}, "")
		return result, nil
	}

	run := &pipelineRun{
		request: request, evaluationID: evaluationID, sections: sections, startedAt: startedAt,
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
		result.HumanReview = humanReviewRoute(run.sections, run.terminal, run.escalationPath)
	}
	return result, nil
}

// Resume runs the remainder of a suspended pipeline, every block inline, and
// prepares the resume write. It never persists. The caller attaches routing
// when the outcome still needs a human.
func (service *ExecutionService) Resume(ctx context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension) (application.ResumeApprovalInput, error) {
	if request.ID != suspension.RequestID || request.OrganizationID != suspension.OrganizationID {
		return application.ResumeApprovalInput{}, fmt.Errorf("suspension does not belong to the request: %w", domain.ErrInvalid)
	}
	agent, err := service.loadAgent(ctx, request)
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	sections, err := service.pinnedSections(ctx, suspension)
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	run := &pipelineRun{
		request: request, evaluationID: suspension.EvaluationID, sections: sections, startedAt: suspension.StartedAt,
		input:           newBlockInput(request, agent, suspension.PreviousMetadata),
		attemptedBlocks: suspension.AttemptedBlockCount, totalAttempts: suspension.TotalAttempts, totalCost: suspension.TotalCost,
	}
	run.input.EvaluationID = suspension.EvaluationID
	if err := service.loadCompiled(ctx, sections); err != nil {
		if !errors.Is(err, errCompiledPipelineCacheUnavailable) {
			return application.ResumeApprovalInput{}, err
		}
		return service.Abandon(ctx, request, suspension, "compiled_cache_unavailable")
	}
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
		result.HumanReview = humanReviewRoute(run.sections, run.terminal, run.escalationPath)
	}
	return result, nil
}

// Abandon prepares the resume write for a suspended pipeline whose
// asynchronous segment could not run: the pipeline completes with the
// reason code and the request goes to a human.
func (service *ExecutionService) Abandon(ctx context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension, reasonCode string) (application.ResumeApprovalInput, error) {
	sections, err := service.pinnedSections(ctx, suspension)
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	completed, err := pipelineCompletedAuditEvent(request, suspension.EvaluationID, sections, service.compiled.Fingerprint(), BlockHuman, reasonCode, terminalBlock{},
		suspension.AttemptedBlockCount, suspension.TotalAttempts, suspension.TotalCost, suspension.StartedAt, service.clock.Now())
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	return application.ResumeApprovalInput{
		OrganizationID: request.OrganizationID, RequestID: request.ID, EvaluationID: suspension.EvaluationID,
		AuditEvents: []domain.AuditEvent{completed}, Now: service.clock.Now(),
		HumanReview: humanReviewRoute(sections, terminalBlock{}, ""),
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

// pinnedSections reloads the immutable revisions a suspension pinned, in
// execution order, whether or not they are still active.
func (service *ExecutionService) pinnedSections(ctx context.Context, suspension domain.PipelineSuspension) ([]executionSection, error) {
	sections := make([]executionSection, 0, 2)
	for _, pinned := range []struct {
		scope string
		id    domain.ID
	}{{domain.PipelineScopeOrganization, suspension.OrganizationPipelineID}, {domain.PipelineScopeAgent, suspension.AgentPipelineID}} {
		if pinned.id == "" {
			continue
		}
		revision, err := service.pipelines.GetRequestApprovalPipelineRevisionByID(ctx, application.GetRequestApprovalPipelineRevisionByIDInput{
			OrganizationID: suspension.OrganizationID, RevisionID: pinned.id,
		})
		if err != nil {
			return nil, err
		}
		sections = append(sections, executionSection{Scope: pinned.scope, Revision: revision})
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("suspension pins no pipeline revision: %w", domain.ErrInvalid)
	}
	return sections, nil
}

func (service *ExecutionService) loadCompiled(ctx context.Context, sections []executionSection) error {
	for index := range sections {
		compiled, err := service.compiled.Get(ctx, sections[index].Revision)
		if err != nil {
			return err
		}
		sections[index].Compiled = compiled
	}
	return nil
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
	sections     []executionSection
	startedAt    time.Time
	input        RequestApprovalBlockInput

	events          []domain.AuditEvent
	outcome         BlockOutcome
	reasonCode      string
	terminal        terminalBlock
	escalationPath  string
	attemptedBlocks int
	totalAttempts   int
	totalCost       uint64
}

// suspensionPoint is the block execution stopped in front of.
type suspensionPoint struct {
	position int
	block    domain.RequestApprovalBlock
	scope    string
}

// runBlocks executes blocks in combined pre-order starting at startPosition.
// A branch whose condition matches is entered and one that does not is
// skipped whole; a branch that runs out of blocks ends the run with a human.
// With suspendAsync, execution stops in front of the first asynchronous
// block it reaches and reports it; otherwise every block runs inline.
func (service *ExecutionService) runBlocks(ctx context.Context, run *pipelineRun, startPosition int, suspendAsync bool) (*suspensionPoint, error) {
	run.outcome = BlockHuman
	run.reasonCode = "end_of_pipeline"
	end := 0
	for _, section := range run.sections {
		blocks := section.Compiled.Blocks
		base := end
		end += len(blocks)
		if startPosition >= end {
			continue
		}
		cursor := newBranchCursor(blocks, max(startPosition-base, 0))
		for {
			step, index := cursor.next()
			if step == stepDone {
				break
			}
			block, snapshot, position := blocks[index], section.Compiled.Snapshots[index], base+index
			if step == stepBranchEnd {
				return nil, service.endBranch(run, section, snapshot, block, position)
			}
			if block.Branch {
				evaluated := executeBranchCondition(ctx, block.Executor, run.input)
				if err := service.record(run, section, snapshot, position, evaluated); err != nil {
					return nil, err
				}
				switch evaluated.Outcome {
				case BlockEnter:
					cursor.enter()
				case BlockNext:
					cursor.skip()
				default:
					// A condition that cannot be evaluated hands the request
					// to a person on the pipeline default: the branch's own
					// path is for its end only.
					run.finish(section, snapshot, evaluated, "")
					return nil, nil
				}
				continue
			}
			if suspendAsync && block.Asynchronous {
				return &suspensionPoint{position: position, block: snapshot, scope: section.Scope}, nil
			}
			evaluated := ExecuteRequestApprovalBlock(ctx, block.Executor, run.input)
			if err := service.record(run, section, snapshot, position, evaluated); err != nil {
				return nil, err
			}
			if evaluated.Outcome == BlockNext {
				if len(evaluated.Metadata) > 0 {
					metadata, err := json.Marshal(evaluated.Metadata)
					if err != nil {
						return nil, err
					}
					run.input.PreviousMetadata[section.Scope+"/"+snapshot.BlockKey] = metadata
				}
				cursor.advance()
				continue
			}
			run.finish(section, snapshot, evaluated, block.EscalationPath)
			return nil, nil
		}
	}
	return nil, nil
}

// record counts one attempted block and appends its evaluated event.
func (service *ExecutionService) record(run *pipelineRun, section executionSection, snapshot domain.RequestApprovalBlock, position int, evaluated RequestApprovalBlockResult) error {
	run.attemptedBlocks++
	run.totalAttempts += attemptCount(evaluated)
	run.totalCost += evaluated.Evidence.Cost
	event, err := blockEvaluatedAuditEvent(run.request, run.evaluationID, section, snapshot, position, evaluated, service.clock.Now())
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
func (service *ExecutionService) endBranch(run *pipelineRun, section executionSection, snapshot domain.RequestApprovalBlock, block compiledBlock, position int) error {
	event, err := branchEndedAuditEvent(run.request, run.evaluationID, section, snapshot, position, block.Reason, service.clock.Now())
	if err != nil {
		return err
	}
	run.events = append(run.events, event)
	run.outcome = BlockHuman
	run.reasonCode = "end_of_branch"
	run.terminal = terminalBlock{ID: snapshot.ID, Key: snapshot.BlockKey, Name: snapshot.Name, Scope: section.Scope}
	run.escalationPath = block.EscalationPath
	return nil
}

// finish records the block whose outcome ended the run.
func (run *pipelineRun) finish(section executionSection, snapshot domain.RequestApprovalBlock, evaluated RequestApprovalBlockResult, escalationPath string) {
	run.outcome = evaluated.Outcome
	run.reasonCode = completionReason(evaluated)
	run.terminal = terminalBlock{ID: snapshot.ID, Key: snapshot.BlockKey, Name: snapshot.Name, Scope: section.Scope}
	run.escalationPath = escalationPath
}

func (run *pipelineRun) suspension(position int, now time.Time) domain.PipelineSuspension {
	suspension := domain.PipelineSuspension{
		RequestID: run.request.ID, OrganizationID: run.request.OrganizationID, EvaluationID: run.evaluationID,
		ResumePosition: position, PreviousMetadata: run.input.PreviousMetadata,
		AttemptedBlockCount: run.attemptedBlocks, TotalAttempts: run.totalAttempts, TotalCost: run.totalCost,
		StartedAt: run.startedAt, SuspendedAt: now,
	}
	for _, section := range run.sections {
		switch section.Scope {
		case domain.PipelineScopeOrganization:
			suspension.OrganizationPipelineID = section.Revision.ID
		case domain.PipelineScopeAgent:
			suspension.AgentPipelineID = section.Revision.ID
		}
	}
	return suspension
}

// completion builds the completed event and, for an automatic outcome, the
// decision and its audit event.
func (service *ExecutionService) completion(run *pipelineRun) (domain.AuditEvent, *domain.Decision, domain.AuditEvent, error) {
	completedAt := service.clock.Now()
	completed, err := pipelineCompletedAuditEvent(run.request, run.evaluationID, run.sections, service.compiled.Fingerprint(), run.outcome, run.reasonCode, run.terminal,
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
// escalation path the terminal block named, else the effective pipeline
// default (the agent-type section's, then the organization's), else none,
// which leaves the request to any authorized reviewer.
func humanReviewRoute(sections []executionSection, terminal terminalBlock, escalationPath string) *application.HumanReviewRoute {
	if escalationPath != "" {
		return &application.HumanReviewRoute{
			PathKey:            escalationPath,
			Reason:             domain.RoutingReasonBlockEscalation,
			PipelineRevisionID: sectionRevisionID(sections, terminal.Scope),
			BlockID:            terminal.ID,
		}
	}
	return defaultReviewRoute(sections)
}

// defaultReviewRoute is the effective pipeline default alone: what a
// fall-through request and a human exit that names no path inherit.
func defaultReviewRoute(sections []executionSection) *application.HumanReviewRoute {
	for _, scope := range []string{domain.PipelineScopeAgent, domain.PipelineScopeOrganization} {
		for _, section := range sections {
			if section.Scope != scope || section.Revision.DefaultEscalationPath == "" {
				continue
			}
			return &application.HumanReviewRoute{
				PathKey: section.Revision.DefaultEscalationPath, Reason: domain.RoutingReasonPipelineDefault,
				PipelineRevisionID: section.Revision.ID,
			}
		}
	}
	return &application.HumanReviewRoute{Reason: domain.RoutingReasonNoEscalation}
}

func sectionRevisionID(sections []executionSection, scope string) domain.ID {
	for _, section := range sections {
		if section.Scope == scope {
			return section.Revision.ID
		}
	}
	return ""
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

// sectionAuditData records each scope's pinned revision explicitly, with
// null values for a scope that had no active revision.
func sectionAuditData(data map[string]any, sections []executionSection) {
	data["organization_pipeline_id"] = nil
	data["organization_revision"] = nil
	data["agent_pipeline_id"] = nil
	data["agent_revision"] = nil
	for _, section := range sections {
		data[section.Scope+"_pipeline_id"] = section.Revision.ID
		data[section.Scope+"_revision"] = section.Revision.Revision
	}
}

func pipelineStartedAuditEvent(request domain.ApprovalRequest, evaluationID domain.ID, sections []executionSection, runtimeFingerprint string, occurredAt time.Time) (domain.AuditEvent, error) {
	blockCount := 0
	for _, section := range sections {
		blockCount += len(section.Revision.BlockIDs)
	}
	data := map[string]any{
		"pipeline_evaluation_id": evaluationID,
		"agent_slug":             request.AgentSlug,
		"block_count":            blockCount,
		"runtime_fingerprint":    runtimeFingerprint,
	}
	sectionAuditData(data, sections)
	encoded, err := json.Marshal(data)
	return systemApprovalAuditEvent(request, pipelineStartedEvent, encoded, occurredAt), err
}

func blockEvaluatedAuditEvent(request domain.ApprovalRequest, evaluationID domain.ID, section executionSection, block domain.RequestApprovalBlock, position int, result RequestApprovalBlockResult, occurredAt time.Time) (domain.AuditEvent, error) {
	fields := blockEvaluatedFields(evaluationID, section, block, position, result.Outcome, result.Reason)
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

// branchEndedAuditEvent is the closing evaluated event of a branch a request
// entered and ran out of: a human outcome with the branch's reason, made
// without evaluating anything.
func branchEndedAuditEvent(request domain.ApprovalRequest, evaluationID domain.ID, section executionSection, block domain.RequestApprovalBlock, position int, reason string, occurredAt time.Time) (domain.AuditEvent, error) {
	fields := blockEvaluatedFields(evaluationID, section, block, position, BlockHuman, reason)
	fields["attempt_count"] = 0
	fields["cost"] = 0
	fields["error_code"] = ""
	fields["metadata"] = nil
	fields["trace"] = []NodeTrace{}
	fields["end_of_branch"] = true
	data, err := json.Marshal(fields)
	return systemApprovalAuditEvent(request, blockEvaluatedEvent, data, occurredAt), err
}

func blockEvaluatedFields(evaluationID domain.ID, section executionSection, block domain.RequestApprovalBlock, position int, outcome BlockOutcome, reason string) map[string]any {
	return map[string]any{
		"pipeline_evaluation_id": evaluationID,
		"scope":                  section.Scope,
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
}

// pipelineSuspendedAuditEvent records where the synchronous segment stopped
// and which block the worker will run first.
func pipelineSuspendedAuditEvent(request domain.ApprovalRequest, run *pipelineRun, point suspensionPoint, occurredAt time.Time) (domain.AuditEvent, error) {
	data := map[string]any{
		"pipeline_evaluation_id": run.evaluationID,
		"agent_slug":             request.AgentSlug,
		"resume_position":        point.position,
		"suspended_block_id":     point.block.ID,
		"suspended_block_key":    point.block.BlockKey,
		"suspended_block_type":   point.block.BlockType,
		"suspended_block_scope":  point.scope,
		"attempted_block_count":  run.attemptedBlocks,
	}
	sectionAuditData(data, run.sections)
	encoded, err := json.Marshal(data)
	return systemApprovalAuditEvent(request, pipelineSuspendedEvent, encoded, occurredAt), err
}

func pipelineCompletedAuditEvent(request domain.ApprovalRequest, evaluationID domain.ID, sections []executionSection, runtimeFingerprint string, outcome BlockOutcome, reasonCode string, terminal terminalBlock, attemptedBlocks, totalAttempts int, totalCost uint64, startedAt, completedAt time.Time) (domain.AuditEvent, error) {
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
	if len(sections) > 0 {
		sectionAuditData(data, sections)
		data["runtime_fingerprint"] = runtimeFingerprint
	}
	if terminal.ID != "" {
		data["terminal_block_id"] = terminal.ID
		data["terminal_block_key"] = terminal.Key
		data["terminal_block_name"] = terminal.Name
		data["terminal_block_scope"] = terminal.Scope
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
