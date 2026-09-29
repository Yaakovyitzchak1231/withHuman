package approvalpipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

const (
	maximumCompilationTime  = 5 * time.Second
	startupRevisionPageSize = 200
)

var errCompiledPipelineCacheUnavailable = errors.New("compiled request approval pipeline cache unavailable")

type compiledPipelineRepository interface {
	ListActiveRequestApprovalPipelineRevisions(context.Context, application.ListActiveRequestApprovalPipelineRevisionsInput) ([]domain.RequestApprovalPipelineRevision, error)
	GetRequestApprovalBlocks(context.Context, application.GetRequestApprovalBlocksInput) ([]domain.RequestApprovalBlock, error)
}

// CompiledPipelineState owns process-local derived pipeline state. Programs
// never leave this type, and readiness becomes true only after every active
// immutable revision has compiled under the current runtime fingerprint.
type CompiledPipelineState struct {
	repository         compiledPipelineRepository
	registry           *BlockRegistry
	cache              *compiledPipelineCache
	runtimeFingerprint string
	compilationTimeout time.Duration
	compilePipeline    func(domain.RequestApprovalPipelineRevision, []domain.RequestApprovalBlock) (*compiledPipeline, error)

	readinessMu sync.RWMutex
	ready       bool
}

func NewCompiledPipelineState(repository compiledPipelineRepository, registry *BlockRegistry) *CompiledPipelineState {
	return &CompiledPipelineState{
		repository: repository, registry: registry, cache: newCompiledPipelineCache(),
		runtimeFingerprint: registry.Fingerprint(), compilationTimeout: maximumCompilationTime,
		compilePipeline: registry.compileStoredPipeline,
	}
}

// Initialize compiles all revisions active at startup. A failed pass leaves
// the state unavailable, so readiness stays closed and direct request traffic
// safely hands off to a human instead of evaluating partial compiled state.
func (state *CompiledPipelineState) Initialize(ctx context.Context) error {
	state.setReadiness(false)
	afterID := domain.ID("")
	activeKeys := make(map[compiledPipelineKey]struct{})
	for {
		revisions, err := state.repository.ListActiveRequestApprovalPipelineRevisions(ctx, application.ListActiveRequestApprovalPipelineRevisionsInput{
			AfterID: afterID,
			Limit:   startupRevisionPageSize,
		})
		if err != nil {
			state.setReadiness(false)
			return fmt.Errorf("list active request approval pipeline revisions: %w", err)
		}
		for _, revision := range revisions {
			key := state.key(revision.ID)
			if _, err := state.loadRaw(ctx, revision); err != nil {
				state.setReadiness(false)
				return fmt.Errorf("compile active request approval pipeline revision %s: %w", revision.ID, err)
			}
			activeKeys[key] = struct{}{}
			afterID = revision.ID
		}
		if len(revisions) < startupRevisionPageSize {
			break
		}
	}
	state.cache.retain(activeKeys)
	state.setReadiness(true)
	return nil
}

func (state *CompiledPipelineState) Readiness() error {
	state.readinessMu.RLock()
	defer state.readinessMu.RUnlock()
	if state.ready {
		return nil
	}
	return errCompiledPipelineCacheUnavailable
}

func (state *CompiledPipelineState) Fingerprint() string { return state.runtimeFingerprint }

// Grammar is what the edition's block kinds add to the pipeline_blocks
// reference.
func (state *CompiledPipelineState) Grammar() string { return state.registry.Grammar() }

func (state *CompiledPipelineState) Get(ctx context.Context, revision domain.RequestApprovalPipelineRevision) (*compiledPipeline, error) {
	if err := state.Readiness(); err != nil {
		return nil, err
	}
	compiled, err := state.loadRaw(ctx, revision)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errCompiledPipelineCacheUnavailable
	}
	return compiled, nil
}

// Refresh compiles an activation or rollback target before the database
// switch. A failed refresh prevents the revision from becoming active.
func (state *CompiledPipelineState) Refresh(ctx context.Context, revision domain.RequestApprovalPipelineRevision) error {
	_, err := state.loadRaw(ctx, revision)
	return err
}

func (state *CompiledPipelineState) loadRaw(ctx context.Context, revision domain.RequestApprovalPipelineRevision) (*compiledPipeline, error) {
	key := state.key(revision.ID)
	compiled, err := state.cache.load(ctx, key, func() (*compiledPipeline, error) {
		return state.compileRevision(ctx, revision)
	})
	if err != nil {
		return nil, err
	}
	if compiled == nil || compiled.Revision.ID != revision.ID ||
		compiled.Revision.OrganizationID != revision.OrganizationID ||
		compiled.Revision.AgentSlug != revision.AgentSlug ||
		compiled.Revision.Revision != revision.Revision ||
		!slices.Equal(compiled.Revision.BlockIDs, revision.BlockIDs) {
		state.cache.remove(key)
		return nil, errCompiledPipelineCacheUnavailable
	}
	return compiled, nil
}

