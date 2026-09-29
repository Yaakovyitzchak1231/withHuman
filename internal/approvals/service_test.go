package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
)

type approvalClock struct{ now time.Time }

func (c approvalClock) Now() time.Time { return c.now }

type approvalRepositoryStub struct {
	request         domain.ApprovalRequest
	releasedClaimID domain.ID
	marked          int
	listInput       application.ListApprovalsInput
	cancelInput     application.CancelApprovalInput
	getInput        application.GetApprovalInput
	routingState    application.ApprovalRoutingState
	routingErr      error
	addedInput      *application.AddApprovalReviewerInput
	addErr          error
	applied         []domain.DecisionCommand
}

// policyStub answers the organization policy from a fixed value.
type policyStub struct {
	policy domain.OrganizationPolicy
	reads  int
}

func (p *policyStub) GetOrganizationPolicy(_ context.Context, input application.GetOrganizationPolicyInput) (domain.OrganizationPolicy, error) {
	p.reads++
	p.policy.OrganizationID = input.OrganizationID
	return p.policy, nil
}

func defaultPolicy() *policyStub {
	return &policyStub{policy: domain.OrganizationPolicy{
		MFAMode: domain.MFAOptional, ApprovalReauthWindow: 24 * time.Hour, ClaimRetriggerWindow: 15 * time.Minute,
		UnroutedRequests: domain.UnroutedAnyReviewer, RequestVisibility: domain.VisibilityAllReaders,
	}}
}

func (r *approvalRepositoryStub) InitializeApproval(_ context.Context, input application.InitializeApprovalInput) (domain.ApprovalRequest, error) {
	r.request = input.Request
	r.request.Decision = input.Decision
	return r.request, nil
}
func (r *approvalRepositoryStub) GetApproval(_ context.Context, input application.GetApprovalInput) (domain.ApprovalRequest, error) {
	r.getInput = input
	if r.request.ID == "" {
		return domain.ApprovalRequest{}, domain.ErrNotFound
	}
	return r.request, nil
}
func (r *approvalRepositoryStub) AddApprovalReviewer(_ context.Context, input application.AddApprovalReviewerInput) (domain.ApprovalReviewer, error) {
	r.addedInput = &input
	if r.addErr != nil {
		return domain.ApprovalReviewer{}, r.addErr
	}
	return domain.ApprovalReviewer{RequestID: input.RequestID, MembershipID: input.MembershipID, AddedByMembershipID: input.Actor.MembershipID, CreatedAt: input.Now}, nil
}
func (r *approvalRepositoryStub) ListReviewerCandidates(context.Context, application.ListReviewerCandidatesInput) ([]application.ReviewerCandidate, error) {
	return nil, nil
}
func (r *approvalRepositoryStub) GetApprovalRoutingState(context.Context, application.GetApprovalRoutingStateInput) (application.ApprovalRoutingState, error) {
	return r.routingState, r.routingErr
}
func (r *approvalRepositoryStub) GetApprovalByIdempotencyKey(context.Context, application.GetApprovalByIdempotencyKeyInput) (domain.ApprovalRequest, error) {
	return domain.ApprovalRequest{}, domain.ErrNotFound
}
func (r *approvalRepositoryStub) ListApprovals(_ context.Context, input application.ListApprovalsInput) ([]domain.ApprovalRequest, error) {
	r.listInput = input
	return nil, nil
}
func (r *approvalRepositoryStub) CreateDecisionOperation(_ context.Context, input application.CreateDecisionOperationInput) (domain.DecisionOperation, error) {
	return input.Operation, nil
}
func (r *approvalRepositoryStub) GetDecisionOperation(context.Context, application.GetDecisionOperationInput) (domain.DecisionOperation, error) {
	return domain.DecisionOperation{}, nil
}
func (r *approvalRepositoryStub) ApplyDecision(_ context.Context, input application.ApplyDecisionInput) (domain.DecisionOperation, error) {
	r.applied = append(r.applied, input.Command)
	operation := domain.DecisionOperation{ID: input.Command.OperationID, OrganizationID: input.Command.OrganizationID, RequestID: input.Command.RequestID, Status: domain.OperationApplied}
	if r.request.Status.Terminal() {
		operation.Status, operation.ErrorCode = domain.OperationRejected, "already_terminal"
	}
	return operation, nil
}
func (r *approvalRepositoryStub) ExpireApproval(context.Context, application.ExpireApprovalInput) (domain.ApprovalRequest, error) {
	return domain.ApprovalRequest{}, nil
}
func (r *approvalRepositoryStub) CancelApproval(_ context.Context, input application.CancelApprovalInput) (domain.ApprovalRequest, error) {
	r.cancelInput = input
	if r.request.ID != input.RequestID {
		return domain.ApprovalRequest{}, domain.ErrNotFound
	}
	if r.request.AgentInstanceID != input.AgentInstanceID {
		return domain.ApprovalRequest{}, domain.ErrForbidden
	}
	if r.request.Status.Terminal() {
		return domain.ApprovalRequest{}, domain.ErrAlreadyTerminal
	}
	r.request.Status = domain.ApprovalCancelled
	return r.request, nil
}
func (r *approvalRepositoryStub) GetPipelineSuspension(context.Context, application.GetPipelineSuspensionInput) (domain.PipelineSuspension, error) {
	return domain.PipelineSuspension{}, domain.ErrNotFound
}
func (r *approvalRepositoryStub) ResumeApproval(context.Context, application.ResumeApprovalInput) (domain.ApprovalRequest, error) {
	return r.request, nil
}
func (r *approvalRepositoryStub) ClaimApproval(context.Context, application.ClaimApprovalInput) (domain.Claim, error) {
	return domain.Claim{}, nil
}
func (r *approvalRepositoryStub) ReleaseClaim(context.Context, application.ReleaseClaimInput) (domain.ID, error) {
	return r.releasedClaimID, nil
}
func (r *approvalRepositoryStub) LapseClaim(context.Context, application.LapseClaimInput) error {
	return nil
}
func (r *approvalRepositoryStub) CountPendingApprovals(context.Context, application.CountPendingApprovalsInput) (int64, error) {
	if r.request.ID != "" && r.request.Status == domain.ApprovalPending {
		return 1, nil
	}
	return 0, nil
}
func (r *approvalRepositoryStub) OperationalMetrics(context.Context) (application.ApprovalMetrics, error) {
	return application.ApprovalMetrics{}, nil
}
func (r *approvalRepositoryStub) Health(context.Context) error { return nil }

