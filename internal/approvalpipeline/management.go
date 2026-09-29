package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

// ManagementService owns the administrative control plane for request
// approval pipelines. It compiles documents before every write that could
// make them durable or active.
type ManagementService struct {
	repository application.RequestApprovalPipelineRepository
	compiled   *CompiledPipelineState
	paths      application.EscalationPathResolver
	clock      application.Clock
}

// ManagementOption supplies an optional collaborator.
type ManagementOption func(*ManagementService)

// WithEscalationPathResolver checks that every escalation path a document
// names (block paths and the pipeline default) exists before the document
// becomes durable, and reports each path's state in References. Without
// it path keys are accepted unchecked at creation; the repository still
// refuses activating a revision whose path has no active revision.
func WithEscalationPathResolver(resolver application.EscalationPathResolver) ManagementOption {
	return func(service *ManagementService) { service.paths = resolver }
}

func NewManagementService(repository application.RequestApprovalPipelineRepository, compiled *CompiledPipelineState, clock application.Clock, options ...ManagementOption) *ManagementService {
	service := &ManagementService{repository: repository, compiled: compiled, clock: clock}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

type PipelineRevision struct {
	Revision domain.RequestApprovalPipelineRevision
	Blocks   []PipelineBlock
}

// PipelineBlock is one stored block. A branch's definition carries its
// blocks as definitions, and Blocks carries them with their own snapshot
// metadata.
type PipelineBlock struct {
	SnapshotID    domain.ID
	ConfigVersion int
	ContentHash   string
	Definition    approvalblock.Definition
	Blocks        []PipelineBlock
}

type PreviewSample struct {
	Request BlockRequest
	Agent   BlockAgent
	// PreviousMetadata seeds what earlier blocks returned, keyed
	// "<scope>/<block_key>", so a stitched effective-pipeline test can
	// carry the organization preview's metadata into the agent-type one.
	PreviousMetadata map[string]json.RawMessage
}

type PreviewResult struct {
	Outcome          BlockOutcome
	ReasonCode       string
	TerminalBlockKey string
	Blocks           []PreviewBlockResult
	// EscalationPath is the path a human outcome would be handed to within
	// this scope alone: the terminal block's, else the document's default.
	// EscalationPathReason names which; empty with no_escalation means the
	// other scope's default or the queue decides.
	EscalationPath       string
	EscalationPathReason string
	// AskAgentOwner reports that the route asks the agent's owner, with
	// EscalationPath as the fallback. A preview has no instance, so it
	// names the route, never a person.
	AskAgentOwner bool
}

// Route is where a human outcome goes: an escalation path, or the agent's
// owner with that path as the fallback for a request without one. The
// zero value names no path and inherits the pipeline default.
type Route struct {
	EscalationPath string
	AskAgentOwner  bool
}

// PreviewBlockResult is one step of a preview, in execution order. A branch
// appears when its condition is evaluated (BlockEnter or BlockNext) and, if
// nothing inside it decided, once more at its end with EndOfBranch set.
type PreviewBlockResult struct {
	BlockKey    string
	Position    int
	Outcome     BlockOutcome
	Reason      string
	Metadata    map[string]json.RawMessage
	Evidence    BlockEvidence
	ErrorCode   BlockErrorCode
	EndOfBranch bool
}

// List is the pipelines index, narrowed to the agent types the caller
// holds pipeline.read at; the organization pipeline is listed only for an
// organization-wide grant. Status leaves the scopes archived with the
// last agent of their type out unless it says otherwise.
func (service *ManagementService) List(ctx context.Context, principal domain.Principal, status application.PipelineStatus, limit int) ([]domain.RequestApprovalPipelineSummary, error) {
	if err := requirePipelineMember(principal); err != nil {
		return nil, err
	}
	scope := authorization.Scope(principal, domain.PermissionPipelineRead)
	if scope.Empty() {
		return nil, authorization.Denied{Permission: domain.PermissionPipelineRead, Resource: domain.OrganizationResource()}
	}
	return service.repository.ListRequestApprovalPipelineSummaries(ctx, application.ListRequestApprovalPipelineSummariesInput{
		OrganizationID: principal.OrganizationID,
		Scope:          scope,
		Status:         status,
		Limit:          limit,
	})
}

func (service *ManagementService) GetActive(ctx context.Context, principal domain.Principal, agentSlug domain.AgentSlug) (PipelineRevision, error) {
	if err := authorizePipelineManagement(principal, domain.PermissionPipelineRead, domain.AgentResource(agentSlug)); err != nil {
		return PipelineRevision{}, err
	}
	if err := validateScope(agentSlug); err != nil {
		return PipelineRevision{}, err
	}
	revision, err := service.repository.GetActiveRequestApprovalPipelineRevision(ctx, application.GetActiveRequestApprovalPipelineRevisionInput{
		OrganizationID: principal.OrganizationID,
		AgentSlug:      agentSlug,
	})
	if err != nil {
		return PipelineRevision{}, err
	}
	return service.loadRevisionBlocks(ctx, revision)
}

// ListRevisions returns one page of the scope's history, newest first;
// cursor is the previous page's NextCursor, empty for the first page.
func (service *ManagementService) ListRevisions(ctx context.Context, principal domain.Principal, agentSlug domain.AgentSlug, limit int, cursor string) (application.RevisionPage[domain.RequestApprovalPipelineRevision], error) {
	var page application.RevisionPage[domain.RequestApprovalPipelineRevision]
	if err := authorizePipelineManagement(principal, domain.PermissionPipelineRead, domain.AgentResource(agentSlug)); err != nil {
		return page, err
	}
	if err := validateScope(agentSlug); err != nil {
		return page, err
	}
	return service.repository.ListRequestApprovalPipelineRevisions(ctx, application.ListRequestApprovalPipelineRevisionsInput{
		OrganizationID: principal.OrganizationID,
		AgentSlug:      agentSlug,
		Limit:          limit,
		Cursor:         cursor,
	})
}

func (service *ManagementService) GetRevision(ctx context.Context, principal domain.Principal, agentSlug domain.AgentSlug, revision int64) (PipelineRevision, error) {
	if err := authorizePipelineManagement(principal, domain.PermissionPipelineRead, domain.AgentResource(agentSlug)); err != nil {
		return PipelineRevision{}, err
	}
	if err := validateScope(agentSlug); err != nil {
		return PipelineRevision{}, err
	}
	stored, err := service.repository.GetRequestApprovalPipelineRevision(ctx, application.GetRequestApprovalPipelineRevisionInput{
		OrganizationID: principal.OrganizationID,
		AgentSlug:      agentSlug,
		Revision:       revision,
	})
	if err != nil {
		return PipelineRevision{}, err
	}
	return service.loadRevisionBlocks(ctx, stored)
}

func (service *ManagementService) CreateRevision(ctx context.Context, principal domain.Principal, agentSlug domain.AgentSlug, definitions []approvalblock.Definition, defaultRoute Route) (PipelineRevision, error) {
	if err := authorizePipelineManagement(principal, domain.PermissionPipelineWrite, domain.AgentResource(agentSlug)); err != nil {
		return PipelineRevision{}, err
	}
	if err := validateScope(agentSlug); err != nil {
		return PipelineRevision{}, err
	}
	if _, err := service.compiled.registry.compileDefinitions(definitions); err != nil {
		return PipelineRevision{}, err
	}
	all := flattenDefinitions(definitions)
	if err := service.requireBlockDependencies(ctx, principal.OrganizationID, all); err != nil {
		return PipelineRevision{}, err
	}
	if err := approvalblock.ValidateRoute(defaultRoute.EscalationPath, defaultRoute.AskAgentOwner); err != nil {
		return PipelineRevision{}, invalidPipelineDocument("default route is invalid", err)
	}
	if err := service.requireEscalationPaths(ctx, principal.OrganizationID, all, defaultRoute.EscalationPath); err != nil {
		return PipelineRevision{}, err
	}
	blocks, err := service.compiled.registry.snapshotInputs(definitions)
	if err != nil {
		return PipelineRevision{}, err
	}
	revision, err := service.repository.CreateRequestApprovalPipelineRevision(ctx, application.CreateRequestApprovalPipelineRevisionInput{
		ID:                    domain.ID(uuid.NewString()),
		OrganizationID:        principal.OrganizationID,
		AgentSlug:             agentSlug,
		Blocks:                blocks,
		DefaultEscalationPath: defaultRoute.EscalationPath,
		DefaultAskAgentOwner:  defaultRoute.AskAgentOwner,
		CreatedByActorID:      principal.ManagementActorID(),
		Now:                   service.clock.Now(),
	})
	if err != nil {
		return PipelineRevision{}, err
	}
	return service.loadRevisionBlocks(ctx, revision)
}

func (service *ManagementService) Activate(ctx context.Context, principal domain.Principal, agentSlug domain.AgentSlug, revision int64, expectedActiveRevision int64) (PipelineRevision, error) {
	if err := authorizePipelineManagement(principal, domain.PermissionPipelineActivate, domain.AgentResource(agentSlug)); err != nil {
		return PipelineRevision{}, err
	}
	target, err := service.GetRevision(ctx, principal, agentSlug, revision)
	if err != nil {
		return PipelineRevision{}, err
	}
	if err := service.compiled.Refresh(ctx, target.Revision); err != nil {
		return PipelineRevision{}, err
	}
	if err := service.requireBlockDependencies(ctx, principal.OrganizationID, flattenDefinitions(pipelineDefinitions(target.Blocks))); err != nil {
		return PipelineRevision{}, err
	}
	// Escalation paths and webhook endpoints are checked by the repository
	// inside the activating transaction, under the same locks an archive or
	// delete takes, so the answer cannot go stale before the switch.
	activated, err := service.repository.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
		OrganizationID:         principal.OrganizationID,
		AgentSlug:              agentSlug,
		Revision:               revision,
		ExpectedActiveRevision: expectedActiveRevision,
		ActorID:                principal.ManagementActorID(),
		Now:                    service.clock.Now(),
	})
	if err != nil {
		return PipelineRevision{}, err
	}
	target.Revision = activated
	return target, nil
}

