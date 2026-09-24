package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/loggermdc"
	"go.temporal.io/sdk/activity"
)

type Activities struct {
	approvals     application.ApprovalRepository
	recipients    application.RoutingRecipientResolver
	rules         application.NotificationRuleRepository
	notifications application.NotificationDispatcher
	clock         application.Clock
	pipeline      PipelineResumer
	router        RoutingEvaluator
	escalation    application.EscalationRepository
	notified      application.ApprovalNotificationRecorder
}

// PipelineResumer runs the asynchronous segment of a suspended pipeline
// without persisting; the activity commits what it returns.
type PipelineResumer interface {
	Resume(ctx context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension) (application.ResumeApprovalInput, error)
	Abandon(ctx context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension, reasonCode string) (application.ResumeApprovalInput, error)
}

// RoutingEvaluator pins where a request that still needs a human goes,
// from the reviewers the pipeline resolved for it.
type RoutingEvaluator interface {
	Evaluate(ctx context.Context, request domain.ApprovalRequest, route *application.HumanReviewRoute) (domain.ApprovalRequestRouting, domain.AuditEvent, bool)
}

// ActivityOption supplies an optional collaborator. Options are functions
// rather than methods because Temporal registers every exported method of
// the activities struct as an activity.
type ActivityOption func(*Activities)

// WithPipeline enables resuming suspended pipelines. Without it a suspended
// request is abandoned to a human, never left waiting.
func WithPipeline(pipeline PipelineResumer, router RoutingEvaluator) ActivityOption {
	return func(a *Activities) {
		a.pipeline = pipeline
		a.router = router
	}
}

// WithNotificationRecords remembers who each plan addressed, which the
// review queue's default layer lists. Without it the queue's notified
// layer stays empty for requests this worker pages.
func WithNotificationRecords(recorder application.ApprovalNotificationRecorder) ActivityOption {
	return func(a *Activities) {
		a.notified = recorder
	}
}

// recordNotified writes the people a plan addressed, before reachability
// thins them: someone with no channel set up is still being asked, and
// the queue is where they find the request.
func (a *Activities) recordNotified(ctx context.Context, request domain.ApprovalRequest, recipients application.ApprovalRecipients) error {
	if a.notified == nil {
		return nil
	}
	return a.notified.RecordApprovalNotifications(ctx, application.RecordApprovalNotificationsInput{
		OrganizationID: request.OrganizationID, RequestID: request.ID,
		MembershipIDs: recipients.MembershipIDs(), Now: a.clock.Now(),
	})
}

func NewActivities(approvals application.ApprovalRepository, recipients application.RoutingRecipientResolver, rules application.NotificationRuleRepository, notifications application.NotificationDispatcher, clock application.Clock, options ...ActivityOption) *Activities {
	activities := &Activities{approvals: approvals, recipients: recipients, rules: rules, notifications: notifications, clock: clock}
	for _, option := range options {
		if option != nil {
			option(activities)
		}
	}
	return activities
}

// ResumePipeline runs the asynchronous segment of a suspended pipeline and
// commits its outcome, routing the request when it still needs a human. A
// request without an open suspension is returned untouched, so retries and
// duplicate workflow starts are harmless.
func (a *Activities) ResumePipeline(ctx context.Context, loaded domain.ApprovalRequest) (domain.ApprovalRequest, error) {
	request, suspension, open, err := a.openSuspension(ctx, loaded)
	if err != nil || !open {
		return request, err
	}
	activity.GetLogger(ctx).Info("resuming suspended approval pipeline", loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(request.OrganizationID), loggermdc.KeyApprovalRequestID: string(request.ID),
	})...)
	if a.pipeline == nil {
		return a.commitResume(ctx, request, suspension, func() (application.ResumeApprovalInput, error) {
			return application.ResumeApprovalInput{}, errors.New("pipeline resume is not wired in this worker")
		}, true)
	}
	return a.commitResume(ctx, request, suspension, func() (application.ResumeApprovalInput, error) {
		return a.pipeline.Resume(ctx, request, suspension)
	}, false)
}

// AbandonPipeline completes a suspended pipeline to a human without running
// it, after the resume activity exhausted its retries.
func (a *Activities) AbandonPipeline(ctx context.Context, loaded domain.ApprovalRequest) (domain.ApprovalRequest, error) {
	request, suspension, open, err := a.openSuspension(ctx, loaded)
	if err != nil || !open {
		return request, err
	}
	activity.GetLogger(ctx).Warn("approval pipeline could not resume; handing request to human review", loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(request.OrganizationID), loggermdc.KeyApprovalRequestID: string(request.ID),
	})...)
	return a.commitResume(ctx, request, suspension, nil, true)
}