func (r *approvalRepositoryStub) ListApprovalWorkflowStarts(context.Context, application.ListApprovalWorkflowStartsInput) ([]application.ApprovalWorkflowStart, error) {
	return nil, nil
}

func (r *approvalRepositoryStub) MarkApprovalWorkflowStarted(context.Context, application.MarkApprovalWorkflowStartedInput) error {
	r.marked++
	return nil
}

type lifecycleStub struct {
	starts        int
	decisions     int
	cancellations int
	err           error
	decisionErr   error
}

func (o *lifecycleStub) StartApproval(context.Context, application.StartApprovalInput) error {
	o.starts++
	return o.err
}
func (o *lifecycleStub) SubmitDecision(context.Context, application.SubmitDecisionInput) error {
	o.decisions++
	return o.decisionErr
}
func (*lifecycleStub) SubmitAgentOwnerHandoff(context.Context, application.SubmitAgentOwnerHandoffInput) error {
	return nil
}

func (*lifecycleStub) SubmitClaimRelease(context.Context, application.SubmitClaimReleaseInput) error {
	return nil
}
func (*lifecycleStub) SubmitClaim(context.Context, application.SubmitClaimInput) error {
	return nil
}
func (o *lifecycleStub) SubmitCancellation(context.Context, application.SubmitCancellationInput) error {
	o.cancellations++
	return o.err
}
func (*lifecycleStub) Health(context.Context) error { return nil }

type evaluatorStub struct {
	status domain.ApprovalStatus
	calls  int
}

