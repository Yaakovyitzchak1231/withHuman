package orchestration

import (
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/blockruntime"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/escalation"
	"github.com/withHumanAI/withHuman/loggermdc"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type decisionSignal struct {
	Command domain.DecisionCommand `json:"command"`
	MDC     loggermdc.Fields       `json:"mdc,omitempty"`
}

// claimSignal arms the retrigger timer for one committed claim. ExpiresAt
// is computed by the store (retrigger window capped at the deadline), so
// the workflow never reads configuration and the timer duration is stable
// across replay.
type claimSignal struct {
	ClaimID   domain.ID        `json:"claim_id"`
	ExpiresAt time.Time        `json:"expires_at"`
	MDC       loggermdc.Fields `json:"mdc,omitempty"`
}

// cancelSignal says the requesting instance withdrew the request. The
// workflow confirms against the store before standing down, so a stray
// signal can never end a request that is still pending.
type cancelSignal struct {
	MDC loggermdc.Fields `json:"mdc,omitempty"`
}

// claimReleasedSignal names the claim that ended early; the workflow
// resumes escalation only when it is the claim it is holding for.
type claimReleasedSignal struct {
	ClaimID domain.ID        `json:"claim_id"`
	MDC     loggermdc.Fields `json:"mdc,omitempty"`
}

// agentOwnerHandoffSignal says the owner of an agent_owner request was
// deactivated. The handoff committed before the signal, so the workflow
// loads the fallback program from the store.
type agentOwnerHandoffSignal struct {
	MDC loggermdc.Fields `json:"mdc,omitempty"`
}

type ApprovalWorkflowInput struct {
	OrganizationID domain.ID `json:"organization_id"`
	RequestID      domain.ID `json:"request_id"`
}

func ApprovalWorkflow(ctx workflow.Context, input ApprovalWorkflowInput) error {
	ctx = withWorkflowMDC(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID:    string(input.OrganizationID),
		loggermdc.KeyApprovalRequestID: string(input.RequestID),
	})
	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second, BackoffCoefficient: 2,
			MaximumInterval: time.Minute,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, activityOptions)
	var request domain.ApprovalRequest
	if err := workflow.ExecuteActivity(ctx, (*Activities).LoadApproval, input.OrganizationID, input.RequestID).Get(ctx, &request); err != nil {
		return err
	}
	if request.Status.Terminal() {
		return nil
	}

	// A pipeline suspended before an asynchronous block resumes here, off
	// the request path, before anyone is notified. The resume budget is
	// bounded by the request deadline; if it cannot complete, the pipeline
	// is abandoned to a human so the request never sits unrouted.
	request, err := resumePipeline(ctx, request)
	if err != nil {
		return err
	}
	if request.Status.Terminal() {
		return nil
	}

	// The edition's human review hooks (the hosted summarizer) run beside
	// notification planning and are never waited on: their result cannot
	// delay a prompt, and a decision that lands first ends the workflow
	// without them.
	hooks := startHumanReviewHooks(ctx, request)

	// Notification delivery never controls the request outcome. Each step
	// fires on its own timer and delivers in a detached goroutine, so a
	// slow or failing send cannot delay a decision. A decision or any
	// claim cancels the steps still to come. A step due before the hooks
	// finish waits for them, at most humanReviewHookWait from now, so a
	// summary they write can reach the first prompt.
	steps := &stepRunner{ctx: ctx, request: request, hooks: hooks, hooksBy: workflow.Now(ctx).Add(humanReviewHookWait)}

	// An escalated request walks its pinned path level by level; the
	// program is loaded once and the machine replays deterministically
	// from it. Anything else, and an escalated request whose program
	// cannot be loaded, pages everyone the routing resolved at once.
	var program EscalationProgramOutput
	if err := workflow.ExecuteActivity(ctx, (*Activities).LoadEscalationProgram, request).Get(ctx, &program); err != nil {
		workflow.GetLogger(ctx).Warn("escalation program unavailable; falling back to notifying all reachable reviewers", "error", err.Error())
	}
	var driver *escalationDriver
	if program.Escalated {
		driver = &escalationDriver{ctx: ctx, request: request, machine: escalation.NewMachine(program.Program), steps: steps}
		driver.apply(driver.machine.Start(workflow.Now(ctx)))
	} else {
		var plan []application.NotificationStep
		if err := workflow.ExecuteActivity(ctx, (*Activities).PlanNotifications, request).Get(ctx, &plan); err != nil {
			// Planning failure is an operational signal, never a stuck request:
			// the queue still lists it, and the deadline still applies.
			workflow.GetLogger(ctx).Warn("approval notification planning failed; request remains in the review queue", "error", err.Error())
		}
		steps.spawn(plan)
	}

	decisionChannel := workflow.GetSignalChannel(ctx, DecisionSignal)
	claimChannel := workflow.GetSignalChannel(ctx, ClaimSignal)
	cancelChannel := workflow.GetSignalChannel(ctx, CancelSignal)
	claimReleasedChannel := workflow.GetSignalChannel(ctx, ClaimReleasedSignal)
	slackDisconnectedChannel := workflow.GetSignalChannel(ctx, SlackDisconnectedSignal)
	agentOwnerHandoffChannel := workflow.GetSignalChannel(ctx, AgentOwnerHandoffSignal)
	deadline := request.DeadlineAt.Sub(workflow.Now(ctx))
	if deadline < 0 {
		deadline = 0
	}
	timer := workflow.NewTimer(ctx, deadline)

	// The claim retrigger timer is armed by claim signals and replaced on
	// re-claim. A canceled or superseded timer future is dropped without
	// ever re-entering a selector, so its completion is inert.
	var claimTimer workflow.Future
	var cancelClaimTimer workflow.CancelFunc
	var activeClaim claimSignal
	standDown := func() {
		if cancelClaimTimer != nil {
			cancelClaimTimer()
		}
		steps.cancel()
		driver.stop()
	}

	for {
		selector := workflow.NewSelector(ctx)
		decisionReady := false
		deadlineReady := false
		claimReady := false
		claimLapsed := false
		claimReleased := false
		cancelReady := false
		escalationDue := false
		handedOff := false
		var disconnectedInstallation domain.ID
		var signal decisionSignal
		var claim claimSignal
		var released claimReleasedSignal
		var cancellation cancelSignal
		var handoff agentOwnerHandoffSignal
		selector.AddReceive(decisionChannel, func(channel workflow.ReceiveChannel, more bool) {
			channel.Receive(ctx, &signal)
			decisionReady = true
		})
		selector.AddReceive(claimChannel, func(channel workflow.ReceiveChannel, more bool) {
			channel.Receive(ctx, &claim)
			claimReady = true
		})
		selector.AddReceive(claimReleasedChannel, func(channel workflow.ReceiveChannel, more bool) {
			channel.Receive(ctx, &released)
			claimReleased = true
		})
		selector.AddReceive(cancelChannel, func(channel workflow.ReceiveChannel, more bool) {
			channel.Receive(ctx, &cancellation)
			cancelReady = true
		})
		selector.AddReceive(slackDisconnectedChannel, func(channel workflow.ReceiveChannel, more bool) { channel.Receive(ctx, &disconnectedInstallation) })
		selector.AddReceive(agentOwnerHandoffChannel, func(channel workflow.ReceiveChannel, more bool) {
			channel.Receive(ctx, &handoff)
			handedOff = true
		})
		if claimTimer != nil {
			selector.AddFuture(claimTimer, func(workflow.Future) { claimLapsed = true })
		}
		if wake := driver.wake(); wake != nil {
			selector.AddFuture(wake, func(workflow.Future) { escalationDue = true })
		}
		selector.AddFuture(timer, func(workflow.Future) { deadlineReady = true })
		selector.Select(ctx)
		if disconnectedInstallation != "" {
			steps.slackDisconnected(disconnectedInstallation)
			continue
		}

		// A signal already recorded in history wins when it is selected before the
		// timer. Otherwise expiration wins. Exactly one branch persists terminal
		// state, and the database independently guards immutability.
		if decisionReady {
			decisionContext := replaceWorkflowMDC(ctx, signal.MDC)
			var operation domain.DecisionOperation
			if err := workflow.ExecuteActivity(decisionContext, (*Activities).RecordDecision, signal.Command).Get(decisionContext, &operation); err != nil {
				return err
			}
			if operation.Status == domain.OperationApplied {
				// The decision transaction already closed any open claim.
				standDown()
				return nil
			}
			continue
		}
		if cancelReady {
			// The agent withdrew the request. The cancellation committed
			// before the signal was sent, so the reload is a guard, not a
			// lookup: a request that is somehow still pending keeps waiting.
			cancelContext := replaceWorkflowMDC(ctx, cancellation.MDC)
			var current domain.ApprovalRequest
			if err := workflow.ExecuteActivity(cancelContext, (*Activities).LoadApproval, request.OrganizationID, request.ID).Get(cancelContext, &current); err != nil {
				return err
			}
			if !current.Status.Terminal() {
				continue
			}
			standDown()
			return nil
		}
		if claimReady {
			// Someone is looking at it: stand everyone else's later rungs
			// down and hold the escalation clock. The deadline still caps.
			steps.cancel()
			if cancelClaimTimer != nil {
				cancelClaimTimer()
			}
			retrigger := claim.ExpiresAt.Sub(workflow.Now(ctx))
			if retrigger < 0 {
				retrigger = 0
			}
			timerContext, cancel := workflow.WithCancel(ctx)
			claimTimer, cancelClaimTimer, activeClaim = workflow.NewTimer(timerContext, retrigger), cancel, claim
			driver.pause()
			continue
		}
		if claimLapsed {
			if cancelClaimTimer != nil {
				cancelClaimTimer()
			}
			claimTimer, cancelClaimTimer = nil, nil
			lapsed := activeClaim
			// Releasing a lapsed claim is advisory bookkeeping, so it runs
			// detached exactly like notification delivery: awaiting it here
			// would let a retrying write delay a decision signal, and a
			// failing one would end the workflow and strand the request
			// short of its deadline. Escalation resumes at once with the
			// time each level had left when the claim arrived.
			workflow.Go(ctx, func(lapseContext workflow.Context) {
				lapseContext = replaceWorkflowMDC(lapseContext, lapsed.MDC)
				_ = workflow.ExecuteActivity(lapseContext, (*Activities).LapseClaim, request.OrganizationID, request.ID, lapsed.ClaimID).Get(lapseContext, nil)
			})
			driver.resume()
			continue
		}
		if claimReleased {
			// Only the claim being held for counts: a release for a claim
			// the retrigger timer already lapsed, or one superseded by a
			// re-claim, changes nothing.
			if claimTimer == nil || released.ClaimID != activeClaim.ClaimID {
				continue
			}
			cancelClaimTimer()
			claimTimer, cancelClaimTimer = nil, nil
			driver.resume()
			continue
		}
		if escalationDue {
			driver.tick()
			continue
		}
		if handedOff {
			// The agent's owner was deactivated: their pages stop and the
			// pinned fallback starts from its first node, as though the
			// request had just been routed there. A request already
			// walking a path ignores a repeated signal.
			if driver != nil {
				continue
			}
			handoffContext := replaceWorkflowMDC(ctx, handoff.MDC)
			var fallback EscalationProgramOutput
			if err := workflow.ExecuteActivity(handoffContext, (*Activities).LoadEscalationProgram, request).Get(handoffContext, &fallback); err != nil {
				workflow.GetLogger(ctx).Warn("fallback escalation program unavailable; notifying every reviewer the routing now admits", "error", err.Error())
			}
			steps.cancel()
			if fallback.Escalated {
				driver = &escalationDriver{ctx: ctx, request: request, machine: escalation.NewMachine(fallback.Program), steps: steps}
				driver.apply(driver.machine.Start(workflow.Now(ctx)))
				if claimTimer != nil {
					driver.pause()
				}
				continue
			}
			var plan []application.NotificationStep
			if err := workflow.ExecuteActivity(handoffContext, (*Activities).PlanNotifications, request).Get(handoffContext, &plan); err != nil {
				workflow.GetLogger(ctx).Warn("approval notification planning failed after the handoff; request remains in the review queue", "error", err.Error())
			}
			steps.spawn(plan)
			continue
		}
		if deadlineReady {
			standDown()
			return workflow.ExecuteActivity(ctx, (*Activities).ExpireApproval, request.OrganizationID, request.ID).Get(ctx, nil)
		}
	}
}

