package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/loggermdc"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

type workflowRepository struct {
	request     domain.ApprovalRequest
	operation   domain.DecisionOperation
	loaded      int
	decided     int
	expired     int
	lapsed      []domain.ID
	lapseError  error
	suspension  *domain.PipelineSuspension
	resumed     int
	resumeInput application.ResumeApprovalInput
	reject      bool
	loadMDC     loggermdc.Fields
	decideMDC   loggermdc.Fields
	expireMDC   loggermdc.Fields
	lapseMDC    loggermdc.Fields
}

func (r *workflowRepository) InitializeApproval(_ context.Context, input application.InitializeApprovalInput) (domain.ApprovalRequest, error) {
	return input.Request, nil
}
func (r *workflowRepository) GetApproval(ctx context.Context, input application.GetApprovalInput) (domain.ApprovalRequest, error) {
	if r.request.OrganizationID != input.OrganizationID || r.request.ID != input.RequestID {
		return domain.ApprovalRequest{}, domain.ErrNotFound
	}
	r.loaded++
	r.loadMDC = loggermdc.From(ctx)
	return r.request, nil
}
func (r *workflowRepository) GetApprovalByIdempotencyKey(_ context.Context, input application.GetApprovalByIdempotencyKeyInput) (domain.ApprovalRequest, error) {
	if r.request.OrganizationID != input.OrganizationID || r.request.IdempotencyKey != input.IdempotencyKey {
		return domain.ApprovalRequest{}, domain.ErrNotFound
	}
	return r.request, nil
}
func (r *workflowRepository) ListApprovals(context.Context, application.ListApprovalsInput) ([]domain.ApprovalRequest, error) {
	return nil, nil
}
func (r *workflowRepository) CreateDecisionOperation(_ context.Context, input application.CreateDecisionOperationInput) (domain.DecisionOperation, error) {
	r.operation = input.Operation
	return input.Operation, nil
}
func (r *workflowRepository) GetDecisionOperation(_ context.Context, input application.GetDecisionOperationInput) (domain.DecisionOperation, error) {
	if r.operation.OrganizationID != input.OrganizationID || r.operation.ID != input.OperationID {
		return domain.DecisionOperation{}, domain.ErrNotFound
	}
	return r.operation, nil
}
func (r *workflowRepository) ApplyDecision(ctx context.Context, input application.ApplyDecisionInput) (domain.DecisionOperation, error) {
	r.decideMDC = loggermdc.From(ctx)
	command, now := input.Command, input.Now
	r.decided++
	if r.reject {
		r.operation = domain.DecisionOperation{ID: command.OperationID, RequestID: command.RequestID, OrganizationID: command.OrganizationID, Status: domain.OperationRejected, ErrorCode: "forbidden"}
		return r.operation, nil
	}
	decision := &domain.Decision{ID: "decision-1", RequestID: command.RequestID, OrganizationID: command.OrganizationID, Source: domain.DecisionSourceHuman, Status: command.Status, DecidedAt: now}
	r.operation = domain.DecisionOperation{ID: command.OperationID, RequestID: command.RequestID, OrganizationID: command.OrganizationID, Status: domain.OperationApplied, Decision: decision}
	r.request.Status, r.request.Decision = command.Status, decision
	return r.operation, nil
}
func (r *workflowRepository) ExpireApproval(ctx context.Context, input application.ExpireApprovalInput) (domain.ApprovalRequest, error) {
	r.expireMDC = loggermdc.From(ctx)
	organizationID, requestID, now := input.OrganizationID, input.RequestID, input.Now
	if r.request.Status.Terminal() {
		return r.request, nil
	}
	r.expired++
	r.request.Status = domain.ApprovalExpired
	r.request.Decision = &domain.Decision{ID: "expiration", RequestID: requestID, OrganizationID: organizationID, Source: domain.DecisionSourceSystem, Status: domain.ApprovalExpired, DecidedAt: now}
	return r.request, nil
}
func (r *workflowRepository) CancelApproval(_ context.Context, input application.CancelApprovalInput) (domain.ApprovalRequest, error) {
	if r.request.Status.Terminal() {
		return r.request, nil
	}
	r.request.Status = domain.ApprovalCancelled
	r.request.Decision = &domain.Decision{ID: "cancellation", RequestID: input.RequestID, OrganizationID: input.OrganizationID, Source: domain.DecisionSourceAgent, Status: domain.ApprovalCancelled, DecidedAt: input.Now}
	return r.request, nil
}
func (r *workflowRepository) GetPipelineSuspension(context.Context, application.GetPipelineSuspensionInput) (domain.PipelineSuspension, error) {
	if r.suspension == nil {
		return domain.PipelineSuspension{}, domain.ErrNotFound
	}
	return *r.suspension, nil
}
func (r *workflowRepository) ResumeApproval(_ context.Context, input application.ResumeApprovalInput) (domain.ApprovalRequest, error) {
	r.resumed++
	r.resumeInput = input
	if r.suspension != nil && r.suspension.ResumedAt == nil {
		now := input.Now
		r.suspension.ResumedAt = &now
		if input.Decision != nil && r.request.Status == domain.ApprovalPending {
			r.request.Status = input.Decision.Status
			r.request.Decision = input.Decision
		}
	}
	return r.request, nil
}
func (r *workflowRepository) ClaimApproval(context.Context, application.ClaimApprovalInput) (domain.Claim, error) {
	return domain.Claim{}, nil
}
func (r *workflowRepository) ReleaseClaim(context.Context, application.ReleaseClaimInput) (domain.ID, error) {
	return "", nil
}
func (r *workflowRepository) LapseClaim(ctx context.Context, input application.LapseClaimInput) error {
	r.lapseMDC = loggermdc.From(ctx)
	r.lapsed = append(r.lapsed, input.ClaimID)
	return r.lapseError
}
func (r *workflowRepository) CountPendingApprovals(context.Context, application.CountPendingApprovalsInput) (int64, error) {
	return 0, nil
}
func (r *workflowRepository) AddApprovalReviewer(context.Context, application.AddApprovalReviewerInput) (domain.ApprovalReviewer, error) {
	return domain.ApprovalReviewer{}, nil
}
func (r *workflowRepository) ListReviewerCandidates(context.Context, application.ListReviewerCandidatesInput) ([]application.ReviewerCandidate, error) {
	return nil, nil
}
func (r *workflowRepository) GetApprovalRoutingState(context.Context, application.GetApprovalRoutingStateInput) (application.ApprovalRoutingState, error) {
	return application.ApprovalRoutingState{}, nil
}
func (r *workflowRepository) OperationalMetrics(context.Context) (application.ApprovalMetrics, error) {
	return application.ApprovalMetrics{}, nil
}
func (r *workflowRepository) Health(context.Context) error { return nil }

