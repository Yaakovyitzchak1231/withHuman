package reviewrouting

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
	"github.com/withHumanAI/withHuman/internal/escalation"
	"github.com/withHumanAI/withHuman/routing"
)

// escalationPathReader is what pinning reads: the path's active revision
// and, for the teams that revision targets, their active policies, and
// for an agent-owner route whether the owner can take the request.
type escalationPathReader interface {
	GetActiveEscalationPathRevision(context.Context, application.GetActiveEscalationPathRevisionInput) (domain.EscalationPathRevision, error)
	GetActiveTeamEscalationPolicies(context.Context, application.GetActiveTeamEscalationPoliciesInput) ([]domain.TeamEscalationPolicyRevision, error)
	application.AgentOwnerResolver
}

// EvaluationService pins which escalation path a request that needs a
// human goes to. The pipeline has already resolved the route (the
// terminal block's, else the pipeline default); this service pins the
// path's active revision so later edits never re-route an in-flight
// request, and for an agent-owner route decides whether the owner takes
// it or the path does. Every internal failure falls back to the default queue:
// routing can delay attention but must never fail or block an approval
// request.
type EvaluationService struct {
	paths  escalationPathReader
	clock  application.Clock
	logger *slog.Logger
}

func NewEvaluationService(paths escalationPathReader, clock application.Clock, logger *slog.Logger) *EvaluationService {
	return &EvaluationService{paths: paths, clock: clock, logger: logger}
}

// Decorate pins the routing outcome onto a pending initialization: the
// snapshot rides the same transaction as the request and a
// routing.evaluated audit event carries the evidence. Non-pending
// initializations pass through untouched.
func (service *EvaluationService) Decorate(ctx context.Context, initialization application.InitializeApprovalInput) application.InitializeApprovalInput {
	// A suspended pipeline has not decided yet; the worker routes the
	// request after the resume, and only if it still needs a human.
	if initialization.Request.Status != domain.ApprovalPending || initialization.Suspension != nil {
		return initialization
	}
	snapshot, event, ok := service.Evaluate(ctx, initialization.Request, initialization.HumanReview)
	initialization.Routing = &snapshot
	if ok {
		initialization.AuditEvents = append(initialization.AuditEvents, event)
	}
	return initialization
}

// Evaluate resolves where a pending request goes and the audit event that
// explains it. The event is absent (ok false) only when it could not be
// encoded; the snapshot is always usable.
func (service *EvaluationService) Evaluate(ctx context.Context, request domain.ApprovalRequest, route *application.HumanReviewRoute) (snapshot domain.ApprovalRequestRouting, event domain.AuditEvent, ok bool) {
	now := service.clock.Now()
	snapshot, detail := service.evaluate(ctx, request, route)
	snapshot.RequestID = request.ID
	snapshot.OrganizationID = request.OrganizationID
	snapshot.CreatedAt = now

	detail["routing_evaluation_id"] = uuid.NewString()
	detail["outcome"] = snapshot.Outcome
	detail["reason"] = snapshot.Reason
	detail["urgency"] = snapshot.Urgency
	data, err := json.Marshal(detail)
	if err != nil {
		service.logger.WarnContext(ctx, "routing audit event dropped", "approval_request_id", request.ID, "error", err)
		return snapshot, domain.AuditEvent{}, false
	}
	return snapshot, domain.AuditEvent{
		ID: domain.ID(uuid.NewString()), OrganizationID: request.OrganizationID,
		EventType: "routing.evaluated", ActorType: domain.ActorSystem,
		SubjectType: "approval_request", SubjectID: request.ID, Data: data, OccurredAt: now,
	}, true
}