// stepRunner spawns notification steps in detached goroutines. Every
// batch runs under its own cancellable context, and cancel stands every
// batch down at once, so a claim or decision silences the rungs still to
// come from every level entered so far.
type stepRunner struct {
	ctx     workflow.Context
	request domain.ApprovalRequest
	cancels []workflow.CancelFunc
	pending []*pendingNotification
	// hooks is the human review hooks' activity, which a step waits for
	// until hooksBy before it delivers.
	hooks   workflow.Future
	hooksBy time.Time
}

// awaitHooks holds a step until the hooks finish or hooksBy passes,
// whichever is first; it reports false when the step was cancelled while
// waiting.
func (r *stepRunner) awaitHooks(ctx workflow.Context) bool {
	if r.hooks == nil || r.hooks.IsReady() {
		return true
	}
	remaining := r.hooksBy.Sub(workflow.Now(ctx))
	if remaining <= 0 {
		return true
	}
	timerCtx, cancelTimer := workflow.WithCancel(ctx)
	defer cancelTimer()
	selector := workflow.NewSelector(ctx)
	selector.AddFuture(r.hooks, func(workflow.Future) {})
	selector.AddFuture(workflow.NewTimer(timerCtx, remaining), func(workflow.Future) {})
	selector.Select(ctx)
	return ctx.Err() == nil
}