type stubRecipientResolver struct {
	recipients application.ApprovalRecipients
}

func (r *stubRecipientResolver) ResolveApprovalRecipients(context.Context, application.ResolveApprovalRecipientsInput) (application.ApprovalRecipients, error) {
	return r.recipients, nil
}

func (r *stubRecipientResolver) ResolveLevelRecipients(context.Context, application.ResolveLevelRecipientsInput) (application.ApprovalRecipients, error) {
	return r.recipients, nil
}

// stubRuleRepository holds no ladders, so every recipient gets the
// defaults: the plan builder's own tests cover shaped ladders.
type stubRuleRepository struct{}

func (stubRuleRepository) ListNotificationRules(context.Context, application.ListNotificationRulesInput) ([]domain.NotificationRule, error) {
	return nil, nil
}
func (stubRuleRepository) ReplaceNotificationRules(context.Context, application.ReplaceNotificationRulesInput) ([]domain.NotificationRule, error) {
	return nil, nil
}
func (stubRuleRepository) ListRecipientRules(context.Context, application.ListRecipientRulesInput) (map[domain.ID][]domain.NotificationRule, error) {
	return map[domain.ID][]domain.NotificationRule{}, nil
}

type recordingNotifier struct {
	mu        sync.Mutex
	calls     int
	mdc       loggermdc.Fields
	lastInput application.SendApprovalPromptInput
	inputs    []application.SendApprovalPromptInput
}

func (n *recordingNotifier) SendApprovalPrompt(ctx context.Context, input application.SendApprovalPromptInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	n.mdc = loggermdc.From(ctx)
	n.lastInput = input
	n.inputs = append(n.inputs, input)
	return nil
}

func (n *recordingNotifier) allInputs() []application.SendApprovalPromptInput {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]application.SendApprovalPromptInput(nil), n.inputs...)
}

func (n *recordingNotifier) SendTransactional(context.Context, application.SendTransactionalInput) error {
	return nil
}