func (evaluator *evaluatorStub) Evaluate(_ context.Context, request domain.ApprovalRequest) (application.InitializeApprovalInput, error) {
	evaluator.calls++
	request.Status = evaluator.status
	if request.Status == "" {
		request.Status = domain.ApprovalPending
	}
	events := []domain.AuditEvent{{
		ID: "audit-1", OrganizationID: request.OrganizationID, EventType: "approval.created",
		ActorType: domain.ActorAgent, ActorID: request.AgentID, SubjectType: "approval_request",
		SubjectID: request.ID, Data: []byte(`{}`), OccurredAt: request.CreatedAt,
	}, {
		ID: "audit-2", OrganizationID: request.OrganizationID, EventType: "request_approval_pipeline.completed",
		ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID,
		Data: []byte(`{"outcome":"human","reason_code":"no_active_revision"}`), OccurredAt: request.CreatedAt,
	}}
	initialization := application.InitializeApprovalInput{Request: request, AuditEvents: events}
	if request.Status.Terminal() {
		decision := &domain.Decision{
			ID: "decision-1", RequestID: request.ID, OrganizationID: request.OrganizationID,
			Source: domain.DecisionSourcePipeline, Status: request.Status, Channel: "pipeline",
			Auth:           domain.AuthContext{Method: "pipeline", Assurance: "automatic", AuthenticatedAt: request.CreatedAt},
			IdempotencyKey: "pipeline:evaluation-1", DecidedAt: request.CreatedAt,
		}
		initialization.Decision = decision
		initialization.AuditEvents = append(initialization.AuditEvents[:1], domain.AuditEvent{
			ID: "audit-start", OrganizationID: request.OrganizationID, EventType: "request_approval_pipeline.started",
			ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID, Data: []byte(`{}`), OccurredAt: request.CreatedAt,
		}, initialization.AuditEvents[1], domain.AuditEvent{
			ID: "audit-decided", OrganizationID: request.OrganizationID, EventType: "approval.decided",
			ActorType: domain.ActorSystem, SubjectType: "approval_request", SubjectID: request.ID, Data: []byte(`{}`), OccurredAt: request.CreatedAt,
		})
	}
	return initialization, nil
}

func TestCreateDerivesAgentIdentityFromCredentialPrincipal(t *testing.T) {
	now := time.Unix(40_000, 0).UTC()
	repository := &approvalRepositoryStub{}
	evaluator := &evaluatorStub{}
	lifecycle := &lifecycleStub{}
	service := NewService(repository, repository, defaultPolicy(), evaluator, nil, lifecycle, entitlements.NewUnlimited(), approvalClock{now})
	principal := domain.Principal{
		ActorType: domain.ActorAgent, ActorID: "agent-1", AgentInstanceID: "instance-1", OrganizationID: "org-1",
		AgentSlug: "release",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestCreate, domain.PermissionRequestRead}))

	request, err := service.Create(context.Background(), principal, CreateInput{
		Tool: "deploy", Arguments: json.RawMessage(`{"version":"1"}`), AgentReasoning: "Deploying version 1 as requested",
		Timeout: time.Minute, IdempotencyKey: "request-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.AgentID != "agent-1" || request.AgentInstanceID != "instance-1" {
		t.Fatalf("request identity was not derived from the principal: %#v", request)
	}
	if request.AgentSlug != "release" {
		t.Fatalf("request agent type was not derived from the principal: %#v", request)
	}
	if lifecycle.starts != 1 || repository.marked != 1 {
		t.Fatalf("pending request did not start human review: starts=%d marked=%d", lifecycle.starts, repository.marked)
	}
}

func TestCreateAutomaticDecisionDoesNotUseTemporalOrPendingCapacity(t *testing.T) {
	now := time.Unix(40_500, 0).UTC()
	repository := &approvalRepositoryStub{}
	evaluator := &evaluatorStub{status: domain.ApprovalApproved}
	lifecycle := &lifecycleStub{err: errors.New("temporal unavailable")}
	service := NewService(repository, repository, defaultPolicy(), evaluator, nil, lifecycle, limitedEntitlements("org-1", application.DimensionPendingApprovals, 0), approvalClock{now})
	principal := domain.Principal{
		ActorType: domain.ActorAgent, ActorID: "agent-1", AgentInstanceID: "instance-1", OrganizationID: "org-1",
		AgentSlug: "release",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestCreate}))

	request, err := service.Create(context.Background(), principal, CreateInput{
		Tool: "deploy", Arguments: json.RawMessage(`{"version":"1"}`), Timeout: time.Minute, IdempotencyKey: "automatic-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != domain.ApprovalApproved || request.Decision == nil {
		t.Fatalf("automatic result was not committed: %#v", request)
	}
	if lifecycle.starts != 0 || repository.marked != 0 {
		t.Fatalf("automatic result touched human orchestration: starts=%d marked=%d", lifecycle.starts, repository.marked)
	}
}

