package application

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

const (
	maximumRequestApprovalPipelineBlocks = 1_000
	maximumRequestApprovalBlockTimeoutMS = 60_000
	maximumRequestApprovalBlockAttempts  = 10
	maximumPipelineNameLength            = 120
)

var requestApprovalBlockRegistryKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// RequestApprovalBlockSnapshotInput is the complete immutable content for one
// block snapshot. The repository may reuse an existing snapshot with the same
// organization-scoped content hash instead of inserting ID.
type RequestApprovalBlockSnapshotInput struct {
	ID            domain.ID
	BlockKey      string
	Name          string
	BlockType     string
	ConfigVersion int
	Config        json.RawMessage
	TimeoutMS     int
	MaxAttempts   int
}

func (input RequestApprovalBlockSnapshotInput) Validate() error {
	if err := firstError([]error{
		requiredID("request approval block ID", input.ID),
		validRegistryKey("request approval block key", input.BlockKey),
		requiredString("request approval block name", input.Name),
		validRegistryKey("request approval block type", input.BlockType),
		validJSON("request approval block config", input.Config),
	}); err != nil {
		return err
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(input.Config, &config); err != nil || config == nil {
		return invalidField("request approval block config", "must be a JSON object")
	}
	if input.ConfigVersion <= 0 {
		return invalidField("request approval block config version", "must be positive")
	}
	if input.TimeoutMS <= 0 || input.TimeoutMS > maximumRequestApprovalBlockTimeoutMS {
		return invalidField("request approval block timeout", "must be between 1 and 60000 milliseconds")
	}
	if input.MaxAttempts <= 0 || input.MaxAttempts > maximumRequestApprovalBlockAttempts {
		return invalidField("request approval block max attempts", "must be between 1 and 10")
	}
	return nil
}

// CreateRequestApprovalPipelineInput creates a pipeline and persists its
// first revision, active at once, so the pipeline exists with an active
// revision from its first moment. A key that was ever used refuses with
// domain.ErrRequestApprovalPipelineExists; the entry key is refused with
// domain.ErrRequestApprovalPipelineEntry, since the organization's
// activation seeds it.
type CreateRequestApprovalPipelineInput CreateRequestApprovalPipelineRevisionInput

func (input CreateRequestApprovalPipelineInput) Validate() error {
	if input.PipelineKey == domain.EntryPipelineKey {
		return invalidField("pipeline key", "is reserved for the entry pipeline")
	}
	return CreateRequestApprovalPipelineRevisionInput(input).Validate()
}

// CreateRequestApprovalPipelineRevisionInput appends the next immutable
// revision of a live pipeline. Name is the pipeline's display name as of
// this revision.
type CreateRequestApprovalPipelineRevisionInput struct {
	ID                    domain.ID
	OrganizationID        domain.ID
	PipelineKey           string
	Name                  string
	Blocks                []RequestApprovalBlockSnapshotInput
	DefaultEscalationPath string
	CreatedByActorID      domain.ID
	Now                   time.Time
}

func (input CreateRequestApprovalPipelineRevisionInput) Validate() error {
	if err := firstError([]error{
		requiredID("request approval pipeline ID", input.ID),
		requiredID("organization ID", input.OrganizationID),
		validPipelineKey(input.PipelineKey),
		validPipelineName(input.Name),
		validOptionalEscalationPathKey(input.DefaultEscalationPath),
		requiredID("creating membership ID", input.CreatedByActorID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if len(input.Blocks) > maximumRequestApprovalPipelineBlocks {
		return invalidField("request approval pipeline blocks", "must contain at most 1000 blocks")
	}
	seenIDs := make(map[domain.ID]struct{}, len(input.Blocks))
	seenKeys := make(map[string]struct{}, len(input.Blocks))
	for _, block := range input.Blocks {
		if err := block.Validate(); err != nil {
			return err
		}
		if _, exists := seenIDs[block.ID]; exists {
			return invalidField("request approval pipeline blocks", "must not contain duplicate block IDs")
		}
		seenIDs[block.ID] = struct{}{}
		if _, exists := seenKeys[block.BlockKey]; exists {
			return invalidField("request approval pipeline blocks", "must not contain duplicate block keys")
		}
		seenKeys[block.BlockKey] = struct{}{}
	}
	return nil
}

type GetRequestApprovalPipelineRevisionInput struct {
	OrganizationID domain.ID
	PipelineKey    string
	Revision       int64
}

// PipelineStatus selects which pipelines a listing returns: the live ones
// (those with an active revision, the default), the archived ones, or
// both.
type PipelineStatus string

const (
	PipelineStatusLive     PipelineStatus = "live"
	PipelineStatusArchived PipelineStatus = "archived"
	PipelineStatusAll      PipelineStatus = "all"
)

func validPipelineStatus(status PipelineStatus) error {
	switch status {
	case "", PipelineStatusLive, PipelineStatusArchived, PipelineStatusAll:
		return nil
	}
	return invalidField("pipeline status", "must be live, archived, or all")
}

type ListRequestApprovalPipelineSummariesInput struct {
	OrganizationID domain.ID
	// Scope is where the caller holds pipeline.read: everything, or a set
	// of pipeline keys.
	Scope domain.ScopeSet
	// Status defaults to live.
	Status PipelineStatus
	Limit  int
}

func (input ListRequestApprovalPipelineSummariesInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validScopeSet("scope", input.Scope, domain.ScopePipeline),
		validPipelineStatus(input.Status),
		nonNegative("limit", int64(input.Limit)),
	})
}

func (input GetRequestApprovalPipelineRevisionInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validPipelineKey(input.PipelineKey),
	}); err != nil {
		return err
	}
	return positiveRevision(input.Revision)
}

