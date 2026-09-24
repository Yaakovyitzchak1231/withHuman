package httpapi

import (
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
)

// approvalResponse is the product representation. It must not be used by AAP handlers.
type approvalResponse struct {
	ID                domain.ID                 `json:"id"`
	OrganizationID    domain.ID                 `json:"organization_id"`
	AgentSlug         domain.AgentSlug          `json:"agent_slug"`
	AgentInstanceID   domain.ID                 `json:"agent_instance_id"`
	AgentName         string                    `json:"agent_name"`
	AgentInstanceName string                    `json:"agent_instance_name"`
	Tool              string                    `json:"tool"`
	Server            string                    `json:"server,omitempty"`
	Arguments         json.RawMessage           `json:"arguments"`
	AgentReasoning    string                    `json:"agent_reasoning,omitempty"`
	Context           json.RawMessage           `json:"context,omitempty"`
	Status            domain.ApprovalStatus     `json:"status"`
	DeadlineAt        time.Time                 `json:"deadline_at"`
	CreatedAt         time.Time                 `json:"created_at"`
	Decision          *approvalDecisionResponse `json:"decision,omitempty"`
}

// requestApprovalPipelineSummaryResponse is one scope. active_revision
// is absent and archived_at present for a scope archived with its agent.
type requestApprovalPipelineSummaryResponse struct {
	Scope           string           `json:"scope"`
	AgentSlug       domain.AgentSlug `json:"agent_slug,omitempty"`
	ActiveRevision  *int64           `json:"active_revision,omitempty"`
	LatestRevision  int64            `json:"latest_revision"`
	RevisionCount   int64            `json:"revision_count"`
	LatestCreatedAt time.Time        `json:"latest_created_at"`
	ArchivedAt      *time.Time       `json:"archived_at,omitempty"`
}

func newRequestApprovalPipelineSummaryResponses(summaries []domain.RequestApprovalPipelineSummary) []requestApprovalPipelineSummaryResponse {
	responses := make([]requestApprovalPipelineSummaryResponse, len(summaries))
	for index, summary := range summaries {
		responses[index] = requestApprovalPipelineSummaryResponse{
			Scope: domain.PipelineScopeName(summary.AgentSlug), AgentSlug: summary.AgentSlug,
			ActiveRevision: summary.ActiveRevision, LatestRevision: summary.LatestRevision,
			RevisionCount: summary.RevisionCount, LatestCreatedAt: summary.LatestCreatedAt,
			ArchivedAt: summary.ArchivedAt,
		}
	}
	return responses
}

type requestApprovalPipelineRevisionSummaryResponse struct {
	ID                    domain.ID        `json:"id"`
	Scope                 string           `json:"scope"`
	AgentSlug             domain.AgentSlug `json:"agent_slug,omitempty"`
	Revision              int64            `json:"revision"`
	IsActive              bool             `json:"is_active"`
	BlockCount            int              `json:"block_count"`
	DefaultEscalationPath string           `json:"default_escalation_path,omitempty"`
	CreatedByActorID      domain.ID        `json:"created_by_actor_id"`
	CreatedAt             time.Time        `json:"created_at"`
	ArchivedAt            *time.Time       `json:"archived_at,omitempty"`
}

type requestApprovalPipelineRevisionResponse struct {
	requestApprovalPipelineRevisionSummaryResponse
	Blocks []requestApprovalPipelineBlockResponse `json:"blocks"`
}

type requestApprovalPipelineBlockResponse struct {
	ID          string                  `json:"id"`
	SnapshotID  domain.ID               `json:"snapshot_id"`
	Name        string                  `json:"name"`
	Type        approvalblock.BlockType `json:"type"`
	Enabled     bool                    `json:"enabled"`
	Timeout     string                  `json:"timeout"`
	MaxAttempts int                     `json:"max_attempts"`
	// Config is the block's configuration: encoded bytes for a leaf, and
	// for a branch a typed value holding its stored blocks, so a deep tree
	// encodes in one pass.
	Config        any    `json:"config"`
	ConfigVersion int    `json:"config_version"`
	ContentHash   string `json:"content_hash"`
}