// openSuspension looks for an open suspension on the loaded request. Most
// requests have none, so the request is only reloaded (for a fresh status)
// when there is work to do.
func (a *Activities) openSuspension(ctx context.Context, loaded domain.ApprovalRequest) (domain.ApprovalRequest, domain.PipelineSuspension, bool, error) {
	if loaded.Status.Terminal() {
		return loaded, domain.PipelineSuspension{}, false, nil
	}
	suspension, err := a.approvals.GetPipelineSuspension(ctx, application.GetPipelineSuspensionInput{OrganizationID: loaded.OrganizationID, RequestID: loaded.ID})
	if errors.Is(err, domain.ErrNotFound) {
		return loaded, domain.PipelineSuspension{}, false, nil
	}
	if err != nil {
		return domain.ApprovalRequest{}, domain.PipelineSuspension{}, false, err
	}
	if !suspension.Open() {
		return loaded, suspension, false, nil
	}
	request, err := a.approvals.GetApproval(ctx, application.GetApprovalInput{OrganizationID: loaded.OrganizationID, RequestID: loaded.ID})
	if err != nil {
		return domain.ApprovalRequest{}, domain.PipelineSuspension{}, false, err
	}
	if request.Status.Terminal() {
		return request, suspension, false, nil
	}
	return request, suspension, true, nil
}

// commitResume prepares the resume write (running the pipeline, or
// abandoning it when abandon is set or the run fails to prepare), attaches
// routing for a request that still needs a human, and commits it.
func (a *Activities) commitResume(ctx context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension, prepare func() (application.ResumeApprovalInput, error), abandon bool) (domain.ApprovalRequest, error) {
	var input application.ResumeApprovalInput
	var err error
	if abandon {
		input, err = a.abandonInput(ctx, request, suspension)
	} else {
		input, err = prepare()
	}
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if input.Decision == nil && a.router != nil {
		snapshot, event, ok := a.router.Evaluate(ctx, request, input.HumanReview)
		input.Routing = &snapshot
		if ok {
			input.AuditEvents = append(input.AuditEvents, event)
		}
	}
	return a.approvals.ResumeApproval(ctx, input)
}

func (a *Activities) abandonInput(ctx context.Context, request domain.ApprovalRequest, suspension domain.PipelineSuspension) (application.ResumeApprovalInput, error) {
	if a.pipeline != nil {
		return a.pipeline.Abandon(ctx, request, suspension, "block_error")
	}
	// No pipeline runtime in this worker: complete with the evidence the
	// suspension already carries so the request still reaches a human.
	data, err := json.Marshal(map[string]any{
		"pipeline_evaluation_id": suspension.EvaluationID,
		"agent_slug":             request.AgentSlug,
		"outcome":                "human",
		"reason_code":            "block_error",
		"attempted_block_count":  suspension.AttemptedBlockCount,
		"total_attempts":         suspension.TotalAttempts,
		"total_cost":             suspension.TotalCost,
		"duration_ms":            a.clock.Now().Sub(suspension.StartedAt).Milliseconds(),
	})
	if err != nil {
		return application.ResumeApprovalInput{}, err
	}
	return application.ResumeApprovalInput{
		OrganizationID: request.OrganizationID, RequestID: request.ID, EvaluationID: suspension.EvaluationID,
		AuditEvents: []domain.AuditEvent{{
			ID: domain.ID(uuid.NewString()), OrganizationID: request.OrganizationID,
			EventType: "request_approval_pipeline.completed", ActorType: domain.ActorSystem,
			SubjectType: "approval_request", SubjectID: request.ID, Data: data, OccurredAt: a.clock.Now(),
		}},
		Now: a.clock.Now(),
	}, nil
}

func (a *Activities) LoadApproval(ctx context.Context, organizationID, requestID domain.ID) (domain.ApprovalRequest, error) {
	activity.GetLogger(ctx).Info("loading approval request for workflow processing", loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(organizationID), loggermdc.KeyApprovalRequestID: string(requestID),
	})...)
	return a.approvals.GetApproval(ctx, application.GetApprovalInput{OrganizationID: organizationID, RequestID: requestID})
}