func TestCreatePendingRequestSurvivesTemporalOutage(t *testing.T) {
	now := time.Unix(40_750, 0).UTC()
	repository := &approvalRepositoryStub{}
	lifecycle := &lifecycleStub{err: errors.New("temporal unavailable")}
	service := NewService(repository, repository, defaultPolicy(), &evaluatorStub{}, nil, lifecycle, entitlements.NewUnlimited(), approvalClock{now})
	principal := domain.Principal{
		ActorType: domain.ActorAgent, ActorID: "agent-1", AgentInstanceID: "instance-1", OrganizationID: "org-1",
		AgentSlug: "release",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestCreate}))

	request, err := service.Create(context.Background(), principal, CreateInput{
		Tool: "deploy", Arguments: json.RawMessage(`{"version":"1"}`), Timeout: time.Minute, IdempotencyKey: "pending-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != domain.ApprovalPending {
		t.Fatalf("Temporal outage changed pending fail-safe result: %#v", request)
	}
	if lifecycle.starts != 1 || repository.marked != 0 {
		t.Fatalf("failed workflow start was acknowledged: starts=%d marked=%d", lifecycle.starts, repository.marked)
	}
}

func TestApprovalFingerprintCanonicalizesJSON(t *testing.T) {
	first := approvalFingerprint("agent-1", "release", "deploy", "", json.RawMessage(`{"a":1,"b":2}`), "Deploy", nil, "1m")
	second := approvalFingerprint("agent-1", "release", "deploy", "", json.RawMessage(`{ "b": 2, "a": 1 }`), "Deploy", json.RawMessage(`null`), "1m")
	if first != second {
		t.Fatal("semantically identical requests produced different fingerprints")
	}
}

func TestApprovalFingerprintPreservesExactNumbers(t *testing.T) {
	fingerprint := func(raw string) string {
		return approvalFingerprint("agent", "test", "tool", "", json.RawMessage(raw), "", nil, "300s")
	}
	if fingerprint(`{"amount":1e3,"fraction":0.50,"zero":-0}`) != fingerprint(`{"amount":1000,"fraction":0.5,"zero":0}`) {
		t.Fatal("JSONB number spelling breaks idempotency")
	}
	if fingerprint(`{"id":9007199254740993}`) == fingerprint(`{"id":9007199254740992}`) {
		t.Fatal("large integers lost precision")
	}
	if fingerprint(`{"id":1}`) == fingerprint(`{"id":["number","1","0"]}`) {
		t.Fatal("user value impersonates a canonical number")
	}
}

func TestDecisionFingerprintIncludesOutcomeAndContext(t *testing.T) {
	base := decisionFingerprint(domain.ApprovalApproved, "looks good", "web", false)
	variants := []string{
		decisionFingerprint(domain.ApprovalDenied, "looks good", "web", false),
		decisionFingerprint(domain.ApprovalApproved, "different note", "web", false),
		decisionFingerprint(domain.ApprovalApproved, "looks good", "mobile", false),
		decisionFingerprint(domain.ApprovalApproved, "looks good", "web", true),
	}
	for index, variant := range variants {
		if base == variant {
			t.Fatalf("variant %d was not distinguished", index)
		}
	}
}

func TestApprovalFingerprintIncludesConsequentialFields(t *testing.T) {
	base := approvalFingerprint("agent-1", "release", "deploy", "", json.RawMessage(`{"version":"1"}`), "Deploy", nil, "1m")
	variants := []string{
		approvalFingerprint("agent-2", "release", "deploy", "", json.RawMessage(`{"version":"1"}`), "Deploy", nil, "1m"),
		approvalFingerprint("agent-1", "support", "deploy", "", json.RawMessage(`{"version":"1"}`), "Deploy", nil, "1m"),
		approvalFingerprint("agent-1", "release", "deploy", "", json.RawMessage(`{"version":"2"}`), "Deploy", nil, "1m"),
		approvalFingerprint("agent-1", "release", "deploy", "", json.RawMessage(`{"version":"1"}`), "Different", nil, "1m"),
		approvalFingerprint("agent-1", "release", "deploy", "", json.RawMessage(`{"version":"1"}`), "Deploy", nil, "2m"),
	}
	for index, variant := range variants {
		if base == variant {
			t.Fatalf("variant %d was not distinguished", index)
		}
	}
}