// storedBranchConfigResponse is a stored branch's configuration: its blocks
// nested, each with its own snapshot metadata.
type storedBranchConfigResponse struct {
	When           approvalblock.Condition                `json:"when"`
	Blocks         []requestApprovalPipelineBlockResponse `json:"blocks"`
	Reason         string                                 `json:"reason"`
	EscalationPath string                                 `json:"escalation_path,omitempty"`
}

func newRequestApprovalPipelineRevisionSummaryResponse(revision domain.RequestApprovalPipelineRevision) requestApprovalPipelineRevisionSummaryResponse {
	return requestApprovalPipelineRevisionSummaryResponse{
		ID: revision.ID, Scope: domain.PipelineScopeName(revision.AgentSlug), AgentSlug: revision.AgentSlug,
		Revision: revision.Revision, IsActive: revision.IsActive, BlockCount: len(revision.BlockIDs),
		DefaultEscalationPath: revision.DefaultEscalationPath,
		CreatedByActorID:      revision.CreatedByActorID, CreatedAt: revision.CreatedAt,
		ArchivedAt: revision.ArchivedAt,
	}
}

func newRequestApprovalPipelineRevisionSummaryResponses(revisions []domain.RequestApprovalPipelineRevision) []requestApprovalPipelineRevisionSummaryResponse {
	responses := make([]requestApprovalPipelineRevisionSummaryResponse, len(revisions))
	for index, revision := range revisions {
		responses[index] = newRequestApprovalPipelineRevisionSummaryResponse(revision)
	}
	return responses
}

// pipelineUseResponse is one active pipeline revision's reference to a
// shared object. block_key is absent when the revision names an
// escalation path as its default.
type pipelineUseResponse struct {
	PipelineID domain.ID        `json:"pipeline_id"`
	Scope      string           `json:"scope"`
	AgentSlug  domain.AgentSlug `json:"agent_slug,omitempty"`
	Revision   int64            `json:"revision"`
	BlockKey   string           `json:"block_key,omitempty"`
}

func newPipelineUseResponses(uses []domain.PipelineUse) []pipelineUseResponse {
	responses := make([]pipelineUseResponse, 0, len(uses))
	for _, use := range uses {
		responses = append(responses, pipelineUseResponse{
			PipelineID: use.PipelineID, Scope: domain.PipelineScopeName(use.AgentSlug),
			AgentSlug: use.AgentSlug, Revision: use.Revision, BlockKey: use.BlockKey,
		})
	}
	return responses
}

// targetedEscalationPathResponse is one active escalation path revision
// that targets a member, as carried by a refused suspension or removal.
type targetedEscalationPathResponse struct {
	PathKey  string `json:"path_key"`
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}

func newEscalationPathReferenceResponses(paths []domain.EscalationPathReference) []targetedEscalationPathResponse {
	responses := make([]targetedEscalationPathResponse, 0, len(paths))
	for _, path := range paths {
		responses = append(responses, targetedEscalationPathResponse{
			PathKey: path.PathKey, Name: path.Name, Revision: path.Revision,
		})
	}
	return responses
}

type pipelineReferencesResponse struct {
	EscalationPaths  []escalationPathReferenceResponse  `json:"escalation_paths"`
	WebhookEndpoints []webhookEndpointReferenceResponse `json:"webhook_endpoints"`
	Blocks           []blockReferenceResponse           `json:"blocks"`
	Activatable      bool                               `json:"activatable"`
}

type escalationPathReferenceResponse struct {
	PathKey   string   `json:"path_key"`
	BlockKeys []string `json:"block_keys"`
	Default   bool     `json:"default"`
	Exists    bool     `json:"exists"`
	Active    bool     `json:"active"`
}

type webhookEndpointReferenceResponse struct {
	EndpointKey string   `json:"endpoint_key"`
	Name        string   `json:"name,omitempty"`
	BlockKeys   []string `json:"block_keys"`
	Exists      bool     `json:"exists"`
	Active      bool     `json:"active"`
}

