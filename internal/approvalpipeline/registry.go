package approvalpipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/blockruntime"
	"github.com/withHumanAI/withHuman/domain"
)

// BlockRegistry maps block types to their runtimes. The composition root
// constructs it once with every dependency a runtime needs; compiled state,
// management, and execution share the same instance so a block compiles the
// same way everywhere in one process. Core's kinds register first, then
// the edition's, in the order given.
type BlockRegistry struct {
	runtimes     map[approvalblock.BlockType]blockruntime.Runtime
	order        []approvalblock.BlockType
	extra        []blockruntime.Runtime
	webhooks     *WebhookRuntime
	entitlements application.Entitlements
	clock        application.Clock
}

// BlockRegistryOption supplies a runtime dependency.
type BlockRegistryOption func(*BlockRegistry)

// WithRuntimes registers an edition's block kinds after core's. Each
// kind's grammar must already be registered (approvalblock.RegisterKind).
func WithRuntimes(runtimes ...blockruntime.Runtime) BlockRegistryOption {
	return func(registry *BlockRegistry) { registry.extra = append(registry.extra, runtimes...) }
}

// WithEntitlements gates plan-limited block types and meters their usage.
func WithEntitlements(entitlements application.Entitlements) BlockRegistryOption {
	return func(registry *BlockRegistry) { registry.entitlements = entitlements }
}

func WithClock(clock application.Clock) BlockRegistryOption {
	return func(registry *BlockRegistry) { registry.clock = clock }
}

func NewBlockRegistry(options ...BlockRegistryOption) *BlockRegistry {
	registry := &BlockRegistry{runtimes: make(map[approvalblock.BlockType]blockruntime.Runtime), clock: application.RealClock{}}
	for _, option := range options {
		if option != nil {
			option(registry)
		}
	}
	registry.register(blockruntime.Runtime{
		Type: approvalblock.TypeOutcome, ConfigVersion: 1, Fingerprint: OutcomeRuntimeFingerprint, Compile: compileOutcomeBlock,
	})
	registry.register(blockruntime.Runtime{
		Type: approvalblock.TypeWebhook, ConfigVersion: webhookConfigVersion, Fingerprint: WebhookRuntimeFingerprint,
		Asynchronous: true, Capability: application.CapabilityWebhookBlock,
		Compile: func(definition approvalblock.Definition) (RequestApprovalBlock, error) {
			compiled, err := CompileWebhookBlock(definition, registry.webhooks, registry.entitlements, registry.clock)
			if err != nil {
				return nil, err
			}
			return compiled, nil
		},
		Problem: func(definition approvalblock.Definition) (string, error) {
			if registry.webhooks == nil {
				return blockProblemWebhookUnconfigured, nil
			}
			return "", nil
		},
	})
	registry.register(blockruntime.Runtime{
		Type: approvalblock.TypeBranch, ConfigVersion: branchConfigVersion, Fingerprint: BranchRuntimeFingerprint,
		Compile: func(definition approvalblock.Definition) (RequestApprovalBlock, error) {
			compiled, err := CompileBranchBlock(definition)
			if err != nil {
				return nil, err
			}
			return compiled, nil
		},
	})
	registry.register(blockruntime.Runtime{
		Type: approvalblock.TypeDelegate, ConfigVersion: delegateConfigVersion, Fingerprint: DelegateRuntimeFingerprint,
		Compile: func(definition approvalblock.Definition) (RequestApprovalBlock, error) {
			compiled, err := CompileDelegateBlock(definition)
			if err != nil {
				return nil, err
			}
			return compiled, nil
		},
	})
	for _, runtime := range registry.extra {
		if !approvalblock.KnownType(runtime.Type) || runtime.Compile == nil {
			panic(fmt.Sprintf("approvalpipeline: runtime %q needs a registered kind and a compiler", runtime.Type))
		}
		if _, taken := registry.runtimes[runtime.Type]; taken {
			panic(fmt.Sprintf("approvalpipeline: runtime %q registered twice", runtime.Type))
		}
		for _, code := range runtime.ErrorCodes {
			registeredErrorCodes.Store(code, true)
		}
		registry.register(runtime)
	}
	return registry
}