func TestCreateEnforcesPendingApprovalLimit(t *testing.T) {
	now := time.Unix(41_000, 0).UTC()
	repository := &approvalRepositoryStub{}
	service := NewService(repository, repository, defaultPolicy(), &evaluatorStub{}, nil, &lifecycleStub{}, limitedEntitlements("org-1", application.DimensionPendingApprovals, 1), approvalClock{now})
	principal := domain.Principal{
		ActorType: domain.ActorAgent, ActorID: "agent-1", AgentInstanceID: "instance-1", OrganizationID: "org-1",
		AgentSlug: "release",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestCreate}))
	input := CreateInput{
		Tool: "deploy", Arguments: json.RawMessage(`{"version":"1"}`), AgentReasoning: "Deploying version 1 as requested",
		Timeout: time.Minute, IdempotencyKey: "request-1",
	}

	if _, err := service.Create(context.Background(), principal, input); err != nil {
		t.Fatalf("create under the limit failed: %v", err)
	}

	input.IdempotencyKey = "request-2"
	if _, err := service.Create(context.Background(), principal, input); !errors.Is(err, domain.ErrLimitExceeded) {
		t.Fatalf("create at the limit returned %v, want domain.ErrLimitExceeded", err)
	}
}

func TestListIsTheHumanReviewQueue(t *testing.T) {
	repository := &approvalRepositoryStub{}
	service := NewService(repository, repository, defaultPolicy(), &evaluatorStub{}, nil, &lifecycleStub{}, entitlements.NewUnlimited(), approvalClock{time.Unix(50_000, 0).UTC()})

	reviewer := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead}))
	if _, err := service.List(context.Background(), reviewer, application.ListApprovalsInput{Limit: 25}); err != nil {
		t.Fatalf("queue listing by a reviewer failed: %v", err)
	}
	if !repository.listInput.HumanReviewOnly || repository.listInput.OrganizationID != "org-1" || repository.listInput.Limit != 25 {
		t.Fatalf("queue listing did not exclude automatic decisions: %#v", repository.listInput)
	}
	// One agent's history is everything it asked for, automatic
	// decisions included.
	if _, err := service.List(context.Background(), reviewer, application.ListApprovalsInput{Limit: 10, AgentSlug: "release"}); err != nil {
		t.Fatalf("agent listing by a reviewer failed: %v", err)
	}
	if repository.listInput.HumanReviewOnly || repository.listInput.AgentSlug != "release" {
		t.Fatalf("agent listing did not scope to the agent: %#v", repository.listInput)
	}
}

// limitedEntitlements fixes one dimension's limit for one organization
// and leaves everything else unlimited.
func limitedEntitlements(organizationID string, dimension application.Dimension, value int64) *entitlements.Fixed {
	fixed := entitlements.NewFixed()
	fixed.SetLimit(organizationID, dimension, application.LimitValue{Value: value})
	return fixed
}