type blockReferenceResponse struct {
	BlockKey string                  `json:"block_key"`
	Type     approvalblock.BlockType `json:"type"`
	Problem  string                  `json:"problem"`
}

func newPipelineReferencesResponse(references approvalpipeline.RevisionReferences) pipelineReferencesResponse {
	response := pipelineReferencesResponse{
		EscalationPaths:  make([]escalationPathReferenceResponse, 0, len(references.EscalationPaths)),
		WebhookEndpoints: make([]webhookEndpointReferenceResponse, 0, len(references.WebhookEndpoints)),
		Blocks:           make([]blockReferenceResponse, 0, len(references.Blocks)),
		Activatable:      references.Activatable,
	}
	for _, path := range references.EscalationPaths {
		response.EscalationPaths = append(response.EscalationPaths, escalationPathReferenceResponse{
			PathKey: path.PathKey, BlockKeys: path.BlockKeys, Default: path.Default, Exists: path.Exists, Active: path.Active,
		})
	}
	for _, endpoint := range references.WebhookEndpoints {
		response.WebhookEndpoints = append(response.WebhookEndpoints, webhookEndpointReferenceResponse{
			EndpointKey: endpoint.EndpointKey, Name: endpoint.Name, BlockKeys: endpoint.BlockKeys, Exists: endpoint.Exists, Active: endpoint.Active,
		})
	}
	for _, block := range references.Blocks {
		response.Blocks = append(response.Blocks, blockReferenceResponse{BlockKey: block.BlockKey, Type: block.Type, Problem: block.Problem})
	}
	return response
}

func newRequestApprovalPipelineRevisionResponse(revision approvalpipeline.PipelineRevision) requestApprovalPipelineRevisionResponse {
	return requestApprovalPipelineRevisionResponse{
		requestApprovalPipelineRevisionSummaryResponse: newRequestApprovalPipelineRevisionSummaryResponse(revision.Revision),
		Blocks: newRequestApprovalPipelineBlockResponses(revision.Blocks),
	}
}

func newRequestApprovalPipelineBlockResponses(blocks []approvalpipeline.PipelineBlock) []requestApprovalPipelineBlockResponse {
	responses := make([]requestApprovalPipelineBlockResponse, len(blocks))
	for index, block := range blocks {
		definition := block.Definition
		var config any
		if definition.Type == approvalblock.TypeBranch && definition.Branch != nil {
			config = storedBranchConfigResponse{
				When: definition.Branch.When, Blocks: newRequestApprovalPipelineBlockResponses(block.Blocks),
				Reason: definition.Branch.Reason, EscalationPath: definition.Branch.EscalationPath,
			}
		} else {
			// The definition was decoded from a validated snapshot, so its
			// configuration always encodes.
			encoded, _ := definition.ConfigJSON()
			config = encoded
		}
		responses[index] = requestApprovalPipelineBlockResponse{
			ID: definition.ID, SnapshotID: block.SnapshotID, Name: definition.Name, Type: definition.Type,
			Enabled: definition.Enabled, Timeout: definition.Timeout, MaxAttempts: definition.MaxAttempts,
			Config: config, ConfigVersion: block.ConfigVersion, ContentHash: block.ContentHash,
		}
	}
	return responses
}

type requestApprovalPipelinePreviewResponse struct {
	Outcome              approvalpipeline.BlockOutcome        `json:"outcome"`
	ReasonCode           string                               `json:"reason_code"`
	TerminalBlockKey     string                               `json:"terminal_block_id,omitempty"`
	Blocks               []requestApprovalPipelineBlockResult `json:"blocks"`
	EscalationPath       string                               `json:"escalation_path,omitempty"`
	EscalationPathReason string                               `json:"escalation_path_reason,omitempty"`
}

