package domain

import (
	"encoding/json"
	"time"
)

type ApprovalStatus string
type DecisionSource string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
	ApprovalExpired  ApprovalStatus = "expired"
	// ApprovalCancelled is the instance that opened the request withdrawing
	// it before anyone decided: its runtime died, was interrupted, or gave
	// up waiting. Reviewers stop seeing the request and adapters treat it
	// as a denial.
	ApprovalCancelled ApprovalStatus = "cancelled"
)

const (
	DecisionSourceHuman    DecisionSource = "human"
	DecisionSourcePipeline DecisionSource = "pipeline"
	DecisionSourceSystem   DecisionSource = "system"
	// DecisionSourceAgent is the requesting instance withdrawing its own
	// request; the only terminal outcome an agent can produce.
	DecisionSourceAgent DecisionSource = "agent"
)

func (s ApprovalStatus) Terminal() bool {
	return s == ApprovalApproved || s == ApprovalDenied || s == ApprovalExpired || s == ApprovalCancelled
}

type ApprovalRequest struct {
	Timeout           string    `json:"timeout"`
	ID                ID        `json:"id"`
	OrganizationID    ID        `json:"organization_id"`
	AgentID           ID        `json:"-"`
	AgentInstanceID   ID        `json:"agent_instance_id"`
	AgentName         string    `json:"agent_name"`
	AgentInstanceName string    `json:"agent_instance_name"`
	AgentSlug         AgentSlug `json:"agent_slug"`
	// RequesterMembershipID is the member a personal instance acts for,
	// copied from the instance when the request is made; empty for a
	// service agent's request. RequesterDisplayName is that member's
	// name, filled by product reads only.
	RequesterMembershipID ID     `json:"requester_membership_id,omitempty"`
	RequesterDisplayName  string `json:"requester_display_name,omitempty"`
	Tool                  string `json:"tool"`
	// Server is the MCP server that defines the tool, as the adapter
	// reported it; empty for a runtime's built-in tools.
	Server         string          `json:"server,omitempty"`
	Arguments      json.RawMessage `json:"arguments"`
	AgentReasoning string          `json:"agent_reasoning,omitempty"`
	Context        json.RawMessage `json:"context,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
	SemanticHash   string          `json:"semantic_hash"`
	Status         ApprovalStatus  `json:"status"`
	DeadlineAt     time.Time       `json:"deadline_at"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Decision       *Decision       `json:"decision,omitempty"`
	// Claim is the open claim on a pending request, populated only by
	// product reads. AAP representations never copy it.
	Claim *Claim `json:"claim,omitempty"`
}

type Decision struct {
	ExpiresAt           *time.Time     `json:"expires_at,omitempty"`
	ID                  ID             `json:"id"`
	RequestID           ID             `json:"request_id"`
	OrganizationID      ID             `json:"organization_id"`
	MembershipID        ID             `json:"membership_id,omitempty"`
	Source              DecisionSource `json:"-"`
	Status              ApprovalStatus `json:"status"`
	Note                string         `json:"note,omitempty"`
	Channel             string         `json:"channel"`
	ReviewerDisplayName string         `json:"reviewer_display_name,omitempty"`
	ReviewerEmail       string         `json:"reviewer_email,omitempty"`
	Auth                AuthContext    `json:"authentication"`
	IdempotencyKey      string         `json:"idempotency_key"`
	DecidedAt           time.Time      `json:"decided_at"`
}

type DecisionCommand struct {
	OperationID    ID
	RequestID      ID
	OrganizationID ID
	Principal      Principal
	Status         ApprovalStatus
	Note           string
	Channel        string
	IdempotencyKey string
	// OutsideRouting is the reviewer's explicit acknowledgement that the
	// decision is made outside the request's routing (break glass). A
	// holder of request.decide.unrouted who is not targeted is refused
	// without it, so a keyboard shortcut never breaks glass by accident.
	OutsideRouting bool
}

// ApprovalReviewer is a person added to a pending request's routing after
// the fact by someone the request was routed to. Adding widens who may
// decide; it never removes anyone.
type ApprovalReviewer struct {
	RequestID           ID        `json:"request_id"`
	MembershipID        ID        `json:"membership_id"`
	DisplayName         string    `json:"display_name"`
	AddedByMembershipID ID        `json:"added_by_membership_id"`
	AddedByDisplayName  string    `json:"added_by_display_name"`
	CreatedAt           time.Time `json:"created_at"`
}

type OperationStatus string

const (
	OperationPending  OperationStatus = "pending"
	OperationApplied  OperationStatus = "applied"
	OperationRejected OperationStatus = "rejected"
)

type DecisionOperation struct {
	ID             ID              `json:"id"`
	OrganizationID ID              `json:"organization_id"`
	RequestID      ID              `json:"request_id"`
	IntentHash     string          `json:"-"`
	Status         OperationStatus `json:"status"`
	ErrorCode      string          `json:"error_code,omitempty"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	Decision       *Decision       `json:"decision,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
}

type AuditEvent struct {
	Sequence       int64           `json:"sequence"`
	ID             ID              `json:"id"`
	OrganizationID ID              `json:"organization_id"`
	EventType      string          `json:"event_type"`
	ActorType      ActorType       `json:"actor_type"`
	ActorID        ID              `json:"actor_id,omitempty"`
	SubjectType    string          `json:"subject_type"`
	SubjectID      ID              `json:"subject_id"`
	Data           json.RawMessage `json:"data"`
	OccurredAt     time.Time       `json:"occurred_at"`
}
