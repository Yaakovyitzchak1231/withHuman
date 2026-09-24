package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
)

func TestSlackDisconnectionWakesQueuedFallbackOnlyOnce(t *testing.T) {
	request := workflowRequest(3 * time.Minute)
	notifier := &recordingNotifier{}
	activities := NewActivities(&workflowRepository{request: request}, &stubRecipientResolver{}, &stubRuleRepository{}, notifier, workflowClock{now: request.DeadlineAt})
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartTime(request.CreatedAt)
	env.RegisterActivity(activities)
	env.OnActivity(activities.PlanNotifications, mock.Anything, mock.Anything).Return([]application.NotificationStep{
		{Delay: time.Minute, Recipients: []application.NotificationRecipient{{UserID: "slack-user", MembershipID: "slack-member", SlackUserID: "U1", SlackInstallationID: "generation", SlackFallbackEmail: "owner@example.com"}}},
		{Delay: 2 * time.Minute, Recipients: []application.NotificationRecipient{{UserID: "email-user", MembershipID: "email-member", Emails: []string{"other@example.com"}}}},
	}, nil)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(SlackDisconnectedSignal, domain.ID("older-generation")) }, 5*time.Second)
	env.RegisterDelayedCallback(func() {
		if notifier.callCount() != 0 {
			t.Fatal("stale signal changed the notification schedule")
		}
		env.SignalWorkflow(SlackDisconnectedSignal, domain.ID("generation"))
	}, 10*time.Second)
	env.RegisterDelayedCallback(func() {
		inputs := notifier.allInputs()
		if len(inputs) != 1 || !inputs[0].Recipients[0].SlackFallback || inputs[0].Recipients[0].SlackUserID != "" {
			t.Fatalf("immediate fallback=%+v", inputs)
		}
		env.SignalWorkflow(SlackDisconnectedSignal, domain.ID("generation"))
	}, 11*time.Second)
	env.RegisterDelayedCallback(func() {
		if notifier.callCount() != 1 {
			t.Fatal("duplicate signal or old timer repeated the fallback")
		}
	}, 90*time.Second)
	env.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	inputs := notifier.allInputs()
	if len(inputs) != 2 || len(inputs[1].Recipients[0].Emails) != 1 {
		t.Fatalf("remaining scheduled channel=%+v", inputs)
	}
}

type slackNoticeRepository struct {
	application.SlackLifecycleRepository
	notices     []application.SlackDisconnection
	completeErr error
}

func (r *slackNoticeRepository) ListSlackDisconnections(context.Context, application.ListPendingSlackEventsInput) ([]application.SlackDisconnection, error) {
	return r.notices, nil
}
func (r *slackNoticeRepository) CompleteSlackDisconnection(context.Context, application.CompleteSlackEventInput) error {
	if r.completeErr != nil {
		return r.completeErr
	}
	r.notices = nil
	return nil
}

type slackSignalClient struct {
	client.Client
	err   error
	calls int
}

func (c *slackSignalClient) SignalWorkflow(context.Context, string, string, string, interface{}) error {
	c.calls++
	return c.err
}

func TestSlackDisconnectionDispatchRetriesUnacknowledgedWork(t *testing.T) {
	repo := &slackNoticeRepository{notices: []application.SlackDisconnection{{ID: "notice", OrganizationID: "org", RequestID: "request", InstallationID: "generation"}}}
	client := &slackSignalClient{err: errors.New("Temporal unavailable")}
	activities := &SlackActivities{Repository: repo, Temporal: NewTemporal(client, ""), Clock: application.RealClock{}}
	if err := activities.dispatchDisconnections(context.Background()); err == nil || len(repo.notices) != 1 {
		t.Fatal("dispatch failure lost pending work")
	}
	client.err = nil
	repo.completeErr = errors.New("database unavailable after signal")
	if err := activities.dispatchDisconnections(context.Background()); err == nil || len(repo.notices) != 1 {
		t.Fatal("acknowledgment failure lost pending work")
	}
	repo.completeErr = nil
	if err := activities.dispatchDisconnections(context.Background()); err != nil || len(repo.notices) != 0 || client.calls != 3 {
		t.Fatalf("retry=%v pending=%d calls=%d", err, len(repo.notices), client.calls)
	}
}
