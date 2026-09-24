package orchestration

import (
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func TestDeliverNotificationStepSkipsTerminalRequest(t *testing.T) {
	for _, status := range []domain.ApprovalStatus{
		domain.ApprovalApproved, domain.ApprovalDenied, domain.ApprovalExpired, domain.ApprovalCancelled,
	} {
		t.Run(string(status), func(t *testing.T) {
			pending := workflowRequest(10 * time.Minute)
			current := pending
			current.Status = status
			notifier := &recordingNotifier{}
			activities := NewActivities(&workflowRepository{request: current}, nil, nil, notifier, workflowClock{now: pending.CreatedAt})
			var suite testsuite.WorkflowTestSuite
			environment := suite.NewTestActivityEnvironment()
			environment.RegisterActivity(activities.DeliverNotificationStep)

			_, err := environment.ExecuteActivity(activities.DeliverNotificationStep, pending, application.NotificationStep{
				Recipients: []application.NotificationRecipient{{Emails: []string{"ana@example.com"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if notifier.callCount() != 0 {
				t.Fatal("a terminal request must not notify reviewers")
			}
		})
	}
}

func TestDeliverNotificationStepRequiresOrganizationScopedRequest(t *testing.T) {
	for _, field := range []string{"organization", "request"} {
		t.Run(field, func(t *testing.T) {
			request := workflowRequest(10 * time.Minute)
			loaded := request
			if field == "organization" {
				loaded.OrganizationID = "another-organization"
			} else {
				loaded.ID = "another-request"
			}
			notifier := &recordingNotifier{}
			activities := NewActivities(&workflowRepository{request: request}, nil, nil, notifier, workflowClock{now: request.CreatedAt})
			var suite testsuite.WorkflowTestSuite
			environment := suite.NewTestActivityEnvironment()
			environment.RegisterActivity(activities.DeliverNotificationStep)

			_, err := environment.ExecuteActivity(activities.DeliverNotificationStep, loaded, application.NotificationStep{
				Recipients: []application.NotificationRecipient{{Emails: []string{"ana@example.com"}}},
			})
			var applicationErr *temporal.ApplicationError
			if !errors.As(err, &applicationErr) || applicationErr.Message() != domain.ErrNotFound.Error() {
				t.Fatalf("missing request lookup: got %v, want not found", err)
			}
			if notifier.callCount() != 0 {
				t.Fatal("a failed request lookup must not notify reviewers")
			}
		})
	}
}