func TestCancelIsTheRequestingInstanceOnly(t *testing.T) {
	now := time.Unix(41_000, 0).UTC()
	repository := &approvalRepositoryStub{request: domain.ApprovalRequest{
		ID: "request-1", OrganizationID: "org-1", AgentID: "agent-1", AgentInstanceID: "instance-1", Status: domain.ApprovalPending,
	}}
	lifecycle := &lifecycleStub{}
	service := NewService(repository, repository, defaultPolicy(), &evaluatorStub{}, nil, lifecycle, entitlements.NewUnlimited(), approvalClock{now})
	agent := domain.Principal{
		ActorType: domain.ActorAgent, ActorID: "agent-1", AgentInstanceID: "instance-1", OrganizationID: "org-1",
		AgentSlug: "release",
		Auth:      domain.AuthContext{Method: "bearer", Assurance: "machine", AuthenticatedAt: now, CredentialID: "credential-1"},
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestCreate}))

	for name, principal := range map[string]domain.Principal{
		"human": domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1"}.
			WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestCreate, domain.PermissionRequestDecide})),
		"agent without create": domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", AgentInstanceID: "instance-1", OrganizationID: "org-1"}.
			WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead})),
	} {
		if _, err := service.Cancel(context.Background(), principal, "request-1"); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%s cancel error = %v, want forbidden", name, err)
		}
	}
	otherInstance := agent
	otherInstance.AgentInstanceID = "instance-2"
	if _, err := service.Cancel(context.Background(), otherInstance, "request-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("another instance's cancel error = %v, want forbidden", err)
	}
	if lifecycle.cancellations != 0 {
		t.Fatalf("a refused cancel signalled the workflow %d times", lifecycle.cancellations)
	}

	request, err := service.Cancel(context.Background(), agent, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != domain.ApprovalCancelled || lifecycle.cancellations != 1 {
		t.Fatalf("cancel result = %s, signals = %d", request.Status, lifecycle.cancellations)
	}
	if repository.cancelInput.Auth.CredentialID != "credential-1" || !repository.cancelInput.Now.Equal(now) {
		t.Fatalf("cancellation provenance was not passed to the store: %#v", repository.cancelInput)
	}
	if _, err := service.Cancel(context.Background(), agent, "request-1"); !errors.Is(err, domain.ErrAlreadyTerminal) {
		t.Fatalf("terminal cancel error = %v, want already terminal", err)
	}
}

func TestCancelSurvivesTemporalOutage(t *testing.T) {
	now := time.Unix(41_500, 0).UTC()
	repository := &approvalRepositoryStub{request: domain.ApprovalRequest{
		ID: "request-1", OrganizationID: "org-1", AgentID: "agent-1", AgentInstanceID: "instance-1", Status: domain.ApprovalPending,
	}}
	lifecycle := &lifecycleStub{err: errors.New("temporal unavailable")}
	service := NewService(repository, repository, defaultPolicy(), &evaluatorStub{}, nil, lifecycle, entitlements.NewUnlimited(), approvalClock{now})
	agent := domain.Principal{
		ActorType: domain.ActorAgent, ActorID: "agent-1", AgentInstanceID: "instance-1", OrganizationID: "org-1",
		Auth: domain.AuthContext{Method: "bearer"},
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestCreate}))
	request, err := service.Cancel(context.Background(), agent, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != domain.ApprovalCancelled || lifecycle.cancellations != 1 {
		t.Fatalf("Temporal outage changed the committed cancellation: %s signals=%d", request.Status, lifecycle.cancellations)
	}
}

func TestDecideOnAClosedWorkflowIsRejectedAsTerminal(t *testing.T) {
	now := time.Unix(41_700, 0).UTC()
	repository := &approvalRepositoryStub{request: domain.ApprovalRequest{
		ID: "request-1", OrganizationID: "org-1", AgentID: "agent-1", AgentInstanceID: "instance-1", Status: domain.ApprovalCancelled,
	}}
	lifecycle := &lifecycleStub{decisionErr: fmt.Errorf("%w: workflow execution already finished", application.ErrWorkflowClosed)}
	service := NewService(repository, repository, defaultPolicy(), &evaluatorStub{}, nil, lifecycle, entitlements.NewUnlimited(), approvalClock{now})
	reviewer := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1"}.
		WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestDecide}))

	operation, err := service.Decide(context.Background(), reviewer, "request-1", DecideInput{Status: domain.ApprovalApproved, IdempotencyKey: "too-late"})
	if !errors.Is(err, domain.ErrAlreadyTerminal) {
		t.Fatalf("decide error = %v, want already terminal", err)
	}
	if lifecycle.decisions != 1 || len(repository.applied) != 1 || repository.applied[0].OperationID != operation.ID {
		t.Fatalf("closed workflow decision: signals=%d applied=%#v operation=%s", lifecycle.decisions, repository.applied, operation.ID)
	}
	if operation.Status != domain.OperationRejected {
		t.Fatalf("operation status = %s, want rejected", operation.Status)
	}
}

