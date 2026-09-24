package approvalblock

import (
	"errors"
	"fmt"
	"strings"
)

// TypeBranch runs its own ordered blocks for the requests its condition
// matches and skips them for every other request. A request that enters a
// branch is decided inside it: when none of its blocks decides, the branch
// hands the request to a human with its own reason and escalation path, and
// nothing after the branch runs.
const TypeBranch BlockType = "branch"

// BranchConfig is the type-specific configuration of a branch block. Blocks
// may be of any type, including further branches; every block, nested or
// not, counts toward MaximumPipelineBlocks.
type BranchConfig struct {
	When   Condition    `json:"when"`
	Blocks []Definition `json:"blocks"`
	// Reason is recorded when none of the branch's blocks decides.
	Reason string `json:"reason"`
	// EscalationPath is where a request that runs out of the branch goes;
	// empty uses the pipeline default. It applies to the branch's own end
	// only: a block inside the branch that names no path uses the pipeline
	// default, never this one.
	EscalationPath string `json:"escalation_path,omitempty"`
}

// Validate checks the branch's own condition, reason, and path, and that it
// holds at least one block. The blocks themselves are checked by
// Definition.Validate.
func (config BranchConfig) Validate() error {
	if strings.TrimSpace(config.Reason) == "" {
		return errors.New("block reason is required")
	}
	if err := validateOptionalEscalationPath(config.EscalationPath); err != nil {
		return err
	}
	if len(config.Blocks) == 0 {
		return errors.New("branch must contain at least one block")
	}
	state := validationState{ids: make(map[string]struct{})}
	return state.validateCondition(config.When, 1, false)
}

// DefineBranch builds a branch block around blocks with the synchronous
// envelope a condition uses.
func DefineBranch(id string, blocks []Definition, options ...Option) *Definition {
	definition := &Definition{
		ID: id, Type: TypeBranch, Enabled: true,
		Timeout: DefaultTimeout.String(), MaxAttempts: 1,
		Branch: &BranchConfig{Blocks: blocks},
	}
	for _, option := range options {
		if option != nil {
			option(definition)
		}
	}
	return definition
}

// branchConfigShape is a branch configuration without its blocks, the part
// decoded strictly at each level of a nested document.
type branchConfigShape struct {
	When           Condition `json:"when"`
	Reason         string    `json:"reason"`
	EscalationPath string    `json:"escalation_path,omitempty"`
}

// branchConfigFromValue converts one decoded branch configuration. The
// blocks are converted from the already decoded values rather than decoded
// again from their bytes, so a deeply nested document decodes in one pass.
func branchConfigFromValue(value any) (*BranchConfig, error) {
	if value == nil {
		return &BranchConfig{}, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("branch config must be a JSON object")
	}
	blocksValue, hasBlocks, err := takeMember(object, "blocks")
	if err != nil {
		return nil, err
	}
	var shape branchConfigShape
	if err := decodeStrictValue(object, &shape); err != nil {
		return nil, err
	}
	config := &BranchConfig{When: shape.When, Reason: shape.Reason, EscalationPath: shape.EscalationPath}
	if !hasBlocks || blocksValue == nil {
		return config, nil
	}
	items, ok := blocksValue.([]any)
	if !ok {
		return nil, errors.New("branch blocks must be a JSON array")
	}
	config.Blocks = make([]Definition, len(items))
	for index, item := range items {
		child, err := definitionFromValue(item)
		if err != nil {
			return nil, fmt.Errorf("block %d: %w", index+1, err)
		}
		config.Blocks[index] = child
	}
	return config, nil
}

// branchConfigWire is the wire form of a branch configuration. Its blocks
// are wire values, not definitions with their own marshaler, so a deep tree
// encodes in one pass.
type branchConfigWire struct {
	When           Condition        `json:"when"`
	Blocks         []definitionWire `json:"blocks"`
	Reason         string           `json:"reason"`
	EscalationPath string           `json:"escalation_path,omitempty"`
}

func (config BranchConfig) wire() (branchConfigWire, error) {
	blocks := make([]definitionWire, len(config.Blocks))
	for index, child := range config.Blocks {
		encoded, err := child.wire()
		if err != nil {
			return branchConfigWire{}, fmt.Errorf("block %q: %w", child.ID, err)
		}
		blocks[index] = encoded
	}
	return branchConfigWire{When: config.When, Blocks: blocks, Reason: config.Reason, EscalationPath: config.EscalationPath}, nil
}
