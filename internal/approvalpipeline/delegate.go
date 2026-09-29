package approvalpipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/withHumanAI/withHuman/approvalblock"
)

// DelegateRuntimeFingerprint versions delegate semantics: enter or skip on
// the condition, commit on entry, the target pipeline's blocks spliced in
// after the block, and the closing human outcome when the target runs out.
const DelegateRuntimeFingerprint = "withhuman-delegate-block/v1;limits/v1"

const delegateConfigVersion = 1

// CompiledDelegateBlock is a delegate's condition. It reports whether a
// request enters the target pipeline and never decides; the executor walks
// the target's blocks, which the effective pipeline splices in after the
// block exactly like a branch's own blocks.
type CompiledDelegateBlock struct {
	enabled bool
	when    compiledWhen
}

// CompileDelegateBlock compiles one delegate's condition. The target
// pipeline is resolved when the effective pipeline is built, never here.
func CompileDelegateBlock(definition approvalblock.Definition) (*CompiledDelegateBlock, error) {
	if err := definition.ValidateBlock(); err != nil {
		return nil, fmt.Errorf("validate delegate block: %w", err)
	}
	if definition.Type != approvalblock.TypeDelegate || definition.Delegate == nil {
		return nil, errors.New("validate delegate block: definition is not a delegate block")
	}
	timeout, _ := time.ParseDuration(definition.Timeout)
	when, err := compileWhen(definition.Delegate.When, timeout)
	if err != nil {
		return nil, fmt.Errorf("compile delegate block: %w", err)
	}
	return &CompiledDelegateBlock{enabled: definition.Enabled, when: when}, nil
}

// Evaluate answers BlockEnter when the condition matches and BlockNext when
// it does not, spending one condition evaluation from the request's budget.
func (block *CompiledDelegateBlock) Evaluate(ctx context.Context, input RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
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