type pendingNotification struct {
	step  application.NotificationStep
	fired bool
}

func (r *stepRunner) spawn(steps []application.NotificationStep) {
	if len(steps) == 0 {
		return
	}
	stepsContext, cancel := workflow.WithCancel(r.ctx)
	r.cancels = append(r.cancels, cancel)
	for _, step := range steps {
		pending := &pendingNotification{step: step}
		r.pending = append(r.pending, pending)
		workflow.Go(stepsContext, func(stepContext workflow.Context) {
			if step.Delay > 0 {
				if err := workflow.Sleep(stepContext, step.Delay); err != nil {
					return
				}
			}
			if !r.awaitHooks(stepContext) {
				return
			}
			pending.fired = true
			if len(pending.step.Recipients) == 0 {
				return
			}
			// One attempt: the registry already tries every channel and
			// logs what failed, and a second attempt would re-send through
			// the channels that succeeded.
			once := workflow.WithRetryPolicy(stepContext, temporal.RetryPolicy{MaximumAttempts: 1})
			var holds []NotificationHold
			_ = workflow.ExecuteActivity(once, (*Activities).DeliverNotificationStep, r.request, pending.step).Get(once, &holds)
			for _, hold := range holds {
				r.closeWindow(stepContext, hold)
			}
		})
	}
}