type requestApprovalPipelineBlockResult struct {
	BlockKey  string                             `json:"block_id"`
	Position  int                                `json:"position"`
	Outcome   approvalpipeline.BlockOutcome      `json:"outcome"`
	Reason    string                             `json:"reason,omitempty"`
	Metadata  map[string]json.RawMessage         `json:"metadata,omitempty"`
	Cost      uint64                             `json:"cost"`
	Trace     []approvalpipeline.NodeTrace       `json:"trace"`
	ErrorCode approvalpipeline.BlockErrorCode    `json:"error_code,omitempty"`
	Delivery  *approvalpipeline.DeliveryEvidence `json:"delivery,omitempty"`
	// EndOfBranch marks the closing step of a branch the request entered
	// and nothing inside decided.
	EndOfBranch bool `json:"end_of_branch,omitempty"`
}

func newRequestApprovalPipelinePreviewResponse(result approvalpipeline.PreviewResult) requestApprovalPipelinePreviewResponse {
	blocks := make([]requestApprovalPipelineBlockResult, len(result.Blocks))
	for index, block := range result.Blocks {
		blocks[index] = requestApprovalPipelineBlockResult{
			BlockKey: block.BlockKey, Position: block.Position, Outcome: block.Outcome,
			Reason: block.Reason, Metadata: block.Metadata, Cost: block.Evidence.Cost,
			Trace: block.Evidence.Trace, ErrorCode: block.ErrorCode, Delivery: block.Evidence.Delivery,
			EndOfBranch: block.EndOfBranch,
		}
	}
	return requestApprovalPipelinePreviewResponse{
		Outcome: result.Outcome, ReasonCode: result.ReasonCode,
		TerminalBlockKey: result.TerminalBlockKey, Blocks: blocks,
		EscalationPath: result.EscalationPath, EscalationPathReason: result.EscalationPathReason,
	}
}

type approvalDecisionResponse struct {
	ID                  domain.ID             `json:"id"`
	RequestID           domain.ID             `json:"request_id"`
	OrganizationID      domain.ID             `json:"organization_id"`
	MembershipID        domain.ID             `json:"membership_id,omitempty"`
	Status              domain.ApprovalStatus `json:"status"`
	Note                string                `json:"note,omitempty"`
	Channel             string                `json:"channel"`
	ReviewerDisplayName string                `json:"reviewer_display_name,omitempty"`
	ReviewerEmail       string                `json:"reviewer_email,omitempty"`
	Authentication      domain.AuthContext    `json:"authentication"`
	IdempotencyKey      string                `json:"idempotency_key"`
	DecidedAt           time.Time             `json:"decided_at"`
}

type decisionOperationResponse struct {
	ID             domain.ID                 `json:"id"`
	OrganizationID domain.ID                 `json:"organization_id"`
	RequestID      domain.ID                 `json:"request_id"`
	Status         domain.OperationStatus    `json:"status"`
	ErrorCode      string                    `json:"error_code,omitempty"`
	ErrorMessage   string                    `json:"error_message,omitempty"`
	Decision       *approvalDecisionResponse `json:"decision,omitempty"`
	CreatedAt      time.Time                 `json:"created_at"`
	CompletedAt    *time.Time                `json:"completed_at,omitempty"`
}

// revisionPageResponse is one page of a revisioned resource's history:
// the same envelope for pipelines, escalation paths and webhook endpoints.
type revisionPageResponse[Revision any] struct {
	Revisions  []Revision `json:"revisions"`
	NextCursor string     `json:"next_cursor,omitempty"`
	TotalCount int64      `json:"total_count"`
}

func newApprovalResponse(request domain.ApprovalRequest) approvalResponse {
	return approvalResponse{
		ID: request.ID, OrganizationID: request.OrganizationID, AgentSlug: request.AgentSlug,
		AgentInstanceID: request.AgentInstanceID, AgentName: request.AgentName,
		AgentInstanceName: request.AgentInstanceName, Tool: request.Tool, Server: request.Server, Arguments: request.Arguments,
		AgentReasoning: request.AgentReasoning, Context: request.Context,
		Status: request.Status, DeadlineAt: request.DeadlineAt, CreatedAt: request.CreatedAt,
		Decision: newApprovalDecisionResponse(request.Decision),
	}
}