func (n *recordingNotifier) fields() loggermdc.Fields {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.mdc
}

func (n *recordingNotifier) callCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls
}

type workflowClock struct{ now time.Time }

func (c workflowClock) Now() time.Time { return c.now }

func TestApprovalWorkflowPersistsDecision(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	clock := workflowClock{now: request.CreatedAt.Add(time.Minute)}
	activities := NewActivities(repository, ladderResolver(), &stubRuleRepository{}, notifier, clock)
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	setWorkflowMDC(t, environment, loggermdc.Fields{
		loggermdc.KeyTraceID: "start-trace", loggermdc.KeyRequestID: "start-request",
		loggermdc.KeyOrganizationID: string(request.OrganizationID), loggermdc.KeyActorType: string(domain.ActorAgent),
		loggermdc.KeyActorID: "agent-1", loggermdc.KeyAgentInstanceID: "instance-1", loggermdc.Key("local-only"): "drop-me",
	})
	environment.RegisterActivity(activities)
	command := domain.DecisionCommand{OperationID: "operation-1", RequestID: request.ID, OrganizationID: request.OrganizationID, Status: domain.ApprovalApproved}
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(DecisionSignal, decisionSignal{Command: command, MDC: loggermdc.Fields{
			loggermdc.KeyTraceID: "decision-trace", loggermdc.KeyRequestID: "decision-request",
			loggermdc.KeyOrganizationID: string(command.OrganizationID), loggermdc.KeyApprovalRequestID: string(command.RequestID),
			loggermdc.KeyWorkflowID: WorkflowID(command.OrganizationID, command.RequestID), loggermdc.KeyActorType: string(domain.ActorHuman),
			loggermdc.KeyActorID: "human-1", loggermdc.KeyDecisionOperationID: string(command.OperationID),
		}})
	}, time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if repository.loaded != 2 || repository.decided != 1 || repository.expired != 0 {
		t.Fatalf("unexpected calls: loaded=%d decided=%d expired=%d", repository.loaded, repository.decided, repository.expired)
	}
	if repository.request.Status != domain.ApprovalApproved {
		t.Fatalf("got status %s", repository.request.Status)
	}
	if repository.loadMDC[loggermdc.KeyTraceID] != "start-trace" || notifier.fields()[loggermdc.KeyTraceID] != "start-trace" {
		t.Fatalf("start MDC did not reach load and notification: load=%#v notify=%#v", repository.loadMDC, notifier.fields())
	}
	if _, leaked := repository.loadMDC[loggermdc.Key("local-only")]; leaked {
		t.Fatalf("process-local field crossed Temporal: %#v", repository.loadMDC)
	}
	if repository.decideMDC[loggermdc.KeyTraceID] != "decision-trace" || repository.decideMDC[loggermdc.KeyDecisionOperationID] != "operation-1" {
		t.Fatalf("decision MDC did not override workflow MDC: %#v", repository.decideMDC)
	}
	if _, stale := repository.decideMDC[loggermdc.KeyAgentInstanceID]; stale {
		t.Fatalf("decision MDC retained the initiating agent instance: %#v", repository.decideMDC)
	}
}

func TestApprovalWorkflowExpiresAtAbsoluteDeadline(t *testing.T) {
	request := workflowRequest(2 * time.Second)
	repository := &workflowRepository{request: request}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.DeadlineAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	setWorkflowMDC(t, environment, loggermdc.Fields{loggermdc.KeyTraceID: "expiry-trace"})
	environment.RegisterActivity(activities)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if repository.expired != 1 || repository.request.Status != domain.ApprovalExpired {
		t.Fatalf("request did not expire: %#v", repository.request)
	}
	if repository.expireMDC[loggermdc.KeyTraceID] != "expiry-trace" {
		t.Fatalf("expiration MDC was not propagated: %#v", repository.expireMDC)
	}
}

func TestApprovalWorkflowCompletesWithoutNotificationAfterPipelineDecision(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	request.Status = domain.ApprovalApproved
	request.Decision = &domain.Decision{
		ID: "decision-1", RequestID: request.ID, OrganizationID: request.OrganizationID,
		Source: domain.DecisionSourcePipeline, Status: domain.ApprovalApproved,
	}
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if notifier.callCount() != 0 || repository.decided != 0 || repository.expired != 0 {
		t.Fatalf("terminal pipeline request continued into human review: notifications=%d decisions=%d expirations=%d", notifier.callCount(), repository.decided, repository.expired)
	}
}

