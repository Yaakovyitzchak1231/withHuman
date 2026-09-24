package domain

import (
	"encoding/json"
	"time"
)

// Pipeline scope wire names. A pipeline belongs to the organization scope,
// whose blocks run before every agent section, or to one agent.
// An empty AgentSlug on pipeline types means the organization scope.
const (
	PipelineScopeOrganization = "organization"
	PipelineScopeAgent        = "agent"
)

// PipelineScopeName returns the wire name of the scope agentSlug selects.
func PipelineScopeName(agentSlug AgentSlug) string {
	if agentSlug == "" {
		return PipelineScopeOrganization
	}
	return PipelineScopeAgent
}

// ValidPipelineScope reports whether agentSlug addresses a pipeline scope:
// empty for the organization scope or a valid agent slug.
func ValidPipelineScope(agentSlug AgentSlug) bool {
	return agentSlug == "" || agentSlug.Valid()
}

// RequestApprovalPipelineRevision is one immutable ordered pipeline revision.
// An empty AgentSlug means the organization scope. IsActive and ArchivedAt
// are the only fields that may change after the revision is created; an
// archived revision is never active and stays frozen. DefaultEscalationPath is
// the escalation path key a request goes to when it falls through the
// pipeline or exits through a human outcome that names no path; empty
// leaves it to any authorized reviewer. The agent section's default
// takes precedence over the organization section's.
type RequestApprovalPipelineRevision struct {
	ID             ID        `json:"id"`
	OrganizationID ID        `json:"organization_id"`
	AgentSlug      AgentSlug `json:"agent_slug"`
	Revision       int64     `json:"revision"`
	IsActive       bool      `json:"is_active"`
	// BlockIDs lists every block of the revision in pre-order: a branch,
	// then the blocks it holds, each its own snapshot.
	BlockIDs              []ID       `json:"block_ids"`
	DefaultEscalationPath string     `json:"default_escalation_path,omitempty"`
	CreatedByActorID      ID         `json:"created_by_actor_id"`
	CreatedAt             time.Time  `json:"created_at"`
	ArchivedAt            *time.Time `json:"archived_at,omitempty"`
}

// RequestApprovalPipelineSummary is the history overview for one scope: the
// organization scope when AgentSlug is empty, one agent otherwise.
// Every live scope has exactly one active revision. An archived scope,
// one whose every revision was archived with its agent,
// has none and carries ArchivedAt instead.
type RequestApprovalPipelineSummary struct {
	AgentSlug       AgentSlug  `json:"agent_slug"`
	ActiveRevision  *int64     `json:"active_revision,omitempty"`
	LatestRevision  int64      `json:"latest_revision"`
	RevisionCount   int64      `json:"revision_count"`
	LatestCreatedAt time.Time  `json:"latest_created_at"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
}

// PipelineUse is one reference from an active pipeline revision to a
// shared object: a webhook endpoint or an escalation path. BlockKey names
// the block that carries the reference; it is empty when the pipeline
// names an escalation path as its default. Retiring a path or deleting an
// endpoint is refused while any use exists, so the used object is listed
// back to the administrator.
type PipelineUse struct {
	PipelineID ID        `json:"pipeline_id"`
	AgentSlug  AgentSlug `json:"agent_slug,omitempty"`
	Revision   int64     `json:"revision"`
	BlockKey   string    `json:"block_key,omitempty"`
}

// RequestApprovalBlock is an immutable executable block snapshot. ContentHash
// covers every executable and author-visible content field, but excludes the
// snapshot identity and creation provenance.
type RequestApprovalBlock struct {
	ID               ID              `json:"id"`
	OrganizationID   ID              `json:"organization_id"`
	BlockKey         string          `json:"block_key"`
	Name             string          `json:"name"`
	BlockType        string          `json:"block_type"`
	ConfigVersion    int             `json:"config_version"`
	Config           json.RawMessage `json:"config"`
	TimeoutMS        int             `json:"timeout_ms"`
	MaxAttempts      int             `json:"max_attempts"`
	ContentHash      string          `json:"content_hash"`
	CreatedByActorID ID              `json:"created_by_actor_id"`
	CreatedAt        time.Time       `json:"created_at"`
}

// PipelineSuspension is the durable state of a request approval pipeline
// paused before an asynchronous block. The synchronous request path writes
// it with the pending request; a worker resumes the pipeline from
// ResumePosition with PreviousMetadata and marks it resumed exactly once.
// ResumePosition is a pre-order position across both sections; resuming
// re-enters every branch that holds it without evaluating their conditions
// again. Pipeline IDs are the pinned revisions; an empty ID means that scope
// had no active revision. ResumedAt is nil while the suspension is open.
type PipelineSuspension struct {
	RequestID              ID
	OrganizationID         ID
	EvaluationID           ID
	OrganizationPipelineID ID
	AgentPipelineID        ID
	ResumePosition         int
	PreviousMetadata       map[string]json.RawMessage
	AttemptedBlockCount    int
	TotalAttempts          int
	TotalCost              uint64
	StartedAt              time.Time
	SuspendedAt            time.Time
	ResumedAt              *time.Time
}

// Open reports whether the suspension still awaits its resume.
func (suspension PipelineSuspension) Open() bool { return suspension.ResumedAt == nil }
