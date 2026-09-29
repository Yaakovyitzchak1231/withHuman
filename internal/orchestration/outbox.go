package orchestration

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/loggermdc"
)

const approvalWorkflowDispatchInterval = 250 * time.Millisecond

// ApprovalWorkflowDispatcher completes the durable PostgreSQL-to-Temporal
// handoff for pending human reviews, and signals the agent-owner handoffs
// written when an owner is deactivated. Workflow IDs make repeated
// delivery safe, and the workflow ignores a repeated handoff signal.
type ApprovalWorkflowDispatcher struct {
	approvals    application.ApprovalRepository
	handoffs     WorkflowOutboxRepository
	orchestrator application.LifecycleOrchestrator
	clock        application.Clock
	logger       *slog.Logger
}

// WorkflowOutboxRepository is the outbox the dispatcher drains: workflow
// starts and agent-owner handoff signals.
type WorkflowOutboxRepository interface {
	application.ApprovalWorkflowOutboxRepository
	application.AgentOwnerHandoffOutboxRepository
}

func NewApprovalWorkflowDispatcher(approvals application.ApprovalRepository, handoffs WorkflowOutboxRepository, orchestrator application.LifecycleOrchestrator, clock application.Clock, logger *slog.Logger) *ApprovalWorkflowDispatcher {
	return &ApprovalWorkflowDispatcher{
		approvals: approvals, handoffs: handoffs, orchestrator: orchestrator, clock: clock, logger: logger,
	}
}

func (dispatcher *ApprovalWorkflowDispatcher) Run(ctx context.Context) {
	_ = dispatcher.Dispatch(ctx)
	ticker := time.NewTicker(approvalWorkflowDispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = dispatcher.Dispatch(ctx)
		}
	}
}

func (dispatcher *ApprovalWorkflowDispatcher) Dispatch(ctx context.Context) error {
	starts, err := dispatcher.handoffs.ListApprovalWorkflowStarts(ctx, application.ListApprovalWorkflowStartsInput{Limit: 100})
	if err != nil {
		return dispatcher.failed(ctx, err)
	}
	for _, start := range starts {
		requestContext := loggermdc.With(ctx, loggermdc.Fields{
			loggermdc.KeyOrganizationID:    string(start.OrganizationID),
			loggermdc.KeyApprovalRequestID: string(start.RequestID),
			loggermdc.KeyWorkflowID:        WorkflowID(start.OrganizationID, start.RequestID),
		})
		request, err := dispatcher.approvals.GetApproval(requestContext, application.GetApprovalInput{
			OrganizationID: start.OrganizationID,
			RequestID:      start.RequestID,
		})
		if err != nil {
			return dispatcher.failed(requestContext, err)
		}
		if request.Status == domain.ApprovalPending {
			if err := dispatcher.orchestrator.StartApproval(requestContext, application.StartApprovalInput{Request: request}); err != nil {
				return dispatcher.failed(requestContext, err)
			}
		}
		if err := dispatcher.handoffs.MarkApprovalWorkflowStarted(requestContext, application.MarkApprovalWorkflowStartedInput{
			OrganizationID: start.OrganizationID,
			RequestID:      start.RequestID,
			PublishedAt:    dispatcher.clock.Now(),
		}); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return dispatcher.failed(requestContext, err)
		}
	}
	return dispatcher.dispatchAgentOwnerHandoffs(ctx)
}

func (dispatcher *ApprovalWorkflowDispatcher) dispatchAgentOwnerHandoffs(ctx context.Context) error {
	handoffs, err := dispatcher.handoffs.ListAgentOwnerHandoffSignals(ctx, application.ListAgentOwnerHandoffSignalsInput{Limit: 100})
	if err != nil {
		return dispatcher.failed(ctx, err)
	}
	for _, handoff := range handoffs {
		requestContext := loggermdc.With(ctx, loggermdc.Fields{
			loggermdc.KeyOrganizationID:    string(handoff.OrganizationID),
			loggermdc.KeyApprovalRequestID: string(handoff.RequestID),
			loggermdc.KeyWorkflowID:        WorkflowID(handoff.OrganizationID, handoff.RequestID),
		})
		if err := dispatcher.orchestrator.SubmitAgentOwnerHandoff(requestContext, application.SubmitAgentOwnerHandoffInput{
			OrganizationID: handoff.OrganizationID, RequestID: handoff.RequestID,
		}); err != nil {
			return dispatcher.failed(requestContext, err)
		}
		if err := dispatcher.handoffs.MarkAgentOwnerHandoffSignaled(requestContext, application.MarkAgentOwnerHandoffSignaledInput{
			OrganizationID: handoff.OrganizationID, RequestID: handoff.RequestID, SignaledAt: dispatcher.clock.Now(),
		}); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return dispatcher.failed(requestContext, err)
		}
	}
	return nil
}

func (dispatcher *ApprovalWorkflowDispatcher) failed(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		dispatcher.logger.ErrorContext(ctx, "human review workflow dispatch failed; outbox will retry", "error", err)
	}
	return err
}