// WebhooksConfigured reports whether webhook blocks can execute in this process.
func (registry *BlockRegistry) WebhooksConfigured() bool { return registry.webhooks != nil }

func (registry *BlockRegistry) register(runtime blockruntime.Runtime) {
	registry.runtimes[runtime.Type] = runtime
	registry.order = append(registry.order, runtime.Type)
}

// Fingerprint composes every registered runtime's fingerprint in
// registration order. Any change to a runtime's semantics, limits, or
// dependencies changes the composite and therefore the compiled cache key.
func (registry *BlockRegistry) Fingerprint() string {
	parts := make([]string, 0, len(registry.order))
	for _, blockType := range registry.order {
		parts = append(parts, registry.runtimes[blockType].Fingerprint)
	}
	return strings.Join(parts, ";")
}

// Grammar is the edition kinds' sections of the pipeline_blocks
// reference, in registration order, appended after core's grammar.
func (registry *BlockRegistry) Grammar() string {
	var builder strings.Builder
	for _, runtime := range registry.extra {
		if runtime.Grammar != "" {
			builder.WriteString("\n")
			builder.WriteString(runtime.Grammar)
		}
	}
	return builder.String()
}

// problem reports what stands in the way of running the definition on
// this deployment: a problem code for the references list and the
// refusal a write answers with, both empty when it can run.
func (registry *BlockRegistry) problem(definition approvalblock.Definition) (string, error) {
	runtime, known := registry.runtime(definition.Type)
	if !known || runtime.Problem == nil {
		return "", nil
	}
	return runtime.Problem(definition)
}

// capability is the plan capability the kind is gated on, if any.
func (registry *BlockRegistry) capability(blockType approvalblock.BlockType) (application.Capability, bool) {
	runtime, known := registry.runtime(blockType)
	if !known || runtime.Capability == "" {
		return "", false
	}
	return runtime.Capability, true
}

func (registry *BlockRegistry) runtime(blockType approvalblock.BlockType) (blockruntime.Runtime, bool) {
	runtime, known := registry.runtimes[blockType]
	return runtime, known
}

// compileDefinitions validates the pipeline-wide invariants over every
// block of the document, nested ones included, and compiles each through
// its type's runtime. The result is the document in pre-order.
func (registry *BlockRegistry) compileDefinitions(definitions []approvalblock.Definition) ([]compiledBlock, error) {
	nodes, err := flattenDocument(definitions)
	if err != nil {
		return nil, err
	}
	blockKeys := make(map[string]struct{}, len(nodes))
	conditionIDs := make(map[string]struct{})
	compiled := make([]compiledBlock, len(nodes))
	for index, node := range nodes {
		definition := node.Definition
		if !definition.Enabled {
			return nil, invalidPipelineDocument(fmt.Sprintf("block %q must be enabled or omitted", definition.ID), nil)
		}
		timeout, timeoutErr := time.ParseDuration(definition.Timeout)
		if timeoutErr == nil && (timeout < time.Millisecond || timeout%time.Millisecond != 0) {
			return nil, invalidPipelineDocument(fmt.Sprintf("block %q timeout must be a whole number of milliseconds", definition.ID), nil)
		}
		if _, exists := blockKeys[definition.ID]; exists {
			return nil, invalidPipelineDocument(fmt.Sprintf("block id %q is duplicated", definition.ID), nil)
		}
		blockKeys[definition.ID] = struct{}{}
		if condition, ok := definitionCondition(definition); ok {
			if duplicate := firstDuplicateConditionID(condition, conditionIDs); duplicate != "" {
				return nil, invalidPipelineDocument(fmt.Sprintf("condition id %q is duplicated across the pipeline", duplicate), nil)
			}
		}
		runtime, known := registry.runtime(definition.Type)
		if !known {
			return nil, invalidPipelineDocument(fmt.Sprintf("block %q uses unsupported type %q", definition.ID, definition.Type), nil)
		}
		block, err := runtime.Compile(definition)
		if err != nil {
			var unconfigured *blockruntime.Unconfigured
			if errors.As(err, &unconfigured) || errors.Is(err, domain.ErrWebhookUnconfigured) {
				return nil, fmt.Errorf("block %q: %w", definition.ID, err)
			}
			return nil, invalidPipelineDocument(fmt.Sprintf("block %q failed validation or compilation", definition.ID), err)
		}
		compiled[index] = compiledBlock{
			Key: definition.ID, Executor: block, Asynchronous: runtime.Asynchronous,
			EscalationPath: definition.EscalationPath(), AskAgentOwner: definition.AskAgentOwner(),
			Parent: node.Parent, End: node.End,
		}
		switch definition.Type {
		case approvalblock.TypeBranch:
			compiled[index].Branch = true
			compiled[index].Reason = definition.Branch.Reason
		case approvalblock.TypeDelegate:
			compiled[index].Delegate = true
			compiled[index].PipelineKey = definition.Delegate.PipelineKey
		}
	}
	return compiled, nil
}