func (a *Activities) RecordDecision(ctx context.Context, command domain.DecisionCommand) (domain.DecisionOperation, error) {
	activity.GetLogger(ctx).Info("recording approval decision - "+string(command.Status), loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(command.OrganizationID), loggermdc.KeyApprovalRequestID: string(command.RequestID),
		loggermdc.KeyDecisionOperationID: string(command.OperationID),
	})...)
	return a.approvals.ApplyDecision(ctx, application.ApplyDecisionInput{Command: command, Now: a.clock.Now()})
}

func (a *Activities) ExpireApproval(ctx context.Context, organizationID, requestID domain.ID) (domain.ApprovalRequest, error) {
	activity.GetLogger(ctx).Info("attempting approval expiry after deadline", loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(organizationID), loggermdc.KeyApprovalRequestID: string(requestID),
	})...)
	return a.approvals.ExpireApproval(ctx, application.ExpireApprovalInput{
		OrganizationID: organizationID,
		RequestID:      requestID,
		Now:            a.clock.Now(),
	})
}

// LapseClaim releases one specific claim after its retrigger timer fired
// without a decision. The store's compare-and-set by claim id makes
// duplicate timers and activity retries clean no-ops.
func (a *Activities) LapseClaim(ctx context.Context, organizationID, requestID, claimID domain.ID) error {
	activity.GetLogger(ctx).Info("attempting approval claim release after review timeout", loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(organizationID), loggermdc.KeyApprovalRequestID: string(requestID),
	})...)
	return a.approvals.LapseClaim(ctx, application.LapseClaimInput{
		OrganizationID: organizationID,
		RequestID:      requestID,
		ClaimID:        claimID,
		Now:            a.clock.Now(),
	})
}

// PlanNotifications resolves who to tell and, from each person's ladder
// for the request's urgency, when and how. The workflow arms one timer per
// step; the plan is data, so it is recorded in history and replays.
func (a *Activities) PlanNotifications(ctx context.Context, request domain.ApprovalRequest) ([]application.NotificationStep, error) {
	recipients, err := a.recipients.ResolveApprovalRecipients(ctx, application.ResolveApprovalRecipientsInput{
		OrganizationID: request.OrganizationID, RequestID: request.ID,
	})
	if err != nil {
		return nil, err
	}
	if err := a.recordNotified(ctx, request, recipients); err != nil {
		return nil, err
	}
	logger := activity.GetLogger(ctx)
	fields := loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(request.OrganizationID), loggermdc.KeyApprovalRequestID: string(request.ID),
	})
	if len(recipients.Recipients) == 0 {
		logger.Warn("approval has no reachable reviewers; no notifications scheduled", append(fields, "reason", "no_reachable_reviewers", "routing_outcome", string(recipients.Outcome))...)
		return nil, nil
	}
	userIDs := make([]domain.ID, 0, len(recipients.Recipients))
	for _, recipient := range recipients.Recipients {
		userIDs = append(userIDs, recipient.UserID)
	}
	rules, err := a.rules.ListRecipientRules(ctx, application.ListRecipientRulesInput{UserIDs: userIDs})
	if err != nil {
		return nil, err
	}
	plan := BuildNotificationPlan(recipients, rules, request.DeadlineAt.Sub(a.clock.Now()))
	logger.Info(fmt.Sprintf("approval notification plan ready - %d recipients, %d delivery steps", len(recipients.Recipients), len(plan)), append(fields, "routing_outcome", string(recipients.Outcome),
		"urgency", string(recipients.Urgency), "recipient_count", len(recipients.Recipients), "step_count", len(plan))...)
	return plan, nil
}

// DeliverNotificationStep sends one step of the plan through every
// channel. The step carries exactly the targets due now.
func (a *Activities) DeliverNotificationStep(ctx context.Context, loaded domain.ApprovalRequest, step application.NotificationStep) error {
	// Temporal's JSON payload omits private fields such as AgentID. Reload
	// the complete request before validating and sending the prompt, and
	// skip a request that was decided while the delivery was queued.
	request, err := a.approvals.GetApproval(ctx, application.GetApprovalInput{
		OrganizationID: loaded.OrganizationID, RequestID: loaded.ID,
	})
	if err != nil {
		return err
	}
	if request.Status.Terminal() {
		return nil
	}
	activity.GetLogger(ctx).Info(fmt.Sprintf("sending approval notification step - %d recipients", len(step.Recipients)), loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(request.OrganizationID), loggermdc.KeyApprovalRequestID: string(request.ID),
	})...)
	return a.notifications.SendApprovalPrompt(ctx, application.SendApprovalPromptInput{
		Request: request, RoutingOutcome: step.Outcome, Recipients: step.Recipients,
	})
}
