package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/escalation"
	"github.com/withHumanAI/withHuman/loggermdc"
	"github.com/withHumanAI/withHuman/routing"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// WithEscalation enables the per-level escalation executor. Without it
// every request runs the default queue plan, which pages everyone the
// routing resolved at once.
func WithEscalation(store application.EscalationRepository) ActivityOption {
	return func(a *Activities) {
		a.escalation = store
	}
}

// EscalationProgramOutput is the loaded program. It is activity output so
// it lands in history and the workflow replays the same program; the
// documents are capped at 64 KiB each, so it stays well inside the event
// size limit.
type EscalationProgramOutput struct {
	Escalated bool               `json:"escalated"`
	Program   escalation.Program `json:"program"`
}

// ApplyEscalationEffectsInput is one tick of the executor: the effects it
// produced and the position it reached, numbered so a retried activity or
// a replayed tick cannot apply twice.
type ApplyEscalationEffectsInput struct {
	Request  domain.ApprovalRequest         `json:"request"`
	Sequence int64                          `json:"sequence"`
	Effects  []escalation.Effect            `json:"effects"`
	Progress application.EscalationProgress `json:"progress"`
}

// ApplyEscalationEffectsOutput is what the workflow arms after a tick.
// Stale reports the store refused the sequence (an older tick, or a
// request no longer pending), in which case nothing was recorded and
// nothing should be sent.
type ApplyEscalationEffectsOutput struct {
	Steps []application.NotificationStep `json:"steps"`
	Stale bool                           `json:"stale"`
}

// Escalation audit event types, one per executor effect.
const (
	EventLevelEntered        = "routing.level_entered"
	EventUrgencyRaised       = "routing.urgency_raised"
	EventDeferred            = "routing.deferred"
	EventRepeated            = "routing.repeated"
	EventEscalationExhausted = "routing.escalation_exhausted"
)

// LoadEscalationProgram reads the request's pinned path and team policies
// together with the request-condition facts the router pinned beside
// them, so the executor only ever reads facts and agrees with decide
// authorization by construction. A default queue request reports
// Escalated false.
func (a *Activities) LoadEscalationProgram(ctx context.Context, request domain.ApprovalRequest) (EscalationProgramOutput, error) {
	if a.escalation == nil {
		return EscalationProgramOutput{}, nil
	}
	logger := activity.GetLogger(ctx)
	fields := loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(request.OrganizationID), loggermdc.KeyApprovalRequestID: string(request.ID),
	})
	record, err := a.escalation.LoadEscalationProgram(ctx, application.LoadEscalationProgramInput{
		OrganizationID: request.OrganizationID, RequestID: request.ID,
	})
	if err != nil {
		return EscalationProgramOutput{}, err
	}
	if !record.Escalated {
		return EscalationProgramOutput{}, nil
	}
	// A revision written by a grammar this build does not know cannot be
	// run safely, and retrying will not change it.
	if record.PathDocumentVersion != routing.DocumentVersion {
		return EscalationProgramOutput{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("escalation path document version %d is not supported", record.PathDocumentVersion), "unsupported_document_version", nil)
	}
	var path routing.PathDocument
	if err := routing.Decode(record.PathDocument, &path); err != nil {
		return EscalationProgramOutput{}, temporal.NewNonRetryableApplicationError("decode escalation path: "+err.Error(), "invalid_document", err)
	}
	teams := make(map[domain.ID]routing.TeamPolicyDocument, len(record.TeamPolicies))
	for _, policy := range record.TeamPolicies {
		if policy.DocumentVersion != routing.DocumentVersion {
			return EscalationProgramOutput{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("team %s policy document version %d is not supported", policy.TeamID, policy.DocumentVersion), "unsupported_document_version", nil)
		}
		var document routing.TeamPolicyDocument
		if err := routing.Decode(policy.Document, &document); err != nil {
			return EscalationProgramOutput{}, temporal.NewNonRetryableApplicationError("decode team policy "+string(policy.TeamID)+": "+err.Error(), "invalid_document", err)
		}
		teams[policy.TeamID] = document
	}
	urgency := routing.Urgency(record.Urgency)
	if urgency == "" {
		urgency = routing.UrgencyStandard
	}
	program := escalation.ProgramFromDocuments(path, teams, urgency)
	for key, value := range record.Facts {
		program.Facts[key] = value
	}
	logger.Info(fmt.Sprintf("escalation program loaded - %s urgency, %d team policies", urgency, len(teams)), append(fields, "urgency", string(urgency), "team_policy_count", len(teams), "fact_count", len(program.Facts))...)
	return EscalationProgramOutput{Escalated: true, Program: program}, nil
}