// definitionFromSnapshot rebuilds the authoring document from an immutable
// stored block. A snapshot whose type or configuration version this process
// cannot interpret is invalid, never silently skipped. A branch comes back
// without its blocks, with the keys of the blocks that follow it.
func (registry *BlockRegistry) definitionFromSnapshot(block domain.RequestApprovalBlock) (approvalblock.Definition, []string, error) {
	runtime, known := registry.runtime(approvalblock.BlockType(block.BlockType))
	if !known || block.ConfigVersion != runtime.ConfigVersion {
		return approvalblock.Definition{}, nil, invalidPipelineDocument(
			fmt.Sprintf("block %q uses unsupported type %q or config version %d", block.BlockKey, block.BlockType, block.ConfigVersion), nil)
	}
	definition := approvalblock.Definition{
		ID: block.BlockKey, Name: block.Name, Type: approvalblock.BlockType(block.BlockType), Enabled: true,
		Timeout: (time.Duration(block.TimeoutMS) * time.Millisecond).String(), MaxAttempts: block.MaxAttempts,
	}
	if definition.Type == approvalblock.TypeBranch {
		childKeys, err := decodeBranchStoredConfig(&definition, block.Config)
		if err != nil {
			return approvalblock.Definition{}, nil, invalidPipelineDocument(fmt.Sprintf("block %q has invalid config", block.BlockKey), err)
		}
		return definition, childKeys, nil
	}
	if err := definition.SetConfigJSON(block.Config); err != nil {
		return approvalblock.Definition{}, nil, invalidPipelineDocument(fmt.Sprintf("block %q has invalid config", block.BlockKey), err)
	}
	return definition, nil, nil
}

// snapshotInputs turns validated definitions into immutable block snapshot
// inputs in pre-order, stamping each with its runtime's configuration
// version. A branch is stored with the keys of its blocks, which follow it.
func (registry *BlockRegistry) snapshotInputs(definitions []approvalblock.Definition) ([]application.RequestApprovalBlockSnapshotInput, error) {
	all := flattenDefinitions(definitions)
	blocks := make([]application.RequestApprovalBlockSnapshotInput, len(all))
	for index, definition := range all {
		runtime, known := registry.runtime(definition.Type)
		if !known {
			return nil, invalidPipelineDocument(fmt.Sprintf("block %q uses unsupported type %q", definition.ID, definition.Type), nil)
		}
		var config json.RawMessage
		var err error
		if definition.Type == approvalblock.TypeBranch {
			config, err = branchStoredConfigJSON(definition)
		} else {
			config, err = definition.ConfigJSON()
		}
		if err != nil {
			return nil, invalidPipelineDocument(fmt.Sprintf("block %q config cannot be encoded", definition.ID), err)
		}
		timeout, _ := time.ParseDuration(definition.Timeout)
		blocks[index] = application.RequestApprovalBlockSnapshotInput{
			ID:            domain.ID(uuid.NewString()),
			BlockKey:      definition.ID,
			Name:          definition.Name,
			BlockType:     string(definition.Type),
			ConfigVersion: runtime.ConfigVersion,
			Config:        config,
			TimeoutMS:     int(timeout.Milliseconds()),
			MaxAttempts:   definition.MaxAttempts,
		}
	}
	return blocks, nil
}
