package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
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

// summaryHook writes a summary the notification reader then serves, the
// way the hosted summarizer and its reader share a table.
type summaryHook struct {
	mu      sync.Mutex
	written map[domain.ID]string
}

func (h *summaryHook) HumanReviewRequested(_ context.Context, input application.HumanReviewRequestedInput) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.written[input.Request.ID] = "Refund $49.00 for a faulty bag"
	return nil
}

func (h *summaryHook) RequestSummaries(_ context.Context, input application.ReadRequestSummariesInput) (map[domain.ID]application.RequestSummary, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[domain.ID]application.RequestSummary{}
	for _, id := range input.RequestIDs {
		if text, ok := h.written[id]; ok {
			out[id] = application.RequestSummary{Text: text}
		}
	}
	return out, nil
}

func TestApprovalWorkflowFirstPromptCarriesTheHooksSummary(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	hook := &summaryHook{written: map[domain.ID]string{}}
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, ladderResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt.Add(time.Minute)},
		WithHumanReviewHooks(hook), WithNotificationSummaries(hook))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	// The hook takes a few seconds, as a model call does.
	environment.OnActivity(activities.RunHumanReviewHooks, mock.Anything, mock.Anything).After(3 * time.Second).Return(
		func(ctx context.Context, request domain.ApprovalRequest) error {
			return activities.RunHumanReviewHooks(ctx, request)
		})
	command := domain.DecisionCommand{OperationID: "operation-1", RequestID: request.ID, OrganizationID: request.OrganizationID, Status: domain.ApprovalApproved}
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(DecisionSignal, decisionSignal{Command: command})
	}, time.Minute)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	inputs := notifier.allInputs()
	if len(inputs) == 0 || inputs[0].Summary != "Refund $49.00 for a faulty bag" {
		t.Fatalf("first prompt = %+v, want it to carry the summary", inputs)
	}
}

func TestApprovalWorkflowPromptsWithoutASummaryAfterTheWait(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	hook := &summaryHook{written: map[domain.ID]string{}}
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, ladderResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt.Add(time.Minute)},
		WithHumanReviewHooks(hook), WithNotificationSummaries(hook))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	// A hook that takes far longer than the wait: the prompt goes out at
	// the wait's end without a summary, long before the decision.
	environment.OnActivity(activities.RunHumanReviewHooks, mock.Anything, mock.Anything).After(time.Hour).Return(nil)
	command := domain.DecisionCommand{OperationID: "operation-1", RequestID: request.ID, OrganizationID: request.OrganizationID, Status: domain.ApprovalApproved}
	environment.RegisterDelayedCallback(func() {
		if notifier.callCount() == 0 {
			t.Error("no prompt by 30 seconds: the wait outlasted humanReviewHookWait")
		}
		environment.SignalWorkflow(DecisionSignal, decisionSignal{Command: command})
	}, 30*time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if inputs := notifier.allInputs(); len(inputs) == 0 || inputs[0].Summary != "" {
		t.Fatalf("prompts = %+v, want one without a summary", inputs)
	}
}