func TestApprovalWorkflowKeepsWaitingAfterRejectedDecision(t *testing.T) {
	request := workflowRequest(5 * time.Second)
	repository := &workflowRepository{request: request, reject: true}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.DeadlineAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(DecisionSignal, decisionSignal{Command: domain.DecisionCommand{OperationID: "rejected", OrganizationID: request.OrganizationID, RequestID: request.ID}})
	}, time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if repository.request.Status != domain.ApprovalExpired {
		t.Fatalf("rejection incorrectly ended workflow: %s", repository.request.Status)
	}
}

func TestApprovalWorkflowClaimThenDecisionNeverLapses(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt.Add(time.Minute)})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(ClaimSignal, claimSignal{
			ClaimID: "claim-1", ExpiresAt: request.CreatedAt.Add(15 * time.Minute),
		})
	}, time.Second)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(DecisionSignal, decisionSignal{Command: domain.DecisionCommand{
			OperationID: "operation-1", RequestID: request.ID, OrganizationID: request.OrganizationID, Status: domain.ApprovalApproved,
		}})
	}, 2*time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if repository.request.Status != domain.ApprovalApproved || repository.expired != 0 {
		t.Fatalf("decision did not win: %s expired=%d", repository.request.Status, repository.expired)
	}
	if len(repository.lapsed) != 0 {
		t.Fatalf("decided claim lapsed anyway: %v", repository.lapsed)
	}
}

func TestApprovalWorkflowLapsesClaimAndKeepsWaiting(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.DeadlineAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	setWorkflowMDC(t, environment, loggermdc.Fields{loggermdc.KeyTraceID: "start-trace"})
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(ClaimSignal, claimSignal{
			ClaimID: "claim-1", ExpiresAt: request.CreatedAt.Add(30 * time.Second),
			MDC: loggermdc.Fields{loggermdc.KeyTraceID: "claim-trace"},
		})
	}, time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if len(repository.lapsed) != 1 || repository.lapsed[0] != "claim-1" {
		t.Fatalf("claim did not lapse exactly once: %v", repository.lapsed)
	}
	if repository.lapseMDC[loggermdc.KeyTraceID] != "claim-trace" {
		t.Fatalf("claim MDC did not reach the lapse activity: %#v", repository.lapseMDC)
	}
	if repository.expired != 1 || repository.request.Status != domain.ApprovalExpired {
		t.Fatalf("workflow did not keep waiting until the deadline: %#v", repository.request)
	}
}

func TestApprovalWorkflowDeadlineWinsOverClaimTimer(t *testing.T) {
	request := workflowRequest(time.Minute)
	repository := &workflowRepository{request: request}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.DeadlineAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(ClaimSignal, claimSignal{
			// The store caps expiry at the deadline; even an uncapped value
			// must not outlive the request.
			ClaimID: "claim-1", ExpiresAt: request.DeadlineAt.Add(time.Hour),
		})
	}, time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if repository.expired != 1 || repository.request.Status != domain.ApprovalExpired {
		t.Fatalf("deadline did not win: %#v", repository.request)
	}
	if len(repository.lapsed) != 0 {
		t.Fatalf("claim lapsed after the request expired: %v", repository.lapsed)
	}
}

func TestApprovalWorkflowReplacesClaimTimerOnReclaim(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.DeadlineAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(ClaimSignal, claimSignal{
			ClaimID: "claim-1", ExpiresAt: request.CreatedAt.Add(30 * time.Second),
		})
	}, time.Second)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(ClaimSignal, claimSignal{
			ClaimID: "claim-2", ExpiresAt: request.CreatedAt.Add(2 * time.Minute),
		})
	}, 2*time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if len(repository.lapsed) != 1 || repository.lapsed[0] != "claim-2" {
		t.Fatalf("superseded claim timer fired: %v", repository.lapsed)
	}
}

func TestApprovalWorkflowLapsesPastExpiryClaimImmediately(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.DeadlineAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(ClaimSignal, claimSignal{
			ClaimID: "claim-1", ExpiresAt: request.CreatedAt.Add(-time.Minute),
		})
	}, time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if len(repository.lapsed) != 1 || repository.lapsed[0] != "claim-1" {
		t.Fatalf("past-expiry claim did not lapse immediately: %v", repository.lapsed)
	}
	if repository.expired != 1 {
		t.Fatalf("workflow did not continue to the deadline: expired=%d", repository.expired)
	}
}

