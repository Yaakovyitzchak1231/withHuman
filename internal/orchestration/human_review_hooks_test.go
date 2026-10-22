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

type recordingReviewHook struct {
	mu       sync.Mutex
	requests []domain.ID
	err      error
}

func (h *recordingReviewHook) HumanReviewRequested(_ context.Context, input application.HumanReviewRequestedInput) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, input.Request.ID)
	return h.err
}

func (h *recordingReviewHook) calls() []domain.ID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]domain.ID(nil), h.requests...)
}

func runHookedWorkflow(t *testing.T, request domain.ApprovalRequest, hook *recordingReviewHook) (*workflowRepository, *recordingNotifier) {
	t.Helper()
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, ladderResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt.Add(time.Minute)},
		WithHumanReviewHooks(hook))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	command := domain.DecisionCommand{OperationID: "operation-1", RequestID: request.ID, OrganizationID: request.OrganizationID, Status: domain.ApprovalApproved}
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(DecisionSignal, decisionSignal{Command: command})
	}, time.Minute)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	return repository, notifier
}

func TestApprovalWorkflowTellsHumanReviewHooksOnce(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	hook := &recordingReviewHook{}
	repository, notifier := runHookedWorkflow(t, request, hook)

	if calls := hook.calls(); len(calls) != 1 || calls[0] != request.ID {
		t.Fatalf("hook calls = %v, want one for %s", calls, request.ID)
	}
	if notifier.callCount() == 0 || repository.decided != 1 {
		t.Fatalf("hook changed the request's course: notifications=%d decisions=%d", notifier.callCount(), repository.decided)
	}
}

func TestApprovalWorkflowNotifiesAndDecidesWhileAHookFails(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	hook := &recordingReviewHook{err: errors.New("model unavailable")}
	repository, notifier := runHookedWorkflow(t, request, hook)

	if calls := hook.calls(); len(calls) != 2 {
		t.Fatalf("hook attempts = %d, want the budget of 2", len(calls))
	}
	if notifier.callCount() == 0 || repository.decided != 1 || repository.request.Status != domain.ApprovalApproved {
		t.Fatalf("a failing hook held the request: notifications=%d decisions=%d status=%s", notifier.callCount(), repository.decided, repository.request.Status)
	}
}

func TestApprovalWorkflowSkipsHumanReviewHooksAfterAPipelineDecision(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	request.Status = domain.ApprovalApproved
	request.Decision = &domain.Decision{
		ID: "decision-1", RequestID: request.ID, OrganizationID: request.OrganizationID,
		Source: domain.DecisionSourcePipeline, Status: domain.ApprovalApproved,
	}
	hook := &recordingReviewHook{}
	repository := &workflowRepository{request: request}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt},
		WithHumanReviewHooks(hook))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if calls := hook.calls(); len(calls) != 0 {
		t.Fatalf("hook ran for a request no person reviews: %v", calls)
	}
}

func TestRunHumanReviewHooksCallsEveryHookAndJoinsFailures(t *testing.T) {
	failing, passing := &recordingReviewHook{err: errors.New("down")}, &recordingReviewHook{}
	activities := NewActivities(&workflowRepository{}, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{},
		WithHumanReviewHooks(failing, passing))
	request := workflowRequest(time.Minute)

	err := activities.RunHumanReviewHooks(context.Background(), request)
	if err == nil || len(failing.calls()) != 1 || len(passing.calls()) != 1 {
		t.Fatalf("err=%v failing=%v passing=%v", err, failing.calls(), passing.calls())
	}
}