// closeWindow waits for a held prompt's window and closes it, inside the
// step's context, so a claim or decision stands the wait down with the
// rest of the ladder. Retrying is safe: a close that committed answers
// nothing the second time, because the window no longer closes then.
func (r *stepRunner) closeWindow(ctx workflow.Context, hold NotificationHold) {
	workflow.Go(ctx, func(holdContext workflow.Context) {
		if wait := hold.ClosesAt.Sub(workflow.Now(holdContext)); wait > 0 {
			if err := workflow.Sleep(holdContext, wait); err != nil {
				return
			}
		}
		bounded := workflow.WithRetryPolicy(holdContext, temporal.RetryPolicy{
			InitialInterval: time.Second, BackoffCoefficient: 2, MaximumAttempts: 3,
		})
		_ = workflow.ExecuteActivity(bounded, (*Activities).CloseNotificationWindow, r.request.OrganizationID, hold).Get(bounded, nil)
	})
}

func (r *stepRunner) cancel() {
	for _, cancel := range r.cancels {
		cancel()
	}
	r.cancels = nil
	r.pending = nil
}

// Only unsent Slack-only recipients move to an immediate fallback. Remaining
// channels keep their original timers, and duplicate signals find no recipients.
func (r *stepRunner) slackDisconnected(installationID domain.ID) {
	var fallbacks []application.NotificationStep
	for _, pending := range r.pending {
		if pending.fired {
			continue
		}
		step := pending.step
		remaining := make([]application.NotificationRecipient, 0, len(step.Recipients))
		fallback := application.NotificationStep{Outcome: step.Outcome}
		for _, recipient := range step.Recipients {
			if recipient.SlackUserID != "" && recipient.SlackInstallationID == installationID && recipient.SlackFallbackEmail != "" {
				recipient.SlackUserID = ""
				recipient.SlackFallback = true
				fallback.Recipients = append(fallback.Recipients, recipient)
			} else {
				remaining = append(remaining, recipient)
			}
		}
		pending.step.Recipients = remaining
		if len(fallback.Recipients) > 0 {
			fallbacks = append(fallbacks, fallback)
		}
	}
	r.spawn(fallbacks)
}