// Lapsing a claim is advisory bookkeeping. A store that keeps failing it
// must not end the workflow or hold up the request, or an unanswered
// request would never reach its deadline.
func TestApprovalWorkflowSurvivesFailingClaimLapse(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request, lapseError: errors.New("claims unavailable")}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.DeadlineAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(ClaimSignal, claimSignal{
			ClaimID: "claim-1", ExpiresAt: request.CreatedAt.Add(30 * time.Second),
		})
	}, time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("a failing claim lapse ended the workflow: %v", err)
	}
	if repository.expired != 1 || repository.request.Status != domain.ApprovalExpired {
		t.Fatalf("request never reached its deadline: %#v", repository.request)
	}
}

func setWorkflowMDC(t *testing.T, environment *testsuite.TestWorkflowEnvironment, fields loggermdc.Fields) {
	t.Helper()
	propagator := NewMDCPropagator()
	header := newTestHeader()
	ctx := loggermdc.With(context.Background(), fields)
	if err := propagator.Inject(ctx, header); err != nil {
		t.Fatal(err)
	}
	environment.SetContextPropagators([]workflow.ContextPropagator{propagator})
	environment.SetHeader(header.header)
}

func workflowRequest(timeout time.Duration) domain.ApprovalRequest {
	now := time.Now().UTC()
	return domain.ApprovalRequest{ID: "request-1", OrganizationID: "org-1", AgentID: "agent-1", AgentInstanceID: "instance-1", AgentName: "agent", AgentInstanceName: "runtime", AgentSlug: "release", Tool: "deploy", Arguments: []byte(`{"version":"1"}`), IdempotencyKey: "key-1", SemanticHash: "semantic-1", Status: domain.ApprovalPending, CreatedAt: now, UpdatedAt: now, DeadlineAt: now.Add(timeout)}
}

func TestPlanNotificationsFollowsUrgencyDefaults(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	resolver := &stubRecipientResolver{recipients: application.ApprovalRecipients{
		Outcome: domain.RoutingEscalated, Urgency: domain.NotificationInterrupt,
		Recipients: []application.NotificationRecipient{
			{MembershipID: "membership-1", UserID: "user-1", DisplayName: "Ana", Methods: []application.RecipientMethod{
				{ID: "", Kind: domain.ContactEmail, Address: "ana@example.com"},
				{ID: "slack-1", Kind: domain.ContactSlack, Address: "U0ANA"},
			}},
			{MembershipID: "membership-2", UserID: "user-2", DisplayName: "Ben", Methods: []application.RecipientMethod{
				{ID: "", Kind: domain.ContactEmail, Address: "ben@example.com"},
			}},
		},
	}}
	activities := NewActivities(&workflowRepository{request: request}, resolver, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestActivityEnvironment()
	environment.RegisterActivity(activities.PlanNotifications)

	result, err := environment.ExecuteActivity(activities.PlanNotifications, request)
	if err != nil {
		t.Fatal(err)
	}
	var plan []application.NotificationStep
	if err := result.Get(&plan); err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || plan[0].Delay != 0 || plan[1].Delay != DefaultInterruptEmailDelay {
		t.Fatalf("plan = %#v", plan)
	}
	if len(plan[0].Recipients) != 2 || plan[0].Recipients[0].SlackUserID != "U0ANA" || plan[0].Recipients[1].Emails[0] != "ben@example.com" {
		t.Fatalf("first step = %#v", plan[0].Recipients)
	}
	if len(plan[1].Recipients) != 1 || plan[1].Recipients[0].Emails[0] != "ana@example.com" {
		t.Fatalf("second step = %#v", plan[1].Recipients)
	}
}

// ladderResolver yields one linked reviewer on an interrupt, so the
// default ladder produces two steps: Slack at once, email after five
// minutes. The tests below check what stops the second step.
func ladderResolver() *stubRecipientResolver {
	return &stubRecipientResolver{recipients: application.ApprovalRecipients{
		Outcome: domain.RoutingEscalated, Urgency: domain.NotificationInterrupt,
		Recipients: []application.NotificationRecipient{
			{MembershipID: "membership-1", UserID: "user-1", DisplayName: "Ana", Methods: []application.RecipientMethod{
				{ID: "", Kind: domain.ContactEmail, Address: "ana@example.com"},
				{ID: "slack-1", Kind: domain.ContactSlack, Address: "U0ANA"},
			}},
		},
	}}
}

func TestApprovalWorkflowDeliversEveryRungWhileWaiting(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, ladderResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if notifier.callCount() != 2 {
		t.Fatalf("an undecided request should fire both rungs, got %d deliveries", notifier.callCount())
	}
	for _, input := range notifier.allInputs() {
		if input.Request.AgentID != request.AgentID {
			t.Fatalf("notification lost the agent ID across Temporal: got %q, want %q", input.Request.AgentID, request.AgentID)
		}
	}
	last := notifier.lastInput
	if len(last.Recipients) != 1 || last.Recipients[0].SlackUserID != "" || last.Recipients[0].Emails[0] != "ana@example.com" {
		t.Fatalf("second rung should be email only: %#v", last.Recipients)
	}
	if repository.expired != 1 {
		t.Fatalf("request did not expire after the ladder ran out: %#v", repository.request)
	}
}

func TestApprovalWorkflowDecisionStopsLaterRungs(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, ladderResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	command := domain.DecisionCommand{OperationID: "operation-1", RequestID: request.ID, OrganizationID: request.OrganizationID, Status: domain.ApprovalApproved}
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(DecisionSignal, decisionSignal{Command: command})
	}, 2*time.Minute)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if notifier.callCount() != 1 {
		t.Fatalf("a decision at two minutes should leave only the first rung, got %d deliveries", notifier.callCount())
	}
	if notifier.lastInput.Recipients[0].SlackUserID != "U0ANA" {
		t.Fatalf("first rung should be Slack: %#v", notifier.lastInput.Recipients)
	}
}