// ApplyEscalationEffects records one tick and resolves the people its
// levels page. The audit events are written with the progress in one
// transaction guarded by the sequence, so a retry after the write
// committed reports Stale and sends nothing twice.
func (a *Activities) ApplyEscalationEffects(ctx context.Context, input ApplyEscalationEffectsInput) (ApplyEscalationEffectsOutput, error) {
	if a.escalation == nil {
		return ApplyEscalationEffectsOutput{}, temporal.NewNonRetryableApplicationError("escalation is not wired in this worker", "escalation_unavailable", nil)
	}
	request := input.Request
	logger := activity.GetLogger(ctx)
	fields := loggermdc.KeyValues(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID: string(request.OrganizationID), loggermdc.KeyApprovalRequestID: string(request.ID),
	})
	now := a.clock.Now()
	events, err := escalationAuditEvents(request, input.Effects, now)
	if err != nil {
		return ApplyEscalationEffectsOutput{}, err
	}
	result, err := a.escalation.RecordEscalationProgress(ctx, application.RecordEscalationProgressInput{
		OrganizationID: request.OrganizationID, RequestID: request.ID,
		Progress: input.Progress, Events: events, Now: now,
	})
	if err != nil {
		return ApplyEscalationEffectsOutput{}, err
	}
	if !result.Applied {
		logger.Info("escalation update skipped: request is no longer pending or update is stale", append(fields, "sequence", input.Sequence)...)
		return ApplyEscalationEffectsOutput{Stale: true}, nil
	}
	var entries []application.LevelEntry
	for _, effect := range input.Effects {
		if effect.Kind == escalation.EffectLevelEntered && len(effect.Targets) > 0 {
			entries = append(entries, application.LevelEntry{TeamID: effect.Scope, Targets: effect.Targets})
		}
	}
	if len(entries) == 0 {
		return ApplyEscalationEffectsOutput{}, nil
	}
	recipients, err := a.recipients.ResolveLevelRecipients(ctx, application.ResolveLevelRecipientsInput{
		OrganizationID: request.OrganizationID, RequestID: request.ID,
		Urgency: domain.NotificationUrgency(input.Progress.Urgency), Entries: entries,
	})
	if err != nil {
		return ApplyEscalationEffectsOutput{}, err
	}
	if err := a.recordNotified(ctx, request, recipients); err != nil {
		return ApplyEscalationEffectsOutput{}, err
	}
	if len(recipients.Recipients) == 0 {
		logger.Warn("escalation level has no reachable reviewers; no notifications scheduled", append(fields, "sequence", input.Sequence, "reason", "no_reachable_reviewers")...)
		return ApplyEscalationEffectsOutput{}, nil
	}
	userIDs := make([]domain.ID, 0, len(recipients.Recipients))
	membershipIDs := make([]string, 0, len(recipients.Recipients))
	for _, recipient := range recipients.Recipients {
		userIDs = append(userIDs, recipient.UserID)
		membershipIDs = append(membershipIDs, string(recipient.MembershipID))
	}
	rules, err := a.rules.ListRecipientRules(ctx, application.ListRecipientRulesInput{UserIDs: userIDs})
	if err != nil {
		return ApplyEscalationEffectsOutput{}, err
	}
	steps := BuildNotificationPlan(recipients, rules, request.DeadlineAt.Sub(now))
	logger.Info(fmt.Sprintf("escalation notification plan ready - %d recipients, %d delivery steps", len(recipients.Recipients), len(steps)), append(fields, "sequence", input.Sequence, "urgency", string(recipients.Urgency),
		"membership_ids", membershipIDs, "step_count", len(steps))...)
	return ApplyEscalationEffectsOutput{Steps: steps}, nil
}

// escalationAuditEvents turns a tick's effects into the request's audit
// trail. The team id is present only for team policy effects; the
// organization path's carry none.
func escalationAuditEvents(request domain.ApprovalRequest, effects []escalation.Effect, now time.Time) ([]domain.AuditEvent, error) {
	events := make([]domain.AuditEvent, 0, len(effects))
	for _, effect := range effects {
		eventType, data := escalationEventData(effect)
		if eventType == "" {
			continue
		}
		if effect.Scope != "" {
			data["team_id"] = string(effect.Scope)
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		events = append(events, domain.AuditEvent{
			ID: domain.ID(uuid.NewString()), OrganizationID: request.OrganizationID,
			EventType: eventType, ActorType: domain.ActorSystem,
			SubjectType: "approval_request", SubjectID: request.ID,
			Data: encoded, OccurredAt: now,
		})
	}
	return events, nil
}

func escalationEventData(effect escalation.Effect) (string, map[string]any) {
	switch effect.Kind {
	case escalation.EffectLevelEntered:
		targets := make([]map[string]string, 0, len(effect.Targets))
		for _, target := range effect.Targets {
			entry := map[string]string{"type": string(target.Type)}
			if target.ID != "" {
				entry["id"] = target.ID
			}
			targets = append(targets, entry)
		}
		return EventLevelEntered, map[string]any{
			"node_id": effect.NodeID, "level_ordinal": effect.LevelOrdinal, "level_count": effect.LevelCount,
			"urgency": string(effect.Urgency), "repeat": effect.Repeat, "targets": targets,
		}
	case escalation.EffectUrgencyRaised:
		// The machine raises urgency only ever from standard.
		return EventUrgencyRaised, map[string]any{
			"node_id": effect.NodeID, "from": string(routing.UrgencyStandard), "to": string(effect.Urgency),
		}
	case escalation.EffectDeferred:
		return EventDeferred, map[string]any{"node_id": effect.NodeID, "until": effect.Until}
	case escalation.EffectRepeat:
		return EventRepeated, map[string]any{"iteration": effect.Iteration, "after": effect.After.String()}
	case escalation.EffectExhausted:
		return EventEscalationExhausted, map[string]any{}
	}
	return "", nil
}