type ListRequestApprovalPipelineRevisionsInput struct {
	OrganizationID domain.ID
	PipelineKey    string
	Limit          int
	// Cursor is the NextCursor of the previous page; empty for the first.
	Cursor string
}

// ListActiveRequestApprovalPipelineRevisionsInput pages across active
// revisions for process startup. It is an operational query rather than a
// tenant-facing product read; each returned revision still carries its owning
// organization, which scopes every subsequent block lookup.
type ListActiveRequestApprovalPipelineRevisionsInput struct {
	AfterID domain.ID
	Limit   int
}

func (input ListActiveRequestApprovalPipelineRevisionsInput) Validate() error {
	if input.Limit <= 0 || input.Limit > 200 {
		return invalidField("limit", "must be between 1 and 200")
	}
	return nil
}

func (input ListRequestApprovalPipelineRevisionsInput) Validate() error {
	_, cursorErr := DecodeRevisionCursor(input.Cursor)
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validPipelineKey(input.PipelineKey),
		nonNegative("limit", int64(input.Limit)),
		cursorErr,
	})
}

type GetActiveRequestApprovalPipelineRevisionInput struct {
	OrganizationID domain.ID
	PipelineKey    string
}

func (input GetActiveRequestApprovalPipelineRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validPipelineKey(input.PipelineKey),
	})
}

// ArchiveRequestApprovalPipelineInput archives a pipeline, revisions
// included. The entry pipeline refuses with
// domain.ErrRequestApprovalPipelineEntry, a pipeline an active revision
// delegates to with domain.RequestApprovalPipelineInUseError.
type ArchiveRequestApprovalPipelineInput struct {
	OrganizationID domain.ID
	PipelineKey    string
	ActorID        domain.ID
	Now            time.Time
}

func (input ArchiveRequestApprovalPipelineInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validPipelineKey(input.PipelineKey),
		requiredID("actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

// ListRequestApprovalPipelineUsesInput asks which active pipeline
// revisions delegate to the pipeline.
type ListRequestApprovalPipelineUsesInput struct {
	OrganizationID domain.ID
	PipelineKey    string
}

func (input ListRequestApprovalPipelineUsesInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validPipelineKey(input.PipelineKey),
	})
}

// ResolvePipelineKeysInput asks which of the named pipeline keys exist
// (live, not archived) and which have an active revision. Pipeline
// revisions consult it: creation needs every delegated key to exist,
// activation needs every delegated key active.
type ResolvePipelineKeysInput struct {
	OrganizationID domain.ID
	PipelineKeys   []string
}

func (input ResolvePipelineKeysInput) Validate() error {
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(input.PipelineKeys))
	for _, key := range input.PipelineKeys {
		if err := validPipelineKey(key); err != nil {
			return err
		}
		if _, duplicate := seen[key]; duplicate {
			return invalidField("pipeline keys", "must be unique")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ResolvedPipelineKeys answers ResolvePipelineKeysInput: Missing keys
// have no live revision (never created, or archived); Inactive keys are
// live without an active revision, which the store never allows but the
// port reports for completeness.
type ResolvedPipelineKeys struct {
	Missing  []string
	Inactive []string
}

type GetRequestApprovalBlocksInput struct {
	OrganizationID domain.ID
	BlockIDs       []domain.ID
}

func (input GetRequestApprovalBlocksInput) Validate() error {
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	seen := make(map[domain.ID]struct{}, len(input.BlockIDs))
	for _, blockID := range input.BlockIDs {
		if err := requiredID("request approval block ID", blockID); err != nil {
			return err
		}
		if _, exists := seen[blockID]; exists {
			return invalidField("request approval block IDs", "must not contain duplicates")
		}
		seen[blockID] = struct{}{}
	}
	return nil
}

// ActivateRequestApprovalPipelineRevisionInput switches a pipeline's
// active revision. Every live pipeline always has one, so the caller
// states which revision it believes is active and the switch is refused
// when that has changed.
type ActivateRequestApprovalPipelineRevisionInput struct {
	OrganizationID         domain.ID
	PipelineKey            string
	Revision               int64
	ExpectedActiveRevision int64
	ActorID                domain.ID
	Now                    time.Time
}

func (input ActivateRequestApprovalPipelineRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validPipelineKey(input.PipelineKey),
		positiveRevision(input.Revision),
		positiveRevision(input.ExpectedActiveRevision),
		requiredID("actor actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

func validPipelineName(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return invalidField("pipeline name", "is required")
	}
	if len(trimmed) > maximumPipelineNameLength {
		return invalidField("pipeline name", "must be at most 120 characters")
	}
	return nil
}

func validRegistryKey(field, value string) error {
	if !requestApprovalBlockRegistryKeyPattern.MatchString(value) {
		return invalidField(field, "must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	return nil
}

func positiveRevision(revision int64) error {
	if revision <= 0 {
		return invalidField("request approval pipeline revision", "must be positive")
	}
	return nil
}

type GetRequestApprovalPipelineRevisionByIDInput struct {
	OrganizationID domain.ID
	RevisionID     domain.ID
}

func (input GetRequestApprovalPipelineRevisionByIDInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("request approval pipeline revision ID", input.RevisionID),
	})
}