func (state *CompiledPipelineState) compileRevision(ctx context.Context, revision domain.RequestApprovalPipelineRevision) (*compiledPipeline, error) {
	blocks, err := state.repository.GetRequestApprovalBlocks(ctx, application.GetRequestApprovalBlocksInput{
		OrganizationID: revision.OrganizationID,
		BlockIDs:       revision.BlockIDs,
	})
	if err != nil {
		return nil, err
	}
	type compilationResult struct {
		pipeline *compiledPipeline
		err      error
	}
	result := make(chan compilationResult, 1)
	go func() {
		compiled, compileErr := loadCompiledPipeline(func() (*compiledPipeline, error) {
			return state.compilePipeline(revision, blocks)
		})
		result <- compilationResult{pipeline: compiled, err: compileErr}
	}()
	timer := time.NewTimer(state.compilationTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("request approval pipeline compilation exceeded %s: %w", state.compilationTimeout, domain.ErrInvalid)
	case compiled := <-result:
		return compiled.pipeline, compiled.err
	}
}

func (state *CompiledPipelineState) key(revisionID domain.ID) compiledPipelineKey {
	return compiledPipelineKey{RevisionID: revisionID, RuntimeFingerprint: state.runtimeFingerprint}
}

func (state *CompiledPipelineState) setReadiness(ready bool) {
	state.readinessMu.Lock()
	defer state.readinessMu.Unlock()
	state.ready = ready
}

func (registry *BlockRegistry) compileStoredPipeline(revision domain.RequestApprovalPipelineRevision, snapshots []domain.RequestApprovalBlock) (*compiledPipeline, error) {
	blocks, err := registry.nestSnapshots(snapshots)
	if err != nil {
		return nil, err
	}
	compiled, err := registry.compileDefinitions(pipelineDefinitions(blocks))
	if err != nil {
		return nil, err
	}
	if len(compiled) != len(snapshots) {
		return nil, invalidPipelineDocument("stored blocks do not match the revision's tree", nil)
	}
	for index := range compiled {
		if compiled[index].Key != snapshots[index].BlockKey {
			return nil, invalidPipelineDocument("stored blocks do not match the revision's tree", nil)
		}
	}
	return &compiledPipeline{Revision: revision, Blocks: compiled, Snapshots: snapshots}, nil
}

type compiledPipelineKey struct {
	RevisionID         domain.ID
	RuntimeFingerprint string
}

// compiledBlock is one block of a compiled section in pre-order: a branch
// comes before the blocks it holds. Parent is the index of the branch that
// holds the block, -1 at the top; End is one past the last block inside a
// branch, the next index for any other block.
type compiledBlock struct {
	Key          string
	Executor     RequestApprovalBlock
	Asynchronous bool
	// EscalationPath is the path key the block's human outcome names,
	// empty to inherit the pipeline default. A branch's path is for its
	// own end only.
	EscalationPath string
	// AskAgentOwner sends the human outcome to the agent's owner, with
	// EscalationPath as the fallback.
	AskAgentOwner bool
	Branch        bool
	// Reason is a branch's reason, recorded when nothing inside decides.
	Reason string
	Parent int
	End    int
}

// compiledPipeline is one revision compiled; Blocks and Snapshots share one
// pre-order index, the order of the revision's block_ids.
type compiledPipeline struct {
	Revision  domain.RequestApprovalPipelineRevision
	Blocks    []compiledBlock
	Snapshots []domain.RequestApprovalBlock
}

type compiledPipelineCacheEntry struct {
	done     chan struct{}
	pipeline *compiledPipeline
	err      error
	evict    bool
}

// compiledPipelineCache keeps successful immutable revisions and shares each
// miss. The caller bounds loader duration; failed loads are removed so a
// later activation or request can retry transient dependencies.
type compiledPipelineCache struct {
	mu      sync.Mutex
	entries map[compiledPipelineKey]*compiledPipelineCacheEntry
}

func newCompiledPipelineCache() *compiledPipelineCache {
	return &compiledPipelineCache{entries: make(map[compiledPipelineKey]*compiledPipelineCacheEntry)}
}

func (cache *compiledPipelineCache) load(ctx context.Context, key compiledPipelineKey, loader func() (*compiledPipeline, error)) (*compiledPipeline, error) {
	cache.mu.Lock()
	if entry, exists := cache.entries[key]; exists {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-entry.done:
			return entry.pipeline, entry.err
		}
	}
	entry := &compiledPipelineCacheEntry{done: make(chan struct{})}
	cache.entries[key] = entry
	cache.mu.Unlock()

	entry.pipeline, entry.err = loadCompiledPipeline(loader)
	cache.mu.Lock()
	if entry.err != nil || entry.evict {
		delete(cache.entries, key)
	}
	close(entry.done)
	cache.mu.Unlock()
	return entry.pipeline, entry.err
}

func (cache *compiledPipelineCache) remove(key compiledPipelineKey) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, exists := cache.entries[key]
	if !exists {
		return
	}
	select {
	case <-entry.done:
		delete(cache.entries, key)
	default:
		entry.evict = true
	}
}

func (cache *compiledPipelineCache) retain(keys map[compiledPipelineKey]struct{}) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for key, entry := range cache.entries {
		if _, keep := keys[key]; !keep {
			select {
			case <-entry.done:
				delete(cache.entries, key)
			default:
				entry.evict = true
			}
		}
	}
}

func loadCompiledPipeline(loader func() (*compiledPipeline, error)) (pipeline *compiledPipeline, err error) {
	defer func() {
		if recover() != nil {
			pipeline = nil
			err = fmt.Errorf("compile request approval pipeline: %w", domain.ErrInvalid)
		}
	}()
	return loader()
}

// route is the block's own route for its human outcome.
func (block compiledBlock) route() Route {
	return Route{EscalationPath: block.EscalationPath, AskAgentOwner: block.AskAgentOwner}
}
