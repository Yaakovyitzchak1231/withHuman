package approvalpipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/withHumanAI/withHuman/approvalblock"
)

const AlwaysRuntimeFingerprint = "withhuman-always-block/v1;limits/v1"

type compiledAlwaysBlock struct {
	enabled bool
	config  approvalblock.AlwaysConfig
}

func compileAlwaysBlock(definition approvalblock.Definition) (RequestApprovalBlock, error) {
	if err := definition.Validate(); err != nil {
		return nil, fmt.Errorf("validate always block: %w", err)
	}
	if definition.Type != approvalblock.TypeAlways || definition.Always == nil {
		return nil, errors.New("definition is not an always block")
	}
	return &compiledAlwaysBlock{enabled: definition.Enabled, config: *definition.Always}, nil
}

func (block *compiledAlwaysBlock) Evaluate(ctx context.Context, _ RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
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
