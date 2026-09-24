package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func organizationDeletionWorkflowID(organizationID string) string {
	return fmt.Sprintf("tenant-deletion/%s", organizationID)
}

type OrganizationDeletionWorkflowInput struct {
	OrganizationID string
	PurgeAt        time.Time
}

// StartOrganizationDeletion starts the durable grace-period timer. The
// deterministic workflow ID makes rescheduling after a cancellation reuse
// the same identity, and a start racing an identical start is idempotent.
func (t *Temporal) StartOrganizationDeletion(ctx context.Context, input application.StartOrganizationDeletionInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	_, err := t.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    organizationDeletionWorkflowID(string(input.OrganizationID)),
		TaskQueue:             t.taskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	}, OrganizationDeletionWorkflow, OrganizationDeletionWorkflowInput{
		OrganizationID: string(input.OrganizationID), PurgeAt: input.PurgeAt,
	})
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &alreadyStarted) {
		return nil
	}
	return err
}

// CancelOrganizationDeletion stops the grace-period timer. A missing or
// already finished workflow is success: the purge activity independently
// refuses an organization that is no longer pending deletion.
func (t *Temporal) CancelOrganizationDeletion(ctx context.Context, input application.CancelOrganizationDeletionInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	err := t.client.CancelWorkflow(ctx, organizationDeletionWorkflowID(string(input.OrganizationID)), "")
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}

// OrganizationDeletionWorkflow waits out the grace period, then purges. The
// purge activity re-checks eligibility, so a cancellation that raced the
// timer results in a no-op rather than data loss.
func OrganizationDeletionWorkflow(ctx workflow.Context, input OrganizationDeletionWorkflowInput) error {
	if wait := input.PurgeAt.Sub(workflow.Now(ctx)); wait > 0 {
		if err := workflow.Sleep(ctx, wait); err != nil {
			return err
		}
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 5 * time.Second, BackoffCoefficient: 2,
			MaximumInterval: 5 * time.Minute, MaximumAttempts: 10,
		},
	})
	var activities *TenantActivities
	return workflow.ExecuteActivity(ctx, activities.PurgeOrganization, input).Get(ctx, nil)
}

type TenantActivities struct {
	store application.TenantRepository
	clock application.Clock
}

func NewTenantActivities(store application.TenantRepository, clock application.Clock) *TenantActivities {
	return &TenantActivities{store: store, clock: clock}
}

func (a *TenantActivities) PurgeOrganization(ctx context.Context, input OrganizationDeletionWorkflowInput) error {
	purged, err := a.store.PurgeOrganization(ctx, application.PurgeOrganizationInput{
		OrganizationID: domain.ID(input.OrganizationID),
		Now:            a.clock.Now(),
	})
	if err != nil {
		return err
	}
	logger := activity.GetLogger(ctx)
	if purged {
		logger.Info("organization purged", "organization_id", input.OrganizationID)
	} else {
		logger.Info("organization purge skipped: no longer eligible", "organization_id", input.OrganizationID)
	}
	return nil
}