func (service *ManagementService) Preview(ctx context.Context, principal domain.Principal, agentSlug domain.AgentSlug, definitions []approvalblock.Definition, defaultRoute Route, sample PreviewSample) (PreviewResult, error) {
	if err := authorizePipelineManagement(principal, domain.PermissionPipelineWrite, domain.AgentResource(agentSlug)); err != nil {
		return PreviewResult{}, err
	}
	if err := validateScope(agentSlug); err != nil {
		return PreviewResult{}, err
	}
	if err := validatePreviewSample(agentSlug, sample); err != nil {
		return PreviewResult{}, err
	}
	compiled, err := service.compiled.registry.compileDefinitions(definitions)
	if err != nil {
		return PreviewResult{}, err
	}

	result := PreviewResult{Outcome: BlockHuman, ReasonCode: "end_of_pipeline", Blocks: make([]PreviewBlockResult, 0, len(compiled))}
	if err := service.requireBlockDependencies(ctx, principal.OrganizationID, flattenDefinitions(definitions)); err != nil {
		return PreviewResult{}, err
	}
	if err := approvalblock.ValidateRoute(defaultRoute.EscalationPath, defaultRoute.AskAgentOwner); err != nil {
		return PreviewResult{}, invalidPipelineDocument("default route is invalid", err)
	}
	// The route a human outcome resolves to within this scope; the
	// terminal block overrides the default below.
	result.EscalationPathReason = domain.RoutingReasonNoEscalation
	if defaultRoute.EscalationPath != "" {
		result.EscalationPath, result.EscalationPathReason = defaultRoute.EscalationPath, domain.RoutingReasonPipelineDefault
		result.AskAgentOwner = defaultRoute.AskAgentOwner
	}
	previous := make(map[string]json.RawMessage, len(sample.PreviousMetadata))
	for key, value := range sample.PreviousMetadata {
		previous[key] = value
	}
	// Preview runs one scope in isolation, so metadata is namespaced by
	// that scope exactly as the executor namespaces it.
	scope := domain.PipelineScopeName(agentSlug)
	input := RequestApprovalBlockInput{OrganizationID: principal.OrganizationID, Preview: true, Request: sample.Request, Agent: sample.Agent, PreviousMetadata: previous, Budget: &BlockExecutionBudget{}}
	cursor := newBranchCursor(compiled, 0)
	for {
		step, index := cursor.next()
		if step == stepDone {
			return result, nil
		}
		block := compiled[index]
		if step == stepBranchEnd {
			// Nothing inside the entered branch decided: it asks a human
			// with its own reason and path, and nothing after it runs.
			result.Blocks = append(result.Blocks, PreviewBlockResult{
				BlockKey: block.Key, Position: index, Outcome: BlockHuman, Reason: block.Reason, EndOfBranch: true,
			})
			result.Outcome, result.ReasonCode, result.TerminalBlockKey = BlockHuman, "end_of_branch", block.Key
			if block.EscalationPath != "" {
				result.EscalationPath, result.EscalationPathReason = block.EscalationPath, domain.RoutingReasonBlockEscalation
				result.AskAgentOwner = block.AskAgentOwner
			}
			return result, nil
		}
		var evaluated RequestApprovalBlockResult
		if block.Branch {
			evaluated = executeBranchCondition(ctx, block.Executor, input)
		} else {
			evaluated = ExecuteRequestApprovalBlock(ctx, block.Executor, input)
		}
		result.Blocks = append(result.Blocks, PreviewBlockResult{
			BlockKey: block.Key, Position: index, Outcome: evaluated.Outcome,
			Reason: evaluated.Reason, Metadata: evaluated.Metadata, Evidence: evaluated.Evidence,
			ErrorCode: evaluated.ErrorCode,
		})
		switch {
		case block.Branch && evaluated.Outcome == BlockEnter:
			cursor.enter()
			continue
		case block.Branch && evaluated.Outcome == BlockNext:
			cursor.skip()
			continue
		case evaluated.Outcome == BlockNext:
			if len(evaluated.Metadata) > 0 {
				encoded, marshalErr := json.Marshal(evaluated.Metadata)
				if marshalErr != nil {
					return PreviewResult{}, marshalErr
				}
				input.PreviousMetadata[scope+"/"+block.Key] = encoded
			}
			cursor.advance()
			continue
		}
		result.Outcome = evaluated.Outcome
		result.ReasonCode = "block_outcome"
		if evaluated.ErrorCode != "" {
			result.ReasonCode = "block_error"
		}
		result.TerminalBlockKey = block.Key
		switch {
		case evaluated.Outcome != BlockHuman:
			result.EscalationPath, result.EscalationPathReason, result.AskAgentOwner = "", "", false
		case block.EscalationPath != "" && !block.Branch:
			// A branch whose condition failed keeps the default: its
			// own route is for its end only.
			result.EscalationPath, result.EscalationPathReason = block.EscalationPath, domain.RoutingReasonBlockEscalation
			result.AskAgentOwner = block.AskAgentOwner
		}
		return result, nil
	}
}

