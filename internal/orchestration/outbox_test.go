package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/loggermdc"
)

type dispatcherOutbox struct {
	starts   []application.ApprovalWorkflowStart
	marked   int
	handoffs []application.AgentOwnerHandoffSignal
	signaled int
}

func (outbox *dispatcherOutbox) ListAgentOwnerHandoffSignals(context.Context, application.ListAgentOwnerHandoffSignalsInput) ([]application.AgentOwnerHandoffSignal, error) {
	return outbox.handoffs, nil
}

func (outbox *dispatcherOutbox) MarkAgentOwnerHandoffSignaled(context.Context, application.MarkAgentOwnerHandoffSignaledInput) error {
	outbox.signaled++
	return nil
}

func (outbox *dispatcherOutbox) ListApprovalWorkflowStarts(context.Context, application.ListApprovalWorkflowStartsInput) ([]application.ApprovalWorkflowStart, error) {
	return outbox.starts, nil
}

func (outbox *dispatcherOutbox) MarkApprovalWorkflowStarted(context.Context, application.MarkApprovalWorkflowStartedInput) error {
	outbox.marked++
	return nil
}

type dispatcherOrchestrator struct {
	starts   int
	handoffs int
	err      error
}

func (orchestrator *dispatcherOrchestrator) SubmitAgentOwnerHandoff(context.Context, application.SubmitAgentOwnerHandoffInput) error {
	orchestrator.handoffs++
	return orchestrator.err
}

func (orchestrator *dispatcherOrchestrator) StartApproval(context.Context, application.StartApprovalInput) error {
	orchestrator.starts++
	return orchestrator.err
}

func (*dispatcherOrchestrator) SubmitDecision(context.Context, application.SubmitDecisionInput) error {
	return nil
}

func (*dispatcherOrchestrator) SubmitClaim(context.Context, application.SubmitClaimInput) error {
	return nil
}
func (*dispatcherOrchestrator) SubmitCancellation(context.Context, application.SubmitCancellationInput) error {
	return nil
}
func (*dispatcherOrchestrator) SubmitClaimRelease(context.Context, application.SubmitClaimReleaseInput) error {
	return nil
}

func (*dispatcherOrchestrator) Health(context.Context) error { return nil }

func TestApprovalWorkflowDispatcherStartsOnlyPendingRequests(t *testing.T) {
	now := time.Unix(5_000, 0).UTC()
	request := workflowRequest(time.Minute)
	repository := &workflowRepository{request: request}
	outbox := &dispatcherOutbox{starts: []application.ApprovalWorkflowStart{{
		EventID: "event-1", OrganizationID: request.OrganizationID, RequestID: request.ID, CreatedAt: now,
	}}}
	orchestrator := &dispatcherOrchestrator{}
	dispatcher := NewApprovalWorkflowDispatcher(repository, outbox, orchestrator, workflowClock{now: now}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := dispatcher.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if orchestrator.starts != 1 || outbox.marked != 1 {
		t.Fatalf("pending handoff: starts=%d marked=%d", orchestrator.starts, outbox.marked)
	}

	repository.request.Status = domain.ApprovalApproved
	orchestrator.starts, outbox.marked = 0, 0
	if err := dispatcher.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if orchestrator.starts != 0 || outbox.marked != 1 {
		t.Fatalf("terminal handoff: starts=%d marked=%d", orchestrator.starts, outbox.marked)
	}
}

func TestApprovalWorkflowDispatcherRetriesFailedTemporalStart(t *testing.T) {
	request := workflowRequest(time.Minute)
	repository := &workflowRepository{request: request}
	outbox := &dispatcherOutbox{starts: []application.ApprovalWorkflowStart{{
		EventID: "event-1", OrganizationID: request.OrganizationID, RequestID: request.ID, CreatedAt: request.CreatedAt,
	}}}
	orchestrator := &dispatcherOrchestrator{err: errors.New("temporal unavailable")}
	var output bytes.Buffer
	logger := loggermdc.Wrap(slog.New(slog.NewJSONHandler(&output, nil)))
	dispatcher := NewApprovalWorkflowDispatcher(repository, outbox, orchestrator, workflowClock{now: request.CreatedAt}, logger)

	if err := dispatcher.Dispatch(context.Background()); err == nil {
		t.Fatal("Temporal failure was not returned")
	}
	if outbox.marked != 0 {
		t.Fatal("failed workflow start was marked published")
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["organization_id"] != string(request.OrganizationID) || record["approval_request_id"] != string(request.ID) || record["workflow_id"] != WorkflowID(request.OrganizationID, request.ID) {
		t.Fatalf("outbox failure lost its request context: %#v", record)
	}
}

func TestApprovalWorkflowDispatcherSignalsAgentOwnerHandoffs(t *testing.T) {
	now := time.Unix(5_000, 0).UTC()
	request := workflowRequest(time.Minute)
	outbox := &dispatcherOutbox{handoffs: []application.AgentOwnerHandoffSignal{{OrganizationID: request.OrganizationID, RequestID: request.ID}}}
	orchestrator := &dispatcherOrchestrator{}
	dispatcher := NewApprovalWorkflowDispatcher(&workflowRepository{request: request}, outbox, orchestrator, workflowClock{now: now}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := dispatcher.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if orchestrator.handoffs != 1 || outbox.signaled != 1 {
		t.Fatalf("handoff: signals=%d marked=%d", orchestrator.handoffs, outbox.signaled)
	}

	// A signal Temporal refused stays unmarked for the next pass.
	orchestrator.err, orchestrator.handoffs, outbox.signaled = errors.New("temporal unavailable"), 0, 0
	if err := dispatcher.Dispatch(context.Background()); err == nil {
		t.Fatal("a failed signal must be reported")
	}
	if outbox.signaled != 0 {
		t.Fatalf("a failed signal was marked: %d", outbox.signaled)
	}
}