func TestApprovalWorkflowClaimStopsLaterRungs(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, ladderResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(ClaimSignal, claimSignal{ClaimID: "claim-1", ExpiresAt: request.CreatedAt.Add(4 * time.Minute)})
	}, time.Minute)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if notifier.callCount() != 1 {
		t.Fatalf("a claim should stand the later rung down even after it lapses, got %d deliveries", notifier.callCount())
	}
	if len(repository.lapsed) != 1 || repository.expired != 1 {
		t.Fatalf("claim did not lapse and request did not expire: lapsed=%v expired=%d", repository.lapsed, repository.expired)
	}
}

type stubPipeline struct {
	resumeInput application.ResumeApprovalInput
	resumeErr   error
	resumed     int
	abandoned   int
}

func (p *stubPipeline) Resume(_ context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension) (application.ResumeApprovalInput, error) {
	p.resumed++
	if p.resumeErr != nil {
		return application.ResumeApprovalInput{}, p.resumeErr
	}
	input := p.resumeInput
	input.OrganizationID, input.RequestID, input.EvaluationID = request.OrganizationID, request.ID, suspension.EvaluationID
	return input, nil
}

func (p *stubPipeline) Abandon(_ context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension, reasonCode string) (application.ResumeApprovalInput, error) {
	p.abandoned++
	return application.ResumeApprovalInput{OrganizationID: request.OrganizationID, RequestID: request.ID, EvaluationID: suspension.EvaluationID,
		AuditEvents: []domain.AuditEvent{{OrganizationID: request.OrganizationID, EventType: "request_approval_pipeline.completed", ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID, Data: []byte(`{"reason_code":"` + reasonCode + `"}`)}},
		Now:         time.Now()}, nil
}

type stubRouter struct{ evaluated int }

func (r *stubRouter) Evaluate(_ context.Context, request domain.ApprovalRequest, _ *application.HumanReviewRoute) (domain.ApprovalRequestRouting, domain.AuditEvent, bool) {
	r.evaluated++
	return domain.ApprovalRequestRouting{RequestID: request.ID, OrganizationID: request.OrganizationID, Outcome: domain.RoutingDefaultQueue, Reason: "test", Urgency: "standard"},
		domain.AuditEvent{OrganizationID: request.OrganizationID, EventType: "routing.evaluated", ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID, Data: []byte(`{}`)}, true
}

func suspendedWorkflowRequest(t *testing.T) (domain.ApprovalRequest, *workflowRepository) {
	t.Helper()
	request := workflowRequest(10 * time.Minute)
	return request, &workflowRepository{request: request, suspension: &domain.PipelineSuspension{
		RequestID: request.ID, OrganizationID: request.OrganizationID, EvaluationID: "evaluation-1",
		PinnedRevisions: map[string]domain.ID{domain.EntryPipelineKey: "pipeline-1"}, ResumePosition: 1, StartedAt: request.CreatedAt, SuspendedAt: request.CreatedAt,
	}}
}