// evaluate resolves the routed outcome; identifiers only, never observed
// request values, so the detail map is safe for the audit trail.
func (service *EvaluationService) evaluate(ctx context.Context, request domain.ApprovalRequest, route *application.HumanReviewRoute) (domain.ApprovalRequestRouting, map[string]any) {
	detail := map[string]any{}
	ownerID, ownerFallback := domain.ID(""), ""
	fallback := func(reason string) domain.ApprovalRequestRouting {
		return domain.ApprovalRequestRouting{
			Outcome: domain.RoutingDefaultQueue, Reason: reason, Urgency: string(routing.UrgencyStandard),
			AgentOwnerFallback: ownerFallback,
		}
	}
	if route != nil && route.PipelineRevisionID != "" {
		detail["pipeline_revision_id"] = route.PipelineRevisionID
	}
	if route == nil || (route.PathKey == "" && !route.AskAgentOwner) {
		return fallback(domain.RoutingReasonNoEscalation), detail
	}
	if (route.PathKey != "" && !routing.ValidPathKey(route.PathKey)) || (route.Reason != domain.RoutingReasonBlockEscalation && route.Reason != domain.RoutingReasonPipelineDefault) {
		service.logger.WarnContext(ctx, "pipeline handed an unusable escalation route; request falls to the default queue",
			"approval_request_id", request.ID, "reason", route.Reason)
		return fallback(domain.RoutingReasonEvaluationError), detail
	}
	if route.PathKey != "" {
		detail["escalation_path_key"] = route.PathKey
	}
	if route.BlockID != "" {
		detail["block_id"] = route.BlockID
	}
	if route.AskAgentOwner {
		ownerID, ownerFallback = service.agentOwner(ctx, request)
		if ownerID != "" {
			detail["agent_owner_membership_id"] = ownerID
		} else if ownerFallback != "" {
			detail["agent_owner_fallback"] = ownerFallback
		}
	}
	// ownerRoute hands the request to the owner, pinning the fallback path
	// beside it as far as it could be read: its key always, its revision
	// and facts when the path had an active revision.
	ownerRoute := func(snapshot domain.ApprovalRequestRouting) domain.ApprovalRequestRouting {
		snapshot.Outcome, snapshot.AgentOwnerMembershipID = domain.RoutingAgentOwner, ownerID
		snapshot.Reason, snapshot.EscalationPathKey = route.Reason, route.PathKey
		snapshot.PipelineRevisionID, snapshot.Urgency = route.PipelineRevisionID, string(routing.UrgencyStandard)
		if route.Reason == domain.RoutingReasonBlockEscalation {
			snapshot.BlockID = route.BlockID
		}
		return snapshot
	}
	// An owner route without a fallback path: the owner takes it, or,
	// without one able to decide, any authorized reviewer.
	if route.PathKey == "" {
		if ownerID != "" {
			return ownerRoute(domain.ApprovalRequestRouting{}), detail
		}
		return fallback(domain.RoutingReasonNoEscalation), detail
	}
	path, err := service.paths.GetActiveEscalationPathRevision(ctx, application.GetActiveEscalationPathRevisionInput{
		OrganizationID: request.OrganizationID,
		PathKey:        route.PathKey,
	})
	if errors.Is(err, domain.ErrNotFound) {
		if ownerID != "" {
			service.logger.WarnContext(ctx, "fallback escalation path has no active revision; the agent's owner takes the request without one",
				"approval_request_id", request.ID, "escalation_path_key", route.PathKey)
			return ownerRoute(domain.ApprovalRequestRouting{}), detail
		}
		service.logger.WarnContext(ctx, "escalation path has no active revision; request falls to the default queue",
			"approval_request_id", request.ID, "escalation_path_key", route.PathKey)
		return fallback(domain.RoutingReasonPathInactive), detail
	}
	if err != nil {
		if ownerID != "" {
			service.logger.WarnContext(ctx, "fallback escalation path unavailable; the agent's owner takes the request without one",
				"approval_request_id", request.ID, "escalation_path_key", route.PathKey, "error", err)
			return ownerRoute(domain.ApprovalRequestRouting{}), detail
		}
		service.logger.WarnContext(ctx, "escalation path unavailable; request falls to the default queue",
			"approval_request_id", request.ID, "escalation_path_key", route.PathKey, "error", err)
		return fallback(domain.RoutingReasonEvaluationError), detail
	}
	detail["escalation_path_revision_id"] = path.ID
	detail["escalation_path_revision"] = path.Revision
	snapshot := domain.ApprovalRequestRouting{
		Outcome: domain.RoutingEscalated, Reason: route.Reason,
		EscalationPathKey: route.PathKey, EscalationPathRevisionID: path.ID,
		PipelineRevisionID: route.PipelineRevisionID, Urgency: string(routing.UrgencyStandard),
		Facts: map[string]bool{}, AgentOwnerFallback: ownerFallback,
	}
	if route.Reason == domain.RoutingReasonBlockEscalation {
		snapshot.BlockID = route.BlockID
	}
	if ownerID != "" {
		snapshot = ownerRoute(snapshot)
	}
	// The request conditions are evaluated exactly once, here, and pinned
	// with the snapshot: who may decide and the executor both read the
	// facts, so they can never disagree. An unreadable document pins the
	// path with no facts and no team policies, which the executor refuses
	// to run and only a break-glass reviewer can decide.
	reachableTargetIDs := make([]string, 0)
	var document routing.PathDocument
	if err := routing.Decode(path.Document, &document); err != nil {
		service.logger.WarnContext(ctx, "pinned escalation path document is unreadable; no facts or team policies pinned",
			"approval_request_id", request.ID, "escalation_path_key", path.PathKey, "error", err)
	} else {
		service.evaluateFacts(ctx, request, escalation.CollectRequestConditions("", document.Nodes), snapshot.Facts)
		reachable := document.ReachableTargets(func(conditionID string) bool {
			return snapshot.Facts[escalation.FactKey("", conditionID)]
		})
		for _, target := range reachable {
			reachableTargetIDs = append(reachableTargetIDs, string(target.Type)+":"+target.ID)
		}
		snapshot.TeamPolicies = service.pinTeamPolicies(ctx, request, path, reachable, snapshot.Facts)
	}
	detail["fact_count"] = len(snapshot.Facts)
	detail["reachable_targets"] = reachableTargetIDs
	teamPolicies := make([]map[string]any, 0, len(snapshot.TeamPolicies))
	for _, policy := range snapshot.TeamPolicies {
		teamPolicies = append(teamPolicies, map[string]any{
			"team_id": policy.TeamID, "policy_revision_id": policy.PolicyRevisionID, "revision": policy.Revision,
		})
	}
	detail["team_policies"] = teamPolicies
	return snapshot, detail
}

