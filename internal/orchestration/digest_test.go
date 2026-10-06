package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"go.temporal.io/sdk/testsuite"
)

// scriptedWindows holds the kinds named in hold until closesAt, sends
// every other kind at once, and hands back closed on every close.
type scriptedWindows struct {
	mu       sync.Mutex
	hold     map[domain.ContactMethodKind]bool
	closesAt time.Time
	closed   []domain.ApprovalRequest
	admitErr error
	admits   []application.AdmitNotificationInput
	closes   []application.CloseNotificationWindowInput
}

func (w *scriptedWindows) AdmitNotification(_ context.Context, input application.AdmitNotificationInput) (application.NotificationAdmission, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.admits = append(w.admits, input)
	if w.admitErr != nil {
		return application.NotificationAdmission{}, w.admitErr
	}
	if w.hold[input.Kind] {
		return application.NotificationAdmission{Held: true, ClosesAt: w.closesAt}, nil
	}
	return application.NotificationAdmission{}, nil
}

func (w *scriptedWindows) CloseNotificationWindow(_ context.Context, input application.CloseNotificationWindowInput) (application.ClosedNotificationWindow, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closes = append(w.closes, input)
	return application.ClosedNotificationWindow{Requests: w.closed}, nil
}

func (w *scriptedWindows) closeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.closes)
}

func runDigestWorkflow(t *testing.T, windows *scriptedWindows, decideAfter time.Duration) (*recordingNotifier, domain.ApprovalRequest) {
	t.Helper()
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, ladderResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt},
		WithNotificationWindows(windows))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.SetStartTime(request.CreatedAt)
	environment.RegisterActivity(activities)
	if decideAfter > 0 {
		command := domain.DecisionCommand{OperationID: "operation-1", RequestID: request.ID, OrganizationID: request.OrganizationID, Status: domain.ApprovalApproved}
		environment.RegisterDelayedCallback(func() {
			environment.SignalWorkflow(DecisionSignal, decisionSignal{Command: command})
		}, decideAfter)
	}
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	return notifier, request
}

func digestRequest(request domain.ApprovalRequest, id domain.ID) domain.ApprovalRequest {
	request.ID = id
	return request
}

func TestApprovalWorkflowHoldsAKindUntilItsWindowClosesThenSendsADigest(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	windows := &scriptedWindows{
		hold:     map[domain.ContactMethodKind]bool{domain.ContactSlack: true},
		closesAt: request.CreatedAt.Add(time.Minute),
		closed:   []domain.ApprovalRequest{digestRequest(request, "request-2"), request},
	}
	notifier, _ := runDigestWorkflow(t, windows, 0)

	prompts := notifier.allInputs()
	if len(prompts) != 1 || prompts[0].Recipients[0].SlackUserID != "" || len(prompts[0].Recipients[0].Emails) != 1 {
		t.Fatalf("only the email rung should send as a prompt; Slack was held: %#v", prompts)
	}
	digests := notifier.allDigests()
	if len(digests) != 1 {
		t.Fatalf("the closed window should send one digest, got %d", len(digests))
	}
	recipient := digests[0].Recipient
	if recipient.SlackUserID != "U0ANA" || len(recipient.Emails) != 0 || recipient.UserID != "user-1" {
		t.Fatalf("the digest should be narrowed to Slack: %#v", recipient)
	}
	if len(windows.closes) != 1 || windows.closes[0].Kind != domain.ContactSlack || !windows.closes[0].ClosesAt.Equal(windows.closesAt) || windows.closes[0].MembershipID != "membership-1" {
		t.Fatalf("the workflow should close the Slack window once at its instant: %#v", windows.closes)
	}
	if len(windows.closes[0].Windows) == 0 {
		t.Fatal("the close should carry the kind's windows")
	}
}

func TestApprovalWorkflowSendsTheOneHeldPromptWhenAWindowClosesOnIt(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	windows := &scriptedWindows{
		hold:     map[domain.ContactMethodKind]bool{domain.ContactSlack: true},
		closesAt: request.CreatedAt.Add(time.Minute),
		closed:   []domain.ApprovalRequest{request},
	}
	notifier, _ := runDigestWorkflow(t, windows, 0)

	if len(notifier.allDigests()) != 0 {
		t.Fatal("one request is a prompt, never a digest")
	}
	prompts := notifier.allInputs()
	if len(prompts) != 2 || prompts[0].Recipients[0].SlackUserID != "U0ANA" || len(prompts[0].Recipients[0].Emails) != 0 {
		t.Fatalf("the held Slack prompt should go out alone when its window closes: %#v", prompts)
	}
}

func TestApprovalWorkflowDecisionStandsDownAHeldWindow(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	windows := &scriptedWindows{
		hold:     map[domain.ContactMethodKind]bool{domain.ContactSlack: true, domain.ContactEmail: true},
		closesAt: request.CreatedAt.Add(3 * time.Minute),
	}
	notifier, _ := runDigestWorkflow(t, windows, 2*time.Minute)

	if windows.closeCount() != 0 || notifier.callCount() != 0 || len(notifier.allDigests()) != 0 {
		t.Fatalf("a decision before the window closes must stand the close down: closes=%d prompts=%d", windows.closeCount(), notifier.callCount())
	}
}

func TestApprovalWorkflowSendsAtOnceWhenTheWindowIsUnavailable(t *testing.T) {
	windows := &scriptedWindows{admitErr: errors.New("database unavailable")}
	notifier, _ := runDigestWorkflow(t, windows, 0)

	if notifier.callCount() != 2 || windows.closeCount() != 0 {
		t.Fatalf("an unavailable window must never cost a page: prompts=%d closes=%d", notifier.callCount(), windows.closeCount())
	}
}
