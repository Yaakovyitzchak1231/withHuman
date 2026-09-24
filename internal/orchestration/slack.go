package orchestration

import (
	"context"
	"errors"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/internal/slackconnect"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Each persisted event or connection transition gets an immediate attempt.
// The independent minute drain repairs a failed start or exhausted activity.
func (t *Temporal) DispatchSlackEvents(ctx context.Context, in application.DispatchSlackEventsInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	_, err := t.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: "slack-events/" + in.Key, TaskQueue: t.taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, SlackEventsWorkflow)
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &started) {
		return nil
	}
	return err
}

// The inbox is the dispatch queue. A minute cron always resumes pending work,
// including events received while Temporal or the worker was unavailable.
func (t *Temporal) EnsureSlackEvents(ctx context.Context) error {
	_, err := t.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: "slack-events", TaskQueue: t.taskQueue, CronSchedule: "* * * * *",
	}, SlackEventsWorkflow)
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &started) {
		return nil
	}
	return err
}

func SlackEventsWorkflow(ctx workflow.Context) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second, MaximumAttempts: 3},
	})
	var activities *SlackActivities
	return workflow.ExecuteActivity(ctx, activities.DrainSlackEvents).Get(ctx, nil)
}

type SlackActivities struct {
	Service    *slackconnect.Service
	Repository application.SlackLifecycleRepository
	Temporal   *Temporal
	Clock      application.Clock
}

func (a *SlackActivities) DrainSlackEvents(ctx context.Context) error {
	return errors.Join(a.Service.DrainEvents(ctx), a.dispatchDisconnections(ctx))
}

func (a *SlackActivities) dispatchDisconnections(ctx context.Context) error {
	notices, err := a.Repository.ListSlackDisconnections(ctx, application.ListPendingSlackEventsInput{Limit: 1000})
	if err != nil {
		return err
	}
	for _, notice := range notices {
		err := a.Temporal.client.SignalWorkflow(ctx, WorkflowID(notice.OrganizationID, notice.RequestID), "", SlackDisconnectedSignal, notice.InstallationID)
		var absent *serviceerror.NotFound
		if err != nil && !errors.As(err, &absent) {
			return err
		}
		if err := a.Repository.CompleteSlackDisconnection(ctx, application.CompleteSlackEventInput{ID: notice.ID, Now: a.Clock.Now()}); err != nil {
			return err
		}
	}
	return nil
}

const SlackDisconnectedSignal = "slack_disconnected"
