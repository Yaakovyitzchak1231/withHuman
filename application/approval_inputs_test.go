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