func TestApprovalWorkflowResumesASuspendedPipelineAndEndsOnItsDecision(t *testing.T) {
	request, repository := suspendedWorkflowRequest(t)
	pipeline := &stubPipeline{resumeInput: application.ResumeApprovalInput{
		Decision:    &domain.Decision{ID: "decision-1", RequestID: request.ID, OrganizationID: request.OrganizationID, Source: domain.DecisionSourcePipeline, Status: domain.ApprovalApproved, DecidedAt: request.CreatedAt},
		AuditEvents: []domain.AuditEvent{{OrganizationID: request.OrganizationID, EventType: "request_approval_pipeline.completed", ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID, Data: []byte(`{}`)}},
		Now:         request.CreatedAt,
	}}
	router := &stubRouter{}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt}, WithPipeline(pipeline, router))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if pipeline.resumed != 1 || pipeline.abandoned != 0 || repository.resumed != 1 {
		t.Fatalf("unexpected resume calls: pipeline=%d abandoned=%d committed=%d", pipeline.resumed, pipeline.abandoned, repository.resumed)
	}
	if router.evaluated != 0 || repository.resumeInput.Routing != nil {
		t.Fatal("an automatic decision must not be routed")
	}
	if notifier.callCount() != 0 {
		t.Fatal("an automatic decision must not notify reviewers")
	}
	if repository.request.Status != domain.ApprovalApproved {
		t.Fatalf("got status %s", repository.request.Status)
	}
}

func TestApprovalWorkflowRoutesAndNotifiesAfterTheJudgeEscalates(t *testing.T) {
	request, repository := suspendedWorkflowRequest(t)
	pipeline := &stubPipeline{resumeInput: application.ResumeApprovalInput{
		AuditEvents: []domain.AuditEvent{{OrganizationID: request.OrganizationID, EventType: "request_approval_pipeline.completed", ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID, Data: []byte(`{}`)}},
		Now:         request.CreatedAt,
	}}
	router := &stubRouter{}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, emailResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt}, WithPipeline(pipeline, router))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(DecisionSignal, decisionSignal{Command: domain.DecisionCommand{OperationID: "operation-1", RequestID: request.ID, OrganizationID: request.OrganizationID, Status: domain.ApprovalDenied}})
	}, time.Minute)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if router.evaluated != 1 || repository.resumeInput.Routing == nil || repository.resumeInput.Routing.Outcome != domain.RoutingDefaultQueue {
		t.Fatalf("a human outcome must be routed at resume: %#v", repository.resumeInput.Routing)
	}
	if got := repository.resumeInput.AuditEvents[len(repository.resumeInput.AuditEvents)-1].EventType; got != "routing.evaluated" {
		t.Fatalf("routing evidence must follow the completion: %s", got)
	}
	if notifier.callCount() != 1 {
		t.Fatalf("reviewers must be notified once after the resume, got %d", notifier.callCount())
	}
	if repository.request.Status != domain.ApprovalDenied || repository.decided != 1 {
		t.Fatalf("the human decision must still apply: status=%s decided=%d", repository.request.Status, repository.decided)
	}
}

func TestApprovalWorkflowAbandonsAPipelineWhoseResumeKeepsFailing(t *testing.T) {
	request, repository := suspendedWorkflowRequest(t)
	pipeline := &stubPipeline{resumeErr: errors.New("compiled state unavailable")}
	router := &stubRouter{}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, emailResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt}, WithPipeline(pipeline, router))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if pipeline.resumed != pipelineResumeTries || pipeline.abandoned != 1 || repository.resumed != 1 {
		t.Fatalf("expected %d resume attempts then one abandon: resumed=%d abandoned=%d committed=%d", pipelineResumeTries, pipeline.resumed, pipeline.abandoned, repository.resumed)
	}
	if router.evaluated != 1 || repository.resumeInput.Routing == nil {
		t.Fatal("an abandoned pipeline must still be routed to a human")
	}
	if notifier.callCount() != 1 {
		t.Fatalf("reviewers must be notified after the abandon, got %d", notifier.callCount())
	}
	if repository.request.Status != domain.ApprovalExpired || repository.expired != 1 {
		t.Fatalf("the request must then wait to its deadline: status=%s expired=%d", repository.request.Status, repository.expired)
	}
}