// escalationDriver runs the pure escalation machine inside the workflow:
// it takes the machine's effects to the apply activity, arms a timer for
// the machine's next wake, and spawns the notification steps the
// activity resolves. A nil driver (a default queue request) is inert.
// Every instant comes from workflow.Now, so replay rebuilds the same
// machine from the same history.
type escalationDriver struct {
	ctx      workflow.Context
	request  domain.ApprovalRequest
	machine  *escalation.Machine
	steps    *stepRunner
	sequence int64
	timer    workflow.Future
	cancel   workflow.CancelFunc
}

// applyOptions bounds the apply activity: it is idempotent through the
// sequence, so a handful of retries is safe, but a store that keeps
// refusing must not hold the selector loop, and with it a decision
// signal, forever. Escalation is notification, never a decision.
var applyOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval: time.Second, BackoffCoefficient: 2,
		MaximumInterval: 30 * time.Second, MaximumAttempts: 5,
	},
}

// apply records one tick and pages the levels it entered, then re-arms
// the wake timer from the machine's new position. Effects may be empty:
// a pause records the frozen position and nothing else.
func (d *escalationDriver) apply(effects []escalation.Effect) {
	now := workflow.Now(d.ctx)
	d.sequence++
	input := ApplyEscalationEffectsInput{
		Request: d.request, Sequence: d.sequence, Effects: effects,
		Progress: d.machine.Progress(d.sequence, now),
	}
	applyContext := workflow.WithActivityOptions(d.ctx, applyOptions)
	var output ApplyEscalationEffectsOutput
	if err := workflow.ExecuteActivity(applyContext, (*Activities).ApplyEscalationEffects, input).Get(applyContext, &output); err != nil {
		workflow.GetLogger(d.ctx).Warn("escalation update failed; notification steps were not scheduled", "sequence", d.sequence, "error", err.Error())
	} else if !output.Stale {
		d.steps.spawn(output.Steps)
	}
	d.arm()
}