func validOptionalEscalationPath(key string) error {
	if key != "" && !approvalblock.ValidEscalationPathKey(key) {
		return invalidPipelineDocument("default_escalation_path must match ^[a-z0-9][a-z0-9._-]{0,62}$", nil)
	}
	return nil
}

// requireEscalationPaths checks that every escalation path the document
// names exists. A pipeline revision may be created against a path that is
// not active yet; the repository refuses activating it until the path is.
func (service *ManagementService) requireEscalationPaths(ctx context.Context, organizationID domain.ID, definitions []approvalblock.Definition, defaultEscalationPath string) error {
	if err := validOptionalEscalationPath(defaultEscalationPath); err != nil {
		return err
	}
	if service.paths == nil {
		return nil
	}
	keys := escalationPathKeys(definitions, defaultEscalationPath)
	if len(keys) == 0 {
		return nil
	}
	resolved, err := service.paths.ResolveEscalationPaths(ctx, application.ResolveEscalationPathsInput{
		OrganizationID: organizationID, PathKeys: keys,
	})
	if err != nil {
		return err
	}
	if len(resolved.Missing) > 0 {
		return invalidPipelineDocument(fmt.Sprintf("escalation paths do not exist: %v", resolved.Missing), nil)
	}
	return nil
}

// escalationPathKeys lists the distinct path keys a document names, block
// paths first in block order, then the default.
func escalationPathKeys(definitions []approvalblock.Definition, defaultEscalationPath string) []string {
	seen := make(map[string]struct{})
	keys := make([]string, 0)
	add := func(key string) {
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for _, definition := range definitions {
		add(definition.EscalationPath())
	}
	add(defaultEscalationPath)
	return keys
}

// RevisionReferences is the live state of everything one immutable
// revision points at outside itself, so an administrator sees what a
// rollback needs before attempting it. Activatable is false while any
// entry would refuse activation.
type RevisionReferences struct {
	EscalationPaths  []EscalationPathReference
	WebhookEndpoints []WebhookEndpointReference
	Blocks           []BlockReference
	Activatable      bool
}

// EscalationPathReference is one path the revision names. BlockKeys are
// the blocks naming it; Default reports the revision names it as its
// default. Exists is false when no revision of the path exists; Active
// when a revision is active.
type EscalationPathReference struct {
	PathKey   string
	BlockKeys []string
	Default   bool
	Exists    bool
	Active    bool
}

// WebhookEndpointReference is one endpoint key the revision's webhook
// blocks post to. Exists is false when no revision of the key exists;
// Active when a revision is active, in which case Name is that
// revision's name.
type WebhookEndpointReference struct {
	EndpointKey string
	Name        string
	BlockKeys   []string
	Exists      bool
	Active      bool
}

// BlockReference is a block the deployment or plan cannot run: Problem is
// the error code activation would answer with.
type BlockReference struct {
	BlockKey string
	Type     approvalblock.BlockType
	Problem  string
}

const (
	blockProblemPlan                = "plan"
	blockProblemWebhookUnconfigured = "webhook_unconfigured"
)

// References reports what one revision depends on and whether each
// dependency is in place right now.
func (service *ManagementService) References(ctx context.Context, principal domain.Principal, agentSlug domain.AgentSlug, revision int64) (RevisionReferences, error) {
	target, err := service.GetRevision(ctx, principal, agentSlug, revision)
	if err != nil {
		return RevisionReferences{}, err
	}
	definitions := flattenDefinitions(pipelineDefinitions(target.Blocks))
	references := RevisionReferences{
		EscalationPaths:  make([]EscalationPathReference, 0),
		WebhookEndpoints: make([]WebhookEndpointReference, 0),
		Blocks:           make([]BlockReference, 0),
		Activatable:      true,
	}
	if err := service.referencedEscalationPaths(ctx, principal.OrganizationID, definitions, target.Revision.DefaultEscalationPath, &references); err != nil {
		return RevisionReferences{}, err
	}
	if err := service.referencedWebhookEndpoints(ctx, principal.OrganizationID, definitions, &references); err != nil {
		return RevisionReferences{}, err
	}
	if err := service.referencedBlockProblems(ctx, principal.OrganizationID, definitions, &references); err != nil {
		return RevisionReferences{}, err
	}
	return references, nil
}

func (service *ManagementService) referencedEscalationPaths(ctx context.Context, organizationID domain.ID, definitions []approvalblock.Definition, defaultEscalationPath string, references *RevisionReferences) error {
	keys := escalationPathKeys(definitions, defaultEscalationPath)
	if len(keys) == 0 {
		return nil
	}
	missing, inactive := map[string]struct{}{}, map[string]struct{}{}
	if service.paths != nil {
		resolved, err := service.paths.ResolveEscalationPaths(ctx, application.ResolveEscalationPathsInput{
			OrganizationID: organizationID, PathKeys: keys,
		})
		if err != nil {
			return err
		}
		for _, key := range resolved.Missing {
			missing[key] = struct{}{}
		}
		for _, key := range resolved.Inactive {
			inactive[key] = struct{}{}
		}
	}
	for _, key := range keys {
		reference := EscalationPathReference{PathKey: key, BlockKeys: make([]string, 0), Default: key == defaultEscalationPath}
		for _, definition := range definitions {
			if definition.EscalationPath() == key {
				reference.BlockKeys = append(reference.BlockKeys, definition.ID)
			}
		}
		_, isMissing := missing[key]
		_, isInactive := inactive[key]
		reference.Exists = !isMissing
		reference.Active = !isMissing && !isInactive
		if !reference.Active {
			references.Activatable = false
		}
		references.EscalationPaths = append(references.EscalationPaths, reference)
	}
	return nil
}

func (service *ManagementService) referencedWebhookEndpoints(ctx context.Context, organizationID domain.ID, definitions []approvalblock.Definition, references *RevisionReferences) error {
	runtime := service.compiled.registry.webhooks
	if runtime == nil || runtime.Endpoints == nil {
		// The block problem below already says the runtime is absent.
		return nil
	}
	keys := webhookEndpointKeys(definitions)
	if len(keys) == 0 {
		return nil
	}
	resolved, err := runtime.Endpoints.ResolveWebhookEndpoints(ctx, application.ResolveWebhookEndpointsInput{
		OrganizationID: organizationID, EndpointKeys: keys,
	})
	if err != nil {
		return err
	}
	missing, inactive := map[string]struct{}{}, map[string]struct{}{}
	for _, key := range resolved.Missing {
		missing[key] = struct{}{}
	}
	for _, key := range resolved.Inactive {
		inactive[key] = struct{}{}
	}
	for _, key := range keys {
		reference := WebhookEndpointReference{EndpointKey: key, BlockKeys: make([]string, 0)}
		for _, definition := range definitions {
			if definition.Type == approvalblock.TypeWebhook && definition.Webhook != nil && definition.Webhook.EndpointKey == key {
				reference.BlockKeys = append(reference.BlockKeys, definition.ID)
			}
		}
		_, isMissing := missing[key]
		_, isInactive := inactive[key]
		reference.Exists = !isMissing
		reference.Active = !isMissing && !isInactive
		if reference.Active {
			active, err := runtime.Endpoints.GetActiveWebhookEndpointRevision(ctx, application.GetActiveWebhookEndpointRevisionInput{
				OrganizationID: organizationID, EndpointKey: key,
			})
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			reference.Name = active.Name
		} else {
			references.Activatable = false
		}
		references.WebhookEndpoints = append(references.WebhookEndpoints, reference)
	}
	return nil
}

// webhookEndpointKeys lists the endpoint keys the webhook blocks post to,
// in block order, deduplicated.
func webhookEndpointKeys(definitions []approvalblock.Definition) []string {
	keys := make([]string, 0)
	seen := make(map[string]struct{})
	for _, definition := range definitions {
		if definition.Type != approvalblock.TypeWebhook || definition.Webhook == nil {
			continue
		}
		key := definition.Webhook.EndpointKey
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func (service *ManagementService) referencedBlockProblems(ctx context.Context, organizationID domain.ID, definitions []approvalblock.Definition, references *RevisionReferences) error {
	registry := service.compiled.registry
	denied := make(map[application.Capability]bool)
	for _, definition := range definitions {
		problem, _ := registry.problem(definition)
		if capability, gated := registry.capability(definition.Type); problem == "" && gated && registry.entitlements != nil {
			refused, checked := denied[capability]
			if !checked {
				decision, err := registry.entitlements.CheckCapability(ctx, application.CheckCapabilityInput{OrganizationID: organizationID, Capability: capability})
				if err != nil {
					return err
				}
				refused = !decision.Allowed
				denied[capability] = refused
			}
			if refused {
				problem = blockProblemPlan
			}
		}
		if problem == "" {
			continue
		}
		references.Activatable = false
		references.Blocks = append(references.Blocks, BlockReference{BlockKey: definition.ID, Type: definition.Type, Problem: problem})
	}
	return nil
}
func (service *ManagementService) loadRevisionBlocks(ctx context.Context, revision domain.RequestApprovalPipelineRevision) (PipelineRevision, error) {
	stored, err := service.repository.GetRequestApprovalBlocks(ctx, application.GetRequestApprovalBlocksInput{
		OrganizationID: revision.OrganizationID,
		BlockIDs:       revision.BlockIDs,
	})
	if err != nil {
		return PipelineRevision{}, err
	}
	blocks, err := service.compiled.registry.nestSnapshots(stored)
	if err != nil {
		return PipelineRevision{}, err
	}
	return PipelineRevision{Revision: revision, Blocks: blocks}, nil
}

// requireBlockDependencies gates plan-limited block types on the
// organization's plan and checks that every judge model and webhook
// endpoint a block references is available. It runs after compilation, so
// an unconfigured runtime is reported first.
func (service *ManagementService) requireBlockDependencies(ctx context.Context, organizationID domain.ID, definitions []approvalblock.Definition) error {
	if err := service.requireCapabilities(ctx, organizationID, definitions); err != nil {
		return err
	}
	if err := service.requireRuntimeDependencies(definitions); err != nil {
		return err
	}
	return service.requireWebhookEndpoints(ctx, organizationID, definitions)
}

// requireRuntimeDependencies asks each block's runtime whether the
// deployment can run it right now: a kind naming a dependency the
// deployment does not offer (a judge model the catalog dropped) refuses
// the write, while a stored revision naming one still compiles and hands
// its requests to a person.
func (service *ManagementService) requireRuntimeDependencies(definitions []approvalblock.Definition) error {
	registry := service.compiled.registry
	for _, definition := range definitions {
		if _, refusal := registry.problem(definition); refusal != nil {
			return fmt.Errorf("block %q: %w", definition.ID, refusal)
		}
	}
	return nil
}

func (service *ManagementService) requireCapabilities(ctx context.Context, organizationID domain.ID, definitions []approvalblock.Definition) error {
	entitlements := service.compiled.registry.entitlements
	if entitlements == nil {
		return nil
	}
	checked := make(map[application.Capability]struct{})
	for _, definition := range definitions {
		capability, gated := service.compiled.registry.capability(definition.Type)
		if !gated {
			continue
		}
		if _, done := checked[capability]; done {
			continue
		}
		checked[capability] = struct{}{}
		decision, err := entitlements.CheckCapability(ctx, application.CheckCapabilityInput{
			OrganizationID: organizationID, Capability: capability,
		})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return fmt.Errorf("%s blocks are not available on this plan: %s: %w", definition.Type, decision.Reason, domain.ErrForbidden)
		}
	}
	return nil
}

// requireWebhookEndpoints rejects definitions that post to an endpoint
// key with no revision in the organization. Creation and preview only
// need the key to exist; activation requires it active, checked by the
// store inside the activating transaction.
func (service *ManagementService) requireWebhookEndpoints(ctx context.Context, organizationID domain.ID, definitions []approvalblock.Definition) error {
	runtime := service.compiled.registry.webhooks
	keys := webhookEndpointKeys(definitions)
	if len(keys) == 0 {
		return nil
	}
	if runtime == nil || runtime.Endpoints == nil {
		return fmt.Errorf("webhook blocks: %w", domain.ErrWebhookUnconfigured)
	}
	resolved, err := runtime.Endpoints.ResolveWebhookEndpoints(ctx, application.ResolveWebhookEndpointsInput{
		OrganizationID: organizationID, EndpointKeys: keys,
	})
	if err != nil {
		return err
	}
	if len(resolved.Missing) > 0 {
		return fmt.Errorf("webhook blocks reference endpoints %v: %w", resolved.Missing, domain.ErrWebhookEndpointNotFound)
	}
	return nil
}

func firstDuplicateConditionID(condition approvalblock.Condition, seen map[string]struct{}) string {
	if _, exists := seen[condition.ID]; exists {
		return condition.ID
	}
	seen[condition.ID] = struct{}{}
	for _, child := range condition.All {
		if duplicate := firstDuplicateConditionID(child, seen); duplicate != "" {
			return duplicate
		}
	}
	for _, child := range condition.Any {
		if duplicate := firstDuplicateConditionID(child, seen); duplicate != "" {
			return duplicate
		}
	}
	if condition.Some != nil {
		return firstDuplicateConditionID(condition.Some.Where, seen)
	}
	if condition.Every != nil {
		return firstDuplicateConditionID(condition.Every.Where, seen)
	}
	return ""
}

func validatePreviewSample(agentSlug domain.AgentSlug, sample PreviewSample) error {
	if strings.TrimSpace(sample.Request.Tool) == "" {
		return invalidPipelineDocument("sample request tool is required", nil)
	}
	if strings.TrimSpace(sample.Agent.Name) == "" {
		return invalidPipelineDocument("sample agent name is required", nil)
	}
	// The organization scope applies to every agent, so its preview
	// accepts any valid sample agent slug; an agent scope must match.
	if agentSlug == "" {
		if !domain.AgentSlug(sample.Agent.Slug).Valid() {
			return invalidPipelineDocument("sample agent slug must match ^[a-z0-9][a-z0-9._-]{0,62}$", nil)
		}
	} else if domain.AgentSlug(sample.Agent.Slug) != agentSlug {
		return invalidPipelineDocument("sample agent slug must match the path agent slug", nil)
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(sample.Request.Arguments, &arguments); err != nil || arguments == nil {
		return invalidPipelineDocument("sample request arguments must be a JSON object", err)
	}
	for key, value := range sample.PreviousMetadata {
		scope, blockKey, found := strings.Cut(key, "/")
		if !found || (scope != domain.PipelineScopeOrganization && scope != domain.PipelineScopeAgent) || blockKey == "" {
			return invalidPipelineDocument("sample previous metadata keys must be <scope>/<block_key>", nil)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value, &object); err != nil || object == nil {
			return invalidPipelineDocument("sample previous metadata values must be JSON objects", err)
		}
	}
	return nil
}

// requirePipelineMember is the shared precondition: a person with a
// membership in the organization.
func requirePipelineMember(principal domain.Principal) error {
	if !domain.ManagementPrincipal(principal) {
		return domain.ErrForbidden
	}
	return nil
}

// authorizePipelineManagement admits a member holding the permission on
// the pipeline's scope: the agent type, or the organization for the
// organization pipeline.
func authorizePipelineManagement(principal domain.Principal, permission domain.Permission, resource domain.Resource) error {
	if err := requirePipelineMember(principal); err != nil {
		return err
	}
	return authorization.Check(principal, permission, resource)
}

// validateScope accepts the pipeline scope address: an empty agent type is
// the organization scope, anything else must be a valid agent type.
func validateScope(agentSlug domain.AgentSlug) error {
	if !domain.ValidPipelineScope(agentSlug) {
		return invalidPipelineDocument("agent slug must match ^[a-z0-9][a-z0-9._-]{0,62}$", nil)
	}
	return nil
}

func invalidPipelineDocument(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%s: %w", message, domain.ErrInvalid)
	}
	return fmt.Errorf("%s: %v: %w", message, cause, domain.ErrInvalid)
}
