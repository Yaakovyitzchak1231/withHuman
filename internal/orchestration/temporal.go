package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/loggermdc"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

const (
	DefaultTaskQueue = "withhuman-approvals"
	DecisionSignal   = "decision"
	ClaimSignal      = "claim"
	CancelSignal     = "cancel"
	// ClaimReleasedSignal tells the workflow a claim ended early so
	// escalation resumes without waiting for the retrigger timer.
	ClaimReleasedSignal = "claim_released"
)

type Temporal struct {
	client    client.Client
	taskQueue string
}

func NewTemporal(temporalClient client.Client, taskQueue string) *Temporal {
	if taskQueue == "" {
		taskQueue = DefaultTaskQueue
	}
	return &Temporal{client: temporalClient, taskQueue: taskQueue}
}

func WorkflowID(organizationID, requestID domain.ID) string {
	return fmt.Sprintf("approval/%s/%s", organizationID, requestID)
}

func (t *Temporal) StartApproval(ctx context.Context, input application.StartApprovalInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	request := input.Request
	workflowID := WorkflowID(request.OrganizationID, request.ID)
	ctx = loggermdc.With(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID:    string(request.OrganizationID),
		loggermdc.KeyApprovalRequestID: string(request.ID),
		loggermdc.KeyWorkflowID:        workflowID,
	})
	_, err := t.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: workflowID, TaskQueue: t.taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &alreadyStarted) {
		return nil
	}
	return err
}

func (t *Temporal) SubmitDecision(ctx context.Context, input application.SubmitDecisionInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	command := input.Command
	workflowID := WorkflowID(command.OrganizationID, command.RequestID)
	ctx = loggermdc.With(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID:      string(command.OrganizationID),
		loggermdc.KeyApprovalRequestID:   string(command.RequestID),
		loggermdc.KeyWorkflowID:          workflowID,
		loggermdc.KeyDecisionOperationID: string(command.OperationID),
	})
	signal := decisionSignal{Command: command, MDC: loggermdc.Propagated(loggermdc.From(ctx))}
	_, err := t.client.SignalWithStartWorkflow(ctx, workflowID, DecisionSignal, signal, client.StartWorkflowOptions{
		ID: workflowID, TaskQueue: t.taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: command.OrganizationID, RequestID: command.RequestID})
	// The reuse policy refuses to start a closed workflow again, and a
	// workflow closes only once its request is terminal.
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &alreadyStarted) {
		return fmt.Errorf("%w: %w", application.ErrWorkflowClosed, err)
	}
	return err
}

// SubmitClaim arms the claim's retrigger timer on the request workflow. It
// signals only: the outbox dispatcher owns workflow starts, and a request
// whose workflow already closed is terminal, so its claim write was
// rejected before this call. The claim row itself committed before the
// signal; a delivery failure only delays the lapse audit until the next
// claim attempt sweeps the stale row.
func (t *Temporal) SubmitClaim(ctx context.Context, input application.SubmitClaimInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	workflowID := WorkflowID(input.OrganizationID, input.RequestID)
	ctx = loggermdc.With(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID:    string(input.OrganizationID),
		loggermdc.KeyApprovalRequestID: string(input.RequestID),
		loggermdc.KeyWorkflowID:        workflowID,
	})
	signal := claimSignal{
		ClaimID: input.ClaimID, ExpiresAt: input.ExpiresAt,
		MDC: loggermdc.Propagated(loggermdc.From(ctx)),
	}
	return t.client.SignalWorkflow(ctx, workflowID, "", ClaimSignal, signal)
}

// SubmitCancellation stands the request's workflow down after the
// cancellation committed. Like SubmitClaim it only signals: a workflow
// that has not started yet loads a terminal request and ends at once,
// and one that already closed has nothing left to stand down. A lost
// signal costs at most the notification rungs still armed, because the
// deadline activity is a no-op against a terminal request.
func (t *Temporal) SubmitCancellation(ctx context.Context, input application.SubmitCancellationInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	workflowID := WorkflowID(input.OrganizationID, input.RequestID)
	ctx = loggermdc.With(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID:    string(input.OrganizationID),
		loggermdc.KeyApprovalRequestID: string(input.RequestID),
		loggermdc.KeyWorkflowID:        workflowID,
	})
	signal := cancelSignal{MDC: loggermdc.Propagated(loggermdc.From(ctx))}
	err := t.client.SignalWorkflow(ctx, workflowID, "", CancelSignal, signal)
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}

func (t *Temporal) Health(ctx context.Context) error {
	_, err := t.client.CheckHealth(ctx, &client.CheckHealthRequest{})
	return err
}

// SubmitClaimRelease signals an early claim release. Signal-only, like
// SubmitClaim: a lost signal costs nothing worse than escalation resuming
// when the retrigger timer lapses against the already released claim.
func (t *Temporal) SubmitClaimRelease(ctx context.Context, input application.SubmitClaimReleaseInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	workflowID := WorkflowID(input.OrganizationID, input.RequestID)
	ctx = loggermdc.With(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID:    string(input.OrganizationID),
		loggermdc.KeyApprovalRequestID: string(input.RequestID),
		loggermdc.KeyWorkflowID:        workflowID,
	})
	signal := claimReleasedSignal{ClaimID: input.ClaimID, MDC: loggermdc.Propagated(loggermdc.From(ctx))}
	err := t.client.SignalWorkflow(ctx, workflowID, "", ClaimReleasedSignal, signal)
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}
