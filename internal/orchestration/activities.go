package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/loggermdc"
	"github.com/withHumanAI/withHuman/telemetry"
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
	windows       application.NotificationWindowRepository
	reviewHooks   []application.HumanReviewHook
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

// WithNotificationWindows throttles prompts per person and kind (design
// doc 17). Without it every step sends at once.
func WithNotificationWindows(windows application.NotificationWindowRepository) ActivityOption {
	return func(a *Activities) {
		a.windows = windows
	}
}

// WithHumanReviewHooks tells the edition's hooks when a request starts
// waiting for a person. Without any the activity does nothing.
func WithHumanReviewHooks(hooks ...application.HumanReviewHook) ActivityOption {
	return func(a *Activities) {
		a.reviewHooks = hooks
	}
}

// RunHumanReviewHooks calls every hook with the routed request. One
// failing hook does not stop the others; the joined error lets Temporal
// retry within the workflow's small budget, so hooks must be idempotent.
func (a *Activities) RunHumanReviewHooks(ctx context.Context, request domain.ApprovalRequest) error {
	var failures []error
	for _, hook := range a.reviewHooks {
		if err := hook.HumanReviewRequested(ctx, application.HumanReviewRequestedInput{Request: request}); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
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

// NotificationHold is one prompt a notification window held: the person
// narrowed to the held kind, and the window it waits for. The workflow
// sleeps until ClosesAt and then closes the window (design doc 17).
type NotificationHold struct {
	Outcome   domain.RoutingOutcome
	Recipient application.NotificationRecipient
	Kind      domain.ContactMethodKind
	ClosesAt  time.Time
}

// DeliverNotificationStep sends one step of the plan. With notification
// windows, each person's kinds pass through their window first: what is
// admitted goes out now in one dispatch, and what is held comes back for
// the workflow to close later. A window that cannot be read sends at
// once: a throttle failure must never cost someone their page.
func (a *Activities) DeliverNotificationStep(ctx context.Context, loaded domain.ApprovalRequest, step application.NotificationStep) ([]NotificationHold, error) {
	// Temporal's JSON payload omits private fields such as AgentID. Reload
	// the complete request before validating and sending the prompt, and
	// skip a request that was decided while the delivery was queued.
	request, err := a.approvals.GetApproval(ctx, application.GetApprovalInput{
		OrganizationID: loaded.OrganizationID, RequestID: loaded.ID,
	})
	if err != nil {
		return nil, err
	}
	if request.Status.Terminal() {
		return nil, nil
	}
	fields := loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(request.OrganizationID), loggermdc.KeyApprovalRequestID: string(request.ID),
	})
	logger := activity.GetLogger(ctx)
	recipients, holds := a.admit(ctx, request, step, func(err error) {
		logger.Warn("notification window unavailable; sending at once", append(fields, "error", err.Error())...)
	})
	if len(holds) > 0 {
		logger.Info(fmt.Sprintf("approval notification held for a digest - %d deliveries", len(holds)), append(fields, "held_count", len(holds))...)
	}
	if len(recipients) == 0 {
		return holds, nil
	}
	logger.Info(fmt.Sprintf("sending approval notification step - %d recipients", len(recipients)), fields...)
	return holds, a.notifications.SendApprovalPrompt(ctx, application.SendApprovalPromptInput{
		Request: request, RoutingOutcome: step.Outcome, Recipients: recipients,
	})
}

// admit splits a step into the recipients to send to now and the holds.
func (a *Activities) admit(ctx context.Context, request domain.ApprovalRequest, step application.NotificationStep, unavailable func(error)) ([]application.NotificationRecipient, []NotificationHold) {
	if a.windows == nil {
		return step.Recipients, nil
	}
	now := a.clock.Now()
	var recipients []application.NotificationRecipient
	var holds []NotificationHold
	for _, recipient := range step.Recipients {
		if recipient.UserID != "" {
			var held []domain.ContactMethodKind
			for _, kind := range recipient.DueKinds() {
				windows := kind.DigestWindows()
				if len(windows) == 0 {
					continue
				}
				admission, err := a.windows.AdmitNotification(ctx, application.AdmitNotificationInput{
					OrganizationID: request.OrganizationID, UserID: recipient.UserID, Kind: kind,
					RequestID: request.ID, Windows: windows, Now: now,
				})
				if err != nil {
					unavailable(err)
					continue
				}
				if admission.Held {
					telemetry.NotificationHeld(ctx, string(kind))
					held = append(held, kind)
					holds = append(holds, NotificationHold{Outcome: step.Outcome, Recipient: recipient.Only(kind), Kind: kind, ClosesAt: admission.ClosesAt})
				}
			}
			recipient = recipient.Without(held...)
		}
		if len(recipient.DueKinds()) > 0 || recipient.SlackFallback {
			recipients = append(recipients, recipient)
		}
	}
	return recipients, holds
}

// CloseNotificationWindow closes the window a hold waits for and sends
// what it hands back: nothing when another holder closed it first or
// nothing held still needs anyone, the ordinary prompt for one request,
// and a digest for more.
func (a *Activities) CloseNotificationWindow(ctx context.Context, organizationID domain.ID, hold NotificationHold) error {
	if a.windows == nil {
		return nil
	}
	closed, err := a.windows.CloseNotificationWindow(ctx, application.CloseNotificationWindowInput{
		OrganizationID: organizationID, UserID: hold.Recipient.UserID, MembershipID: hold.Recipient.MembershipID,
		Kind: hold.Kind, ClosesAt: hold.ClosesAt, Windows: hold.Kind.DigestWindows(), Now: a.clock.Now(),
	})
	if err != nil {
		return err
	}
	fields := loggermdc.KeyValues(ctx, loggermdc.Fields{loggermdc.KeyOrganizationID: string(organizationID)})
	switch len(closed.Requests) {
	case 0:
		return nil
	case 1:
		activity.GetLogger(ctx).Info("notification window closed - sending the one held prompt", append(fields, "contact_kind", string(hold.Kind))...)
		return a.notifications.SendApprovalPrompt(ctx, application.SendApprovalPromptInput{
			Request: closed.Requests[0], RoutingOutcome: hold.Outcome, Recipients: []application.NotificationRecipient{hold.Recipient},
		})
	default:
		activity.GetLogger(ctx).Info(fmt.Sprintf("notification window closed - sending a digest of %d requests", len(closed.Requests)),
			append(fields, "contact_kind", string(hold.Kind), "request_count", len(closed.Requests))...)
		return a.notifications.SendApprovalDigest(ctx, application.SendApprovalDigestInput{
			OrganizationID: organizationID, Recipient: hold.Recipient, Requests: closed.Requests,
		})
	}
}
