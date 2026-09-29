package approvalpipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/withHumanAI/withHuman/approvalblock"
)

const OutcomeRuntimeFingerprint = "withhuman-outcome-block/v1;limits/v1"

type compiledOutcomeBlock struct {
	enabled bool
	config  approvalblock.OutcomeConfig
}

func compileOutcomeBlock(definition approvalblock.Definition) (RequestApprovalBlock, error) {
	if err := definition.Validate(); err != nil {
		return nil, fmt.Errorf("validate outcome block: %w", err)
	}
	if definition.Type != approvalblock.TypeOutcome || definition.Outcome == nil {
		return nil, errors.New("definition is not an outcome block")
	}
	return &compiledOutcomeBlock{enabled: definition.Enabled, config: *definition.Outcome}, nil
}

func (block *compiledOutcomeBlock) Evaluate(ctx context.Context, _ RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
	if !block.enabled {
		return RequestApprovalBlockResult{Outcome: BlockNext}, nil
	}
	if ctx == nil {
		return RequestApprovalBlockResult{}, executionError(ErrorInvalidInput, errors.New("context is required"))
	}
	if err := ctx.Err(); err != nil {
		return RequestApprovalBlockResult{}, executionError(ErrorTimeout, err)
	}
	return RequestApprovalBlockResult{Outcome: blockOutcome(block.config.Outcome), Reason: block.config.Reason}, nil
}
