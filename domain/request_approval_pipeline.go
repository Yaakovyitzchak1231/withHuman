package domain

import (
	"encoding/json"
	"regexp"
	"time"
)

// EntryPipelineKey is the reserved key of the pipeline every request
// starts in. It is seeded with the organization, can never be archived,
// and can never be a delegate's target.
const EntryPipelineKey = "entry"

// MaximumDelegationDepth bounds how many delegate blocks a request may
// pass through, the entry pipeline being depth zero. Activation refuses
// an active graph deeper than this, and the executor refuses to flatten
// one, so a run never recurses without bound.
const MaximumDelegationDepth = 8

// MaximumEffectivePipelineBlocks bounds the flattened effective pipeline:
// the entry revision's blocks plus every delegated pipeline's blocks,
// counted once per delegate block that names it. Every position must fit
// an integer column, and every run allocates one entry per block.
const MaximumEffectivePipelineBlocks = 10_000

var pipelineKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidPipelineKey reports whether key has the shape of a pipeline key.
func ValidPipelineKey(key string) bool {
	return pipelineKeyPattern.MatchString(key)
}

// RequestApprovalPipelineRevision is one immutable ordered pipeline
// revision of the pipeline PipelineKey names. IsActive and ArchivedAt are
// the only fields that may change after the revision is created; an
// archived revision is never active and stays frozen. Name is the
// pipeline's display name as of this revision. DefaultEscalationPath is
// the escalation path key a request goes to when it runs out of this
// pipeline or exits it through a human outcome that names no path; empty
// defers to the pipelines that delegated into this one, nearest first,
// and with none of those the request waits for any authorized reviewer.
type RequestApprovalPipelineRevision struct {
	ID             ID     `json:"id"`
	OrganizationID ID     `json:"organization_id"`
	PipelineKey    string `json:"pipeline_key"`
	Name           string `json:"name"`
	Revision       int64  `json:"revision"`
	IsActive       bool   `json:"is_active"`
	// BlockIDs lists every block of the revision in pre-order: a branch,
	// then the blocks it holds, each its own snapshot.
	BlockIDs              []ID       `json:"block_ids"`
	DefaultEscalationPath string     `json:"default_escalation_path,omitempty"`
	CreatedByActorID      ID         `json:"created_by_actor_id"`
	CreatedAt             time.Time  `json:"created_at"`
	ArchivedAt            *time.Time `json:"archived_at,omitempty"`
}

// RequestApprovalPipelineSummary is the history overview of one pipeline
// key. Every live pipeline has exactly one active revision. An archived
// pipeline has none and carries ArchivedAt instead. Name is the active
// revision's, or the latest revision's for an archived pipeline.
type RequestApprovalPipelineSummary struct {
	PipelineKey     string     `json:"pipeline_key"`
	Name            string     `json:"name"`
	ActiveRevision  *int64     `json:"active_revision,omitempty"`
	LatestRevision  int64      `json:"latest_revision"`
	RevisionCount   int64      `json:"revision_count"`
	LatestCreatedAt time.Time  `json:"latest_created_at"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
}

// PipelineUse is one reference from an active pipeline revision to a
// shared object: a webhook endpoint, an escalation path, or another
// pipeline a delegate block names. BlockKey names the block that carries
// the reference; it is empty when the pipeline names an escalation path
// as its default. Archiving a path, an endpoint, or a pipeline is refused
// while any use exists, so the used object is listed back to the
// administrator.
type PipelineUse struct {
	PipelineID  ID     `json:"pipeline_id"`
	PipelineKey string `json:"pipeline_key"`
	Revision    int64  `json:"revision"`
	BlockKey    string `json:"block_key,omitempty"`
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
// ResumePosition is a pre-order position across the effective pipeline;
// resuming re-enters every branch and delegate that holds it without
// evaluating their conditions again. PinnedRevisions maps every pipeline
// key of the effective pipeline, entered or not, to the immutable revision
// the run selected; the entry key is always present, and resuming flattens
// the same revisions into the same positions. ResumedAt is nil while the
// suspension is open.
type PipelineSuspension struct {
	RequestID           ID
	OrganizationID      ID
	EvaluationID        ID
	PinnedRevisions     map[string]ID
	ResumePosition      int
	PreviousMetadata    map[string]json.RawMessage
	AttemptedBlockCount int
	TotalAttempts       int
	TotalCost           uint64
	StartedAt           time.Time
	SuspendedAt         time.Time
	ResumedAt           *time.Time
}

// Open reports whether the suspension still awaits its resume.
func (suspension PipelineSuspension) Open() bool { return suspension.ResumedAt == nil }

// EntryPipelineID is the pinned revision of the entry pipeline.
func (suspension PipelineSuspension) EntryPipelineID() ID {
	return suspension.PinnedRevisions[EntryPipelineKey]
}
