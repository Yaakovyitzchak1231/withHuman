package approvalpipeline

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/withHumanAI/withHuman/blockruntime"
)

// The execution contract is blockruntime's, exported so an edition's
// kinds compile through the same one; these names keep the pipeline's
// own code reading as it always has.
type (
	BlockOutcome               = blockruntime.Outcome
	BlockErrorCode             = blockruntime.ErrorCode
	RequestApprovalBlock       = blockruntime.Block
	RequestApprovalBlockInput  = blockruntime.Input
	BlockRequest               = blockruntime.Request
	BlockAgent                 = blockruntime.Agent
	RequestApprovalBlockResult = blockruntime.Result
	BlockEvidence              = blockruntime.Evidence
	DeliveryEvidence           = blockruntime.DeliveryEvidence
	NodeTrace                  = blockruntime.NodeTrace
	BlockExecutionBudget       = blockruntime.Budget
)

const (
	BlockNext    = blockruntime.Next
	BlockApprove = blockruntime.Approve
	BlockDeny    = blockruntime.Deny
	BlockHuman   = blockruntime.Human
	BlockEnter   = blockruntime.Enter
)

const (
	ErrorInvalidInput        = blockruntime.InvalidInput
	ErrorEvaluation          = blockruntime.Evaluation
	ErrorCostLimit           = blockruntime.CostLimit
	ErrorResourceLimit       = blockruntime.ResourceLimit
	ErrorTimeout             = blockruntime.Timeout
	ErrorInvalidBlockOutput  = blockruntime.InvalidOutput
	ErrorInternal            = blockruntime.Internal
	ErrorProvider            = blockruntime.Provider
	ErrorEndpointUnreachable = blockruntime.EndpointUnreachable
	ErrorEndpointError       = blockruntime.EndpointError
	ErrorEndpointRejected    = blockruntime.EndpointRejected
	ErrorOutcomeNotAllowed   = blockruntime.OutcomeNotAllowed
	ErrorEndpointUnavailable = blockruntime.EndpointUnavailable
)

// coreErrorCodes are the codes core's kinds report; a registered runtime
// adds its own through Runtime.ErrorCodes.
var coreErrorCodes = map[BlockErrorCode]bool{
	ErrorInvalidInput: true, ErrorEvaluation: true, ErrorCostLimit: true, ErrorResourceLimit: true,
	ErrorTimeout: true, ErrorInvalidBlockOutput: true, ErrorInternal: true, ErrorProvider: true,
	ErrorEndpointUnreachable: true, ErrorEndpointError: true, ErrorEndpointRejected: true,
	ErrorOutcomeNotAllowed: true, ErrorEndpointUnavailable: true,
}

// registeredErrorCodes are the codes edition runtimes declared, in any
// registry of this process; a code only ever joins the set.
var registeredErrorCodes sync.Map

func executionError(code BlockErrorCode, err error) error {
	return blockruntime.ExecutionError(code, err)
}

// ExecuteRequestApprovalBlock turns every runtime error, panic, or ambiguous
// result into human review. It is the only safe way for a pipeline executor to
// invoke a block.
func ExecuteRequestApprovalBlock(ctx context.Context, block RequestApprovalBlock, input RequestApprovalBlockInput) RequestApprovalBlockResult {
	return executeBlock(ctx, block, input, validOutcome)
}

// executeBranchCondition is the same boundary for a branch's condition,
// which may only enter the branch or skip it. Anything else, including a
// failed evaluation, is human review, never a silent skip.
func executeBranchCondition(ctx context.Context, block RequestApprovalBlock, input RequestApprovalBlockInput) RequestApprovalBlockResult {
	return executeBlock(ctx, block, input, func(outcome BlockOutcome) bool {
		return outcome == BlockEnter || outcome == BlockNext
	})
}

func executeBlock(ctx context.Context, block RequestApprovalBlock, input RequestApprovalBlockInput, allowed func(BlockOutcome) bool) (result RequestApprovalBlockResult) {
	result = humanResult(ErrorInternal)
	if block == nil {
		return result
	}
	defer func() {
		if recover() != nil {
			result = humanResult(ErrorInternal)
		}
	}()

	evaluated, err := block.Evaluate(ctx, input)
	if err != nil {
		code := ErrorEvaluation
		if reported, ok := blockruntime.ErrorCodeOf(err); ok {
			code = reported
		}
		result = humanResultWithEvidence(code, evaluated.Evidence)
		result.Attempts = evaluated.Attempts
		return result
	}
	if !allowed(evaluated.Outcome) {
		return humanResultWithEvidence(ErrorInvalidBlockOutput, evaluated.Evidence)
	}
	if !validMetadata(evaluated.Metadata) {
		return humanResultWithEvidence(ErrorInvalidBlockOutput, evaluated.Evidence)
	}
	if !validErrorCode(evaluated.ErrorCode) || (evaluated.Outcome != BlockHuman && evaluated.ErrorCode != "") {
		return humanResultWithEvidence(ErrorInvalidBlockOutput, evaluated.Evidence)
	}
	return evaluated
}

func validOutcome(outcome BlockOutcome) bool {
	return outcome == BlockNext || outcome == BlockApprove || outcome == BlockDeny || outcome == BlockHuman
}

func validErrorCode(code BlockErrorCode) bool {
	if code == "" || coreErrorCodes[code] {
		return true
	}
	_, registered := registeredErrorCodes.Load(code)
	return registered
}

func validMetadata(metadata map[string]json.RawMessage) bool {
	for key, value := range metadata {
		if key == "" || !json.Valid(value) {
			return false
		}
	}
	encoded, err := json.Marshal(metadata)
	return err == nil && len(encoded) <= 16*1024
}

func humanResult(code BlockErrorCode) RequestApprovalBlockResult {
	return humanResultWithEvidence(code, BlockEvidence{})
}

func humanResultWithEvidence(code BlockErrorCode, evidence BlockEvidence) RequestApprovalBlockResult {
	return RequestApprovalBlockResult{Outcome: BlockHuman, Evidence: evidence, ErrorCode: code}
}