func newApprovalResponses(requests []domain.ApprovalRequest) []approvalResponse {
	responses := make([]approvalResponse, len(requests))
	for index, request := range requests {
		responses[index] = newApprovalResponse(request)
	}
	return responses
}

type approvalClaimResponse struct {
	ID           domain.ID `json:"id"`
	MembershipID domain.ID `json:"membership_id"`
	DisplayName  string    `json:"display_name,omitempty"`
	ClaimedAt    time.Time `json:"claimed_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// queueApprovalResponse is the product-only request representation: the
// shared AAP body plus queue state. Claims are advisory reviewer
// coordination and must never cross the AAP boundary, so AAP handlers keep
// serializing approvalResponse and are structurally unable to carry a
// claim.
type queueApprovalResponse struct {
	approvalResponse
	// UpdatedAt moves on every change a reviewer should notice, escalation
	// progress included, so the page refetches the review when it changes
	// rather than polling. Product-only: agents have no use for it.
	UpdatedAt time.Time              `json:"updated_at"`
	Claim     *approvalClaimResponse `json:"claim,omitempty"`
	// Presentation is how the client renders the arguments, resolved
	// from the organization's entries and the shipped catalog. Absent
	// when neither names the tool, and the client renders by shape.
	// Product-only: it is a reading aid, never part of the action.
	Presentation *presentationResponse `json:"presentation,omitempty"`
}

// newQueueApprovalResponse renders an expired claim as absent. Read-time
// expiry keeps a lost lapse signal invisible to viewers, and it changes the
// serialized queue payload the moment a claim expires, so the SSE
// fingerprint diff pushes a frame within its next poll.
func newQueueApprovalResponse(request domain.ApprovalRequest, now time.Time) queueApprovalResponse {
	response := queueApprovalResponse{approvalResponse: newApprovalResponse(request), UpdatedAt: request.UpdatedAt}
	if claim := request.Claim; claim != nil && claim.Live(now) {
		response.Claim = &approvalClaimResponse{
			ID: claim.ID, MembershipID: claim.MembershipID, DisplayName: claim.ClaimantDisplayName,
			ClaimedAt: claim.ClaimedAt, ExpiresAt: claim.ExpiresAt,
		}
	}
	return response
}

func newQueueApprovalResponses(requests []domain.ApprovalRequest, now time.Time) []queueApprovalResponse {
	responses := make([]queueApprovalResponse, len(requests))
	for index, request := range requests {
		responses[index] = newQueueApprovalResponse(request, now)
	}
	return responses
}

func newClaimResponse(claim domain.Claim) approvalClaimResponse {
	return approvalClaimResponse{
		ID: claim.ID, MembershipID: claim.MembershipID, DisplayName: claim.ClaimantDisplayName,
		ClaimedAt: claim.ClaimedAt, ExpiresAt: claim.ExpiresAt,
	}
}

func newApprovalDecisionResponse(decision *domain.Decision) *approvalDecisionResponse {
	if decision == nil {
		return nil
	}
	return &approvalDecisionResponse{
		ID: decision.ID, RequestID: decision.RequestID, OrganizationID: decision.OrganizationID,
		MembershipID: decision.MembershipID, Status: decision.Status, Note: decision.Note,
		Channel: decision.Channel, ReviewerDisplayName: decision.ReviewerDisplayName,
		ReviewerEmail: decision.ReviewerEmail, Authentication: decision.Auth,
		IdempotencyKey: decision.IdempotencyKey, DecidedAt: decision.DecidedAt,
	}
}

func newDecisionOperationResponse(operation domain.DecisionOperation) decisionOperationResponse {
	return decisionOperationResponse{
		ID: operation.ID, OrganizationID: operation.OrganizationID, RequestID: operation.RequestID,
		Status: operation.Status, ErrorCode: operation.ErrorCode, ErrorMessage: operation.ErrorMessage,
		Decision: newApprovalDecisionResponse(operation.Decision), CreatedAt: operation.CreatedAt,
		CompletedAt: operation.CompletedAt,
	}
}
