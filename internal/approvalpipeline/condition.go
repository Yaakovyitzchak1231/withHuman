package approvalpipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

// EvaluateRequestCondition runs one pipeline-shaped condition envelope
// against a request outside any pipeline: no budget, no previous
// metadata, no block. Escalation paths use it for their request branches,
// which read the same request, agent, and argument fields a CEL block
// does but decide who is paged rather than whether a human is asked.
// The request is fixed for its lifetime, so the caller evaluates each
// condition once and the escalation machine reads the stored result.
// An evaluation failure is an error, never a silent false, so the caller
// can decide how a broken branch should route.
func EvaluateRequestCondition(ctx context.Context, condition approvalblock.Condition, request domain.ApprovalRequest, agent domain.Agent) (bool, error) {
	if ctx == nil {
		return false, errors.New("context is required")
	}
	if err := approvalblock.ValidateCondition(condition); err != nil {
		return false, fmt.Errorf("validate request condition: %w", err)
	}
	compiler := conditionCompiler{}
	compiled, err := compiler.compile(condition, false)
	if err != nil {
		return false, fmt.Errorf("compile request condition: %w", err)
	}
	root, err := buildSafeInput(newBlockInput(request, agent, nil))
	if err != nil {
		return false, fmt.Errorf("prepare request condition input: %w", err)
	}
	// The CEL block's default timeout bounds the evaluation exactly as a
	// block would be bounded; a request condition is the same shape.
	evaluationCtx, cancel := context.WithTimeout(ctx, approvalblock.DefaultTimeout)
	defer cancel()
	state := evaluationState{ctx: evaluationCtx, root: root}
	matched, err := state.evaluate(compiled, nil)
	if err != nil {
		return false, fmt.Errorf("evaluate request condition: %w", err)
	}
	return matched, nil
}