// arm replaces the wake timer with one for the machine's earliest armed
// timer, or none when every walker is paused or exhausted. The previous
// timer's context is canceled first so its completion is inert.
func (d *escalationDriver) arm() {
	d.stop()
	wake, armed := d.machine.NextWake()
	if !armed {
		return
	}
	delay := wake.Sub(workflow.Now(d.ctx))
	if delay < 0 {
		delay = 0
	}
	timerContext, cancel := workflow.WithCancel(d.ctx)
	d.timer, d.cancel = workflow.NewTimer(timerContext, delay), cancel
}

func (d *escalationDriver) wake() workflow.Future {
	if d == nil {
		return nil
	}
	return d.timer
}

func (d *escalationDriver) tick() {
	if d == nil {
		return
	}
	d.apply(d.machine.Tick(workflow.Now(d.ctx)))
}

func (d *escalationDriver) pause() {
	if d == nil {
		return
	}
	d.machine.Pause(workflow.Now(d.ctx))
	d.apply(nil)
}

// resume thaws every frozen timer and fires the ones already due: a
// level whose wait ran out while the claim was held pages at once.
func (d *escalationDriver) resume() {
	if d == nil {
		return
	}
	now := workflow.Now(d.ctx)
	d.machine.Resume(now)
	d.apply(d.machine.Tick(now))
}

func (d *escalationDriver) stop() {
	if d == nil || d.cancel == nil {
		return
	}
	d.cancel()
	d.timer, d.cancel = nil, nil
}

const pipelineResumeTries = 3

// pipelineResumeBudget bounds one resume attempt. It is derived from
// constants so it is deterministic on replay: core's asynchronous block
// type at its maximum count, per-attempt timeout, and attempts, the
// allowance every edition's asynchronous kinds must stay within, plus
// backoff and headroom for the commit.
var pipelineResumeBudget = resumeBudget()

func resumeBudget() time.Duration {
	webhook := time.Duration(approvalblock.MaximumWebhookBlocksPerRequest) *
		(approvalblock.MaximumWebhookTimeout*time.Duration(approvalblock.MaximumWebhookAttempts) + 15*time.Second)
	return webhook + blockruntime.MaximumEditionAsynchronousBudget + time.Minute
}

func resumePipeline(ctx workflow.Context, request domain.ApprovalRequest) (domain.ApprovalRequest, error) {
	remaining := request.DeadlineAt.Sub(workflow.Now(ctx))
	if remaining < time.Second {
		remaining = time.Second
	}
	resumeContext := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    pipelineResumeBudget,
		ScheduleToCloseTimeout: min(pipelineResumeBudget*pipelineResumeTries, remaining),
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 5 * time.Second, BackoffCoefficient: 2,
			MaximumInterval: time.Minute, MaximumAttempts: pipelineResumeTries,
		},
	})
	var resumed domain.ApprovalRequest
	if err := workflow.ExecuteActivity(resumeContext, (*Activities).ResumePipeline, request).Get(resumeContext, &resumed); err == nil {
		return resumed, nil
	}
	if err := workflow.ExecuteActivity(ctx, (*Activities).AbandonPipeline, request).Get(ctx, &resumed); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return resumed, nil
}

// humanReviewHookWait is the longest a prompt waits for the edition's
// human review hooks; past it the prompt goes out without what they write.
const humanReviewHookWait = 10 * time.Second

// startHumanReviewHooks starts the hooks' activity with a small budget of
// its own (two attempts, twenty seconds each). Only notification steps
// wait for it, and only up to humanReviewHookWait. A worker without hooks
// finishes it at once.
func startHumanReviewHooks(ctx workflow.Context, request domain.ApprovalRequest) workflow.Future {
	hookCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 2},
	})
	return workflow.ExecuteActivity(hookCtx, (*Activities).RunHumanReviewHooks, request)
}
