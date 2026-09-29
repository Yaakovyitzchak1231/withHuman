package approvalpipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/withHumanAI/withHuman/approvalblock"
)

// BranchRuntimeFingerprint versions branch semantics: enter or skip on the
// condition, commit on entry, and the closing human outcome when nothing
// inside decides.
const BranchRuntimeFingerprint = "withhuman-branch-block/v1;limits/v1"

const branchConfigVersion = 1

// CompiledBranchBlock is a branch's condition. It reports whether a request
// enters the branch and never decides; the executor walks the branch's
// blocks, which compile as blocks of their own.
type CompiledBranchBlock struct {
	enabled bool
	when    compiledWhen
}

// CompileBranchBlock compiles one branch without visiting its blocks, so a
// tree of branches compiles in time proportional to its size.
func CompileBranchBlock(definition approvalblock.Definition) (*CompiledBranchBlock, error) {
	if err := definition.ValidateBlock(); err != nil {
		return nil, fmt.Errorf("validate branch block: %w", err)
	}
	if definition.Type != approvalblock.TypeBranch || definition.Branch == nil {
		return nil, errors.New("validate branch block: definition is not a branch block")
	}
	timeout, _ := time.ParseDuration(definition.Timeout)
	when, err := compileWhen(definition.Branch.When, timeout)
	if err != nil {
		return nil, fmt.Errorf("compile branch block: %w", err)
	}
	return &CompiledBranchBlock{enabled: definition.Enabled, when: when}, nil
}

// Evaluate answers BlockEnter when the condition matches and BlockNext when
// it does not, spending one condition evaluation from the request's budget.
func (block *CompiledBranchBlock) Evaluate(ctx context.Context, input RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
	if !block.enabled {
		return RequestApprovalBlockResult{Outcome: BlockNext}, nil
	}
	matched, evidence, err := block.when.evaluate(ctx, input)
	if err != nil {
		return RequestApprovalBlockResult{Evidence: evidence}, err
	}
	if !matched {
		return RequestApprovalBlockResult{Outcome: BlockNext, Evidence: evidence}, nil
	}
	return RequestApprovalBlockResult{Outcome: BlockEnter, Evidence: evidence}, nil
}

// branchStoredConfig is how a branch is stored. Every block of a revision,
// nested or not, is its own snapshot listed in pre-order, so a branch's
// blocks follow it in the revision's block_ids; the branch names its direct
// blocks by key, in order, instead of embedding them. That keeps every
// reader of block rows (the key-uniqueness trigger, the activation reference
// guard, the uses queries, routing) seeing nested blocks as ordinary rows.
type branchStoredConfig struct {
	When           approvalblock.Condition `json:"when"`
	BlockKeys      []string                `json:"block_keys"`
	Reason         string                  `json:"reason"`
	EscalationPath string                  `json:"escalation_path,omitempty"`
	AskAgentOwner  bool                    `json:"ask_agent_owner,omitempty"`
}

func branchStoredConfigJSON(definition approvalblock.Definition) (json.RawMessage, error) {
	if definition.Branch == nil {
		return nil, errors.New("branch block requires a branch configuration")
	}
	keys := make([]string, len(definition.Branch.Blocks))
	for index, child := range definition.Branch.Blocks {
		keys[index] = child.ID
	}
	return json.Marshal(branchStoredConfig{
		When: definition.Branch.When, BlockKeys: keys,
		Reason: definition.Branch.Reason, EscalationPath: definition.Branch.EscalationPath,
		AskAgentOwner: definition.Branch.AskAgentOwner,
	})
}

// decodeBranchStoredConfig reads a stored branch into definition, leaving
// its blocks empty, and returns the keys of the blocks that follow it.
func decodeBranchStoredConfig(definition *approvalblock.Definition, config json.RawMessage) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(config))
	decoder.DisallowUnknownFields()
	var stored branchStoredConfig
	if err := decoder.Decode(&stored); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("config must contain one JSON value")
	}
	definition.Branch = &approvalblock.BranchConfig{
		When: stored.When, Reason: stored.Reason, EscalationPath: stored.EscalationPath, AskAgentOwner: stored.AskAgentOwner,
	}
	return stored.BlockKeys, nil
}
