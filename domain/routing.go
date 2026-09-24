package domain

import (
	"encoding/json"
	"time"
)

// EscalationPathRevision is one immutable escalation path revision.
// IsActive and ArchivedAt are the only fields that may change after the
// revision is created: archiving the path stamps every revision at once
// and deactivates the active one.
type EscalationPathRevision struct {
	ID               ID
	OrganizationID   ID
	PathKey          string
	Revision         int64
	IsActive         bool
	Name             string
	Document         json.RawMessage
	DocumentVersion  int
	CreatedByActorID ID
	CreatedAt        time.Time
	ArchivedAt       *time.Time
}

// EscalationPathReference names one active path revision, enough for a
// refusal to point at the path and for a page to link to it.
type EscalationPathReference struct {
	PathKey  string
	Name     string
	Revision int64
}

// EscalationPathSummary describes one path key's revision lineage.
// ActiveRevision is nil when no revision is active; ArchivedAt is set
// when the path was archived.
type EscalationPathSummary struct {
	PathKey         string
	Name            string
	ActiveRevision  *int64
	LatestRevision  int64
	RevisionCount   int64
	LatestCreatedAt time.Time
	ArchivedAt      *time.Time
}

type RoutingOutcome string

const (
	// RoutingEscalated means the request was handed to an escalation path:
	// the one its terminal block named, or the pipeline default.
	RoutingEscalated RoutingOutcome = "escalated"
	// RoutingDefaultQueue means no path applies: any authorized reviewer
	// may decide from the queue.
	RoutingDefaultQueue RoutingOutcome = "default_queue"
)

// Routing reason codes. The first two accompany escalated; the rest
// explain a default_queue outcome.
const (
	RoutingReasonBlockEscalation = "block_escalation"
	RoutingReasonPipelineDefault = "pipeline_default"
	RoutingReasonNoEscalation    = "no_escalation"
	RoutingReasonPathInactive    = "path_inactive"
	RoutingReasonEvaluationError = "evaluation_error"
)

// ApprovalRequestRouting pins where one pending request was handed when
// it entered human review. Later configuration changes never re-route it.
// PipelineRevisionID and BlockID record which pipeline revision and block
// named the path; both are empty for a default_queue outcome.
type ApprovalRequestRouting struct {
	RequestID                ID
	OrganizationID           ID
	Outcome                  RoutingOutcome
	Reason                   string
	EscalationPathKey        string
	EscalationPathRevisionID ID
	PipelineRevisionID       ID
	BlockID                  ID
	Urgency                  string
	// TeamPolicies pins the active policy revision of every team the
	// pinned path can reach for this request that had one at routing
	// time. A team absent here is notified all at once when a level
	// reaches it.
	TeamPolicies []ApprovalRoutingTeamPolicy
	// Facts is the result of every request condition in the pinned path
	// and the pinned team policies, keyed "<scope>/<condition id>" (scope
	// "" for the path, the team id for a policy), evaluated once when the
	// request was routed. Who may decide and the escalation executor both
	// read these instead of re-evaluating the request.
	Facts     map[string]bool
	CreatedAt time.Time
}

// TeamEscalationPolicyRevision is one immutable team escalation policy
// revision. IsActive is the only field that may change after creation.
type TeamEscalationPolicyRevision struct {
	ID               ID
	OrganizationID   ID
	TeamID           ID
	Revision         int64
	IsActive         bool
	Document         json.RawMessage
	DocumentVersion  int
	CreatedByActorID ID
	CreatedAt        time.Time
}

// TeamEscalationPolicyReference names one active team policy revision,
// enough for a refusal to point at the team and for a page to link to it.
type TeamEscalationPolicyReference struct {
	TeamID   ID
	TeamName string
	Revision int64
}

// TeamEscalationPolicyStatus summarizes a team's policy lineage. Both
// fields are nil when the team has never had a policy; ActiveRevision
// alone is nil when no revision is active, whether the latest was saved
// as a draft or the policy was archived.
type TeamEscalationPolicyStatus struct {
	ActiveRevision *int64 `json:"active_revision"`
	LatestRevision *int64 `json:"latest_revision"`
}

// ApprovalRoutingTeamPolicy pins which team policy revision an escalated
// request runs for one team target of its pinned path.
type ApprovalRoutingTeamPolicy struct {
	TeamID           ID
	PolicyRevisionID ID
	Revision         int64
}