func TestResumePipelineIsANoOpWithoutAnOpenSuspension(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	pipeline := &stubPipeline{}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt}, WithPipeline(pipeline, &stubRouter{}))
	resumed, err := activities.ResumePipeline(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if pipeline.resumed != 0 || repository.resumed != 0 || repository.loaded != 0 || resumed.ID != request.ID {
		t.Fatalf("a request without a suspension must pass through untouched: resumed=%d committed=%d loaded=%d", pipeline.resumed, repository.resumed, repository.loaded)
	}
	now := request.CreatedAt
	repository.suspension = &domain.PipelineSuspension{RequestID: request.ID, OrganizationID: request.OrganizationID, EvaluationID: "evaluation-1", PinnedRevisions: map[string]domain.ID{domain.EntryPipelineKey: "pipeline-1"}, ResumedAt: &now}
	if _, err := activities.ResumePipeline(context.Background(), request); err != nil || pipeline.resumed != 0 {
		t.Fatalf("a resumed suspension must not run again: err=%v resumed=%d", err, pipeline.resumed)
	}
}

// emailResolver yields one reviewer reachable on the account email only,
// so a standard request produces exactly one immediate step.
func emailResolver() *stubRecipientResolver {
	return &stubRecipientResolver{recipients: application.ApprovalRecipients{
		Outcome: domain.RoutingEscalated, Urgency: domain.NotificationStandard,
		Recipients: []application.NotificationRecipient{
			{MembershipID: "membership-1", UserID: "user-1", DisplayName: "Ana", Methods: []application.RecipientMethod{
				{ID: "", Kind: domain.ContactEmail, Address: "ana@example.com"},
			}},
		},
	}}
}

func TestApprovalWorkflowCancellationStopsRungsAndTimers(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	repository := &workflowRepository{request: request}
	notifier := &recordingNotifier{}
	activities := NewActivities(repository, ladderResolver(), &stubRuleRepository{}, notifier, workflowClock{now: request.CreatedAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	setWorkflowMDC(t, environment, loggermdc.Fields{loggermdc.KeyTraceID: "start-trace"})
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(ClaimSignal, claimSignal{ClaimID: "claim-1", ExpiresAt: request.CreatedAt.Add(4 * time.Minute)})
	}, 30*time.Second)
	environment.RegisterDelayedCallback(func() {
		// The store committed the cancellation before the signal was sent.
		if _, err := repository.CancelApproval(context.Background(), application.CancelApprovalInput{
			OrganizationID: request.OrganizationID, RequestID: request.ID, AgentInstanceID: request.AgentInstanceID,
			Auth: domain.AuthContext{Method: "bearer"}, Now: request.CreatedAt.Add(time.Minute),
		}); err != nil {
			t.Error(err)
		}
		environment.SignalWorkflow(CancelSignal, cancelSignal{MDC: loggermdc.Fields{loggermdc.KeyTraceID: "cancel-trace"}})
	}, time.Minute)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if repository.request.Status != domain.ApprovalCancelled || repository.expired != 0 {
		t.Fatalf("cancellation did not end the workflow cleanly: status=%s expired=%d", repository.request.Status, repository.expired)
	}
	if len(repository.lapsed) != 0 {
		t.Fatalf("the claim timer fired after the cancellation: %v", repository.lapsed)
	}
	if notifier.callCount() != 1 {
		t.Fatalf("a cancellation at one minute should leave only the first rung, got %d deliveries", notifier.callCount())
	}
	if repository.loadMDC[loggermdc.KeyTraceID] != "cancel-trace" {
		t.Fatalf("cancellation MDC was not propagated to the confirming load: %#v", repository.loadMDC)
	}
}

func TestApprovalWorkflowIgnoresCancelSignalForPendingRequest(t *testing.T) {
	request := workflowRequest(2 * time.Second)
	repository := &workflowRepository{request: request}
	activities := NewActivities(repository, &stubRecipientResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.DeadlineAt})
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivity(activities)
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(CancelSignal, cancelSignal{})
	}, time.Second)
	environment.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: request.OrganizationID, RequestID: request.ID})

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if repository.expired != 1 || repository.request.Status != domain.ApprovalExpired {
		t.Fatalf("a stray cancel signal ended a pending request: %#v", repository.request)
	}
}