func TestListAndGetNarrowToTheReaderUnderRoutedOnly(t *testing.T) {
	repository := &approvalRepositoryStub{}
	repository.request = domain.ApprovalRequest{ID: "request-1", OrganizationID: "org-1", AgentID: "agent-1", Status: domain.ApprovalPending}
	policy := defaultPolicy()
	policy.policy.RequestVisibility = domain.VisibilityRoutedOnly
	service := NewService(repository, repository, policy, &evaluatorStub{}, nil, &lifecycleStub{}, entitlements.NewUnlimited(), approvalClock{time.Unix(50_000, 0).UTC()})

	reviewer := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead}))
	if _, err := service.List(context.Background(), reviewer, application.ListApprovalsInput{Limit: 25}); err != nil {
		t.Fatal(err)
	}
	if repository.listInput.VisibleTo != "membership-1" {
		t.Fatalf("routed_only listing was not narrowed to the reader: %#v", repository.listInput)
	}
	if _, err := service.Get(context.Background(), reviewer, "request-1"); err != nil {
		t.Fatal(err)
	}
	if repository.getInput.VisibleTo != "membership-1" {
		t.Fatalf("routed_only read was not narrowed to the reader: %#v", repository.getInput)
	}

	// Owners and auditors see everything: anyone holding
	// request.read.unrouted.
	auditor := reviewer.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead, domain.PermissionRequestReadUnrouted}))
	if _, err := service.List(context.Background(), auditor, application.ListApprovalsInput{Limit: 25}); err != nil {
		t.Fatal(err)
	}
	if repository.listInput.VisibleTo != "" {
		t.Fatalf("an auditor's listing was narrowed: %#v", repository.listInput)
	}

	// An agent reads its own request without any policy read at all.
	reads := policy.reads
	agent := domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", OrganizationID: "org-1"}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead}))
	if _, err := service.Get(context.Background(), agent, "request-1"); err != nil {
		t.Fatal(err)
	}
	if repository.getInput.VisibleTo != "" || policy.reads != reads {
		t.Fatalf("an agent read consulted the visibility policy: %#v reads=%d", repository.getInput, policy.reads-reads)
	}

	// Under all_readers nothing is narrowed.
	policy.policy.RequestVisibility = domain.VisibilityAllReaders
	if _, err := service.List(context.Background(), reviewer, application.ListApprovalsInput{Limit: 25}); err != nil {
		t.Fatal(err)
	}
	if repository.listInput.VisibleTo != "" {
		t.Fatalf("all_readers listing was narrowed: %#v", repository.listInput)
	}
}

