package application

import (
	"fmt"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// The cap admits the longest segment a pipeline can write: every budgeted
// block continuing, one terminal event, and the envelope around them.
func TestApprovalInitializationAdmitsTheLongestPipelineSegment(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	request := domain.ApprovalRequest{
		Timeout: "1h", ID: "request-1", OrganizationID: "organization-1", AgentID: "agent-1", AgentInstanceID: "instance-1",
		AgentSlug: "release", Tool: "deploy", Arguments: []byte(`{}`), IdempotencyKey: "request-key-1",
		SemanticHash: "semantic-hash-1", Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	event := func(index int, eventType string, actor domain.ActorType, actorID domain.ID) domain.AuditEvent {
		return domain.AuditEvent{
			ID: domain.ID(fmt.Sprintf("audit-%d", index)), OrganizationID: request.OrganizationID, EventType: eventType,
			ActorType: actor, ActorID: actorID, SubjectType: "approval_request", SubjectID: request.ID,
			Data: []byte(`{}`), OccurredAt: now,
		}
	}
	initialization := func(total int) InitializeApprovalInput {
		events := []domain.AuditEvent{
			event(0, "approval.created", domain.ActorAgent, request.AgentID),
			event(1, "request_approval_pipeline.started", domain.ActorSystem, ""),
		}
		for len(events) < total-2 {
			events = append(events, event(len(events), "request_approval_block.evaluated", domain.ActorSystem, ""))
		}
		events = append(events,
			event(len(events), "request_approval_pipeline.completed", domain.ActorSystem, ""),
			event(len(events)+1, "routing.evaluated", domain.ActorSystem, ""))
		return InitializeApprovalInput{Request: request, AuditEvents: events}
	}
	if err := initialization(maximumApprovalAuditEvents).Validate(); err != nil {
		t.Fatalf("the longest segment was refused: %v", err)
	}
	if err := initialization(maximumApprovalAuditEvents + 1).Validate(); err == nil {
		t.Fatal("a segment past the cap was accepted")
	}
}

// An agent_owner snapshot names its owner and the fallback path key it
// pins; only it names an owner, and only the others explain a fallback.
func TestApprovalInitializationRoutingShapes(t *testing.T) {
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	request := domain.ApprovalRequest{
		Timeout: "1h", ID: "request-1", OrganizationID: "organization-1", AgentID: "agent-1", AgentInstanceID: "instance-1",
		AgentSlug: "claude-code", Tool: "Bash", Arguments: []byte(`{}`), IdempotencyKey: "request-key-1",
		SemanticHash: "semantic-hash-1", Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	routed := func(routing domain.ApprovalRequestRouting) error {
		routing.RequestID, routing.OrganizationID, routing.Urgency, routing.CreatedAt = request.ID, request.OrganizationID, "standard", now
		if routing.Reason == "" {
			routing.Reason = domain.RoutingReasonPipelineDefault
		}
		events := make([]domain.AuditEvent, 0, 4)
		for index, step := range []struct {
			eventType string
			actor     domain.ActorType
			actorID   domain.ID
		}{
			{"approval.created", domain.ActorAgent, request.AgentID},
			{"request_approval_pipeline.started", domain.ActorSystem, ""},
			{"request_approval_pipeline.completed", domain.ActorSystem, ""},
			{"routing.evaluated", domain.ActorSystem, ""},
		} {
			events = append(events, domain.AuditEvent{
				ID: domain.ID(fmt.Sprintf("audit-%d", index)), OrganizationID: request.OrganizationID, EventType: step.eventType,
				ActorType: step.actor, ActorID: step.actorID, SubjectType: "approval_request", SubjectID: request.ID,
				Data: []byte(`{}`), OccurredAt: now,
			})
		}
		return InitializeApprovalInput{Request: request, Routing: &routing, AuditEvents: events}.Validate()
	}
	for name, test := range map[string]struct {
		routing domain.ApprovalRequestRouting
		valid   bool
	}{
		"owner with a pinned fallback": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingAgentOwner, AgentOwnerMembershipID: "owner-1",
			EscalationPathKey: "platform", EscalationPathRevisionID: "path-revision-1",
		}, true},
		"owner with an inactive fallback": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingAgentOwner, AgentOwnerMembershipID: "owner-1", EscalationPathKey: "platform",
		}, true},
		"owner without a fallback path": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingAgentOwner, AgentOwnerMembershipID: "owner-1",
		}, true},
		"owner with a pinned revision but no path key": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingAgentOwner, AgentOwnerMembershipID: "owner-1", EscalationPathRevisionID: "path-revision-1",
		}, false},
		"owner outcome without an owner": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingAgentOwner, EscalationPathKey: "platform",
		}, false},
		"owner with a fallback reason": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingAgentOwner, AgentOwnerMembershipID: "owner-1", EscalationPathKey: "platform",
			AgentOwnerFallback: domain.AgentOwnerFallbackNoOwner,
		}, false},
		"escalated naming an owner": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingEscalated, AgentOwnerMembershipID: "owner-1",
			EscalationPathKey: "platform", EscalationPathRevisionID: "path-revision-1",
		}, false},
		"escalated fallback with its reason": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingEscalated, AgentOwnerFallback: domain.AgentOwnerFallbackCannotDecide,
			EscalationPathKey: "platform", EscalationPathRevisionID: "path-revision-1",
		}, true},
		"queue fallback with its reason": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingDefaultQueue, Reason: domain.RoutingReasonPathInactive,
			AgentOwnerFallback: domain.AgentOwnerFallbackNoOwner,
		}, true},
		"an unknown fallback reason": {domain.ApprovalRequestRouting{
			Outcome: domain.RoutingDefaultQueue, Reason: domain.RoutingReasonPathInactive, AgentOwnerFallback: "asleep",
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if err := routed(test.routing); (err == nil) != test.valid {
				t.Fatalf("valid = %v, error = %v", test.valid, err)
			}
		})
	}
}