// agentOwner decides whether an agent-owner route reaches the owner: the
// owner of the personal instance that raised the request, while their
// membership is active and they hold request.decide for it. Otherwise it
// names why the fallback takes the request. A lookup failure is logged and
// sends the request to the fallback with no reason, because routing never
// fails a request and never waits on an owner it could not confirm.
func (service *EvaluationService) agentOwner(ctx context.Context, request domain.ApprovalRequest) (domain.ID, string) {
	if request.RequesterMembershipID == "" {
		return "", domain.AgentOwnerFallbackNoOwner
	}
	standing, err := service.paths.GetAgentOwnerStanding(ctx, application.GetAgentOwnerStandingInput{
		OrganizationID: request.OrganizationID, MembershipID: request.RequesterMembershipID, RequestID: request.ID,
	})
	switch {
	case err != nil:
		service.logger.WarnContext(ctx, "agent owner unavailable; request goes to the fallback path",
			"approval_request_id", request.ID, "error", err)
		return "", ""
	case !standing.Active:
		return "", domain.AgentOwnerFallbackInactive
	case !standing.CanDecide:
		return "", domain.AgentOwnerFallbackCannotDecide
	}
	return request.RequesterMembershipID, ""
}

// evaluateFacts evaluates request conditions against the request and
// records each result under its scoped key. The agent the conditions
// read is the one pinned on the request, never the agent's current row,
// so the facts are as static as the request itself. A condition that
// fails to evaluate reads false so its branch never fires; the warning is
// the operator's signal to fix the document.
func (service *EvaluationService) evaluateFacts(ctx context.Context, request domain.ApprovalRequest, conditions []escalation.ScopedCondition, into map[string]bool) {
	if len(conditions) == 0 {
		return
	}
	agent := domain.Agent{ID: request.AgentID, Name: request.AgentName, AgentSlug: request.AgentSlug}
	for _, scoped := range conditions {
		matched, err := approvalpipeline.EvaluateRequestCondition(ctx, scoped.Condition, request, agent)
		if err != nil {
			service.logger.WarnContext(ctx, "escalation request condition failed; its branch reads false",
				"approval_request_id", request.ID, "condition_id", scoped.Condition.ID, "team_id", string(scoped.Scope), "error", err)
			matched = false
		}
		into[escalation.FactKey(scoped.Scope, scoped.Condition.ID)] = matched
	}
}

// pinTeamPolicies resolves the active policy of every team the pinned
// path reaches for this request, so later policy edits never re-route
// the request either, and evaluates each policy's own request conditions
// into the facts. A team without an active policy is simply not pinned:
// a level reaching it notifies every member at once. A policy whose
// document does not decode is still pinned, without facts, so the
// executor's own refusal surfaces the problem. Any lookup failure pins
// no team rows and keeps the path pin, because routing never fails a
// request.
func (service *EvaluationService) pinTeamPolicies(ctx context.Context, request domain.ApprovalRequest, path domain.EscalationPathRevision, reachable []routing.Target, facts map[string]bool) []domain.ApprovalRoutingTeamPolicy {
	teamIDs := make([]domain.ID, 0)
	for _, target := range reachable {
		if target.Type == routing.TargetTeam {
			teamIDs = append(teamIDs, domain.ID(target.ID))
		}
	}
	if len(teamIDs) == 0 {
		return nil
	}
	revisions, err := service.paths.GetActiveTeamEscalationPolicies(ctx, application.GetActiveTeamEscalationPoliciesInput{
		OrganizationID: request.OrganizationID, TeamIDs: teamIDs,
	})
	if err != nil {
		service.logger.WarnContext(ctx, "team escalation policies unavailable; no team policies pinned",
			"approval_request_id", request.ID, "escalation_path_key", path.PathKey, "error", err)
		return nil
	}
	pinned := make([]domain.ApprovalRoutingTeamPolicy, 0, len(revisions))
	for _, revision := range revisions {
		pinned = append(pinned, domain.ApprovalRoutingTeamPolicy{
			TeamID: revision.TeamID, PolicyRevisionID: revision.ID, Revision: revision.Revision,
		})
		var document routing.TeamPolicyDocument
		if err := routing.Decode(revision.Document, &document); err != nil {
			service.logger.WarnContext(ctx, "pinned team escalation policy document is unreadable; no facts pinned for it",
				"approval_request_id", request.ID, "team_id", revision.TeamID, "error", err)
			continue
		}
		service.evaluateFacts(ctx, request, escalation.CollectRequestConditions(escalation.Scope(revision.TeamID), document.Nodes), facts)
	}
	return pinned
}