// The queue's layers are the reader's: notified is their own records
// whatever the visibility policy, decidable is the routing rule the
// decision applies (empty outright for someone who may not decide), and
// visible narrows nothing beyond the policy.
func TestListScopesTheQueueToTheReadersLayer(t *testing.T) {
	repository := &approvalRepositoryStub{}
	policy := defaultPolicy()
	service := NewService(repository, repository, policy, &evaluatorStub{}, nil, &lifecycleStub{}, entitlements.NewUnlimited(), approvalClock{time.Unix(50_000, 0).UTC()})
	reader := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead}))
	list := func(principal domain.Principal, scope application.QueueScope) []domain.ApprovalRequest {
		t.Helper()
		repository.listInput = application.ListApprovalsInput{}
		requests, err := service.List(context.Background(), principal, application.ListApprovalsInput{Limit: 25, Scope: scope})
		if err != nil {
			t.Fatalf("List(%s): %v", scope, err)
		}
		return requests
	}

	list(reader, application.QueueScopeNotified)
	if repository.listInput.NotifiedTo != "membership-1" || repository.listInput.VisibleTo != "" {
		t.Fatalf("notified layer = %#v", repository.listInput)
	}
	if requests := list(reader, application.QueueScopeDecidable); len(requests) != 0 || repository.listInput.OrganizationID != "" {
		t.Fatalf("a reader without request.decide reached the repository: %v %#v", requests, repository.listInput)
	}

	reviewer := reader.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead, domain.PermissionRequestDecide}))
	list(reviewer, application.QueueScopeDecidable)
	if repository.listInput.VisibleTo != "membership-1" || repository.listInput.HideUnrouted || repository.listInput.NotifiedTo != "" {
		t.Fatalf("decidable layer under any_reviewer = %#v", repository.listInput)
	}
	policy.policy.UnroutedRequests = domain.UnroutedBreakGlassOnly
	list(reviewer, application.QueueScopeDecidable)
	if repository.listInput.VisibleTo != "membership-1" || !repository.listInput.HideUnrouted {
		t.Fatalf("decidable layer under break_glass_only = %#v", repository.listInput)
	}
	owner := reader.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead, domain.PermissionRequestDecide, domain.PermissionRequestDecideUnrouted}))
	list(owner, application.QueueScopeDecidable)
	if repository.listInput.VisibleTo != "membership-1" || repository.listInput.HideUnrouted {
		t.Fatalf("a break-glass holder's decidable layer hid unrouted requests: %#v", repository.listInput)
	}
	list(reviewer, application.QueueScopeVisible)
	if repository.listInput.VisibleTo != "" || repository.listInput.NotifiedTo != "" || repository.listInput.HideUnrouted {
		t.Fatalf("visible layer narrowed the listing: %#v", repository.listInput)
	}
	// Under routed_only the visible layer is already the reader's routing,
	// and the notified layer stays inside it.
	policy.policy.RequestVisibility = domain.VisibilityRoutedOnly
	list(reviewer, application.QueueScopeNotified)
	if repository.listInput.VisibleTo != "membership-1" || repository.listInput.NotifiedTo != "membership-1" {
		t.Fatalf("notified layer under routed_only = %#v", repository.listInput)
	}
}

func TestAddReviewerRequiresAHumanReviewerAndHandsTheRestToTheRepository(t *testing.T) {
	now := time.Unix(51_000, 0).UTC()
	repository := &approvalRepositoryStub{}
	service := NewService(repository, repository, defaultPolicy(), &evaluatorStub{}, nil, &lifecycleStub{}, entitlements.NewUnlimited(), approvalClock{now})

	reviewer := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead, domain.PermissionRequestDecide}))
	added, err := service.AddReviewer(context.Background(), reviewer, "request-1", "membership-2")
	if err != nil {
		t.Fatal(err)
	}
	if added.MembershipID != "membership-2" || added.AddedByMembershipID != "membership-1" {
		t.Fatalf("unexpected reviewer: %#v", added)
	}
	input := repository.addedInput
	if input == nil || input.OrganizationID != "org-1" || input.RequestID != "request-1" || input.MembershipID != "membership-2" ||
		input.Actor.MembershipID != "membership-1" || !input.Now.Equal(now) {
		t.Fatalf("repository input = %#v", input)
	}

	// The repository's answer is the routing rule's: not targeted is forbidden.
	repository.addErr = domain.ErrForbidden
	if _, err := service.AddReviewer(context.Background(), reviewer, "request-1", "membership-2"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("repository refusal was not passed through: %v", err)
	}
	repository.addErr = nil

	repository.addedInput = nil
	if _, err := service.AddReviewer(context.Background(), reviewer, "request-1", " "); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("blank membership: %v, want invalid", err)
	}
	reader := reviewer.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead}))
	if _, err := service.AddReviewer(context.Background(), reader, "request-1", "membership-2"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("without request.decide: %v, want forbidden", err)
	}
	agent := domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", OrganizationID: "org-1"}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestDecide}))
	if _, err := service.AddReviewer(context.Background(), agent, "request-1", "membership-2"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("by an agent: %v, want forbidden", err)
	}
	if repository.addedInput != nil {
		t.Fatal("a refused call reached the repository")
	}
}

func TestOperationErrorCarriesTheRoutingRejectionCode(t *testing.T) {
	for _, code := range []string{"not_targeted", "break_glass_only", "outside_routing_required"} {
		err := operationError(domain.DecisionOperation{Status: domain.OperationRejected, ErrorCode: code})
		var rejection RoutingRejection
		if !errors.As(err, &rejection) || rejection.Code != code || !errors.Is(err, domain.ErrNotTargeted) {
			t.Fatalf("%s: %v", code, err)
		}
	}
}
