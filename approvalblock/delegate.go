package approvalblock

import (
	"errors"
	"regexp"
)

// TypeDelegate hands the requests its condition matches to another
// pipeline, named by key. It is a branch whose body is that pipeline:
// a match enters the target's blocks and commits the request to them, a
// decision inside ends the run, and a target that runs out of blocks
// hands the request to a human under the target's own default
// escalation path (else the enclosing pipelines', nearest first). A
// request the condition does not match skips to the next block. The
// condition belongs to the pipeline holding the block; everything after
// a match belongs to the target pipeline's owners.
const TypeDelegate BlockType = "delegate"

// EntryPipelineKey is the reserved key of every organization's entry
// pipeline, where every request starts. It can never be a delegate's
// target: reaching it again would be a cycle.
const EntryPipelineKey = "entry"

var pipelineKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidPipelineKey reports whether key has the shape of a pipeline key.
// Whether the pipeline exists is a repository question answered at
// revision creation and activation.
func ValidPipelineKey(key string) bool {
	return pipelineKeyPattern.MatchString(key)
}

// DelegateConfig is the type-specific configuration of a delegate block.
// It carries no reason and no escalation path: the target pipeline owns
// what happens to a request that enters it.
type DelegateConfig struct {
	When        Condition `json:"when"`
	PipelineKey string    `json:"pipeline_key"`
}

// Validate checks the target key and the condition. The target's
// existence is checked at revision creation, its liveness at activation.
func (config DelegateConfig) Validate() error {
	if !ValidPipelineKey(config.PipelineKey) {
		return errors.New("pipeline_key must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	if config.PipelineKey == EntryPipelineKey {
		return errors.New("a delegate cannot name the entry pipeline")
	}
	state := validationState{ids: make(map[string]struct{})}
	return state.validateCondition(config.When, 1, false)
}

// DefineDelegate builds a delegate block to pipelineKey with the
// synchronous envelope a condition uses.
func DefineDelegate(id, pipelineKey string, options ...Option) *Definition {
	definition := &Definition{
		ID: id, Type: TypeDelegate, Enabled: true,
		Timeout: DefaultTimeout.String(), MaxAttempts: 1,
		Delegate: &DelegateConfig{PipelineKey: pipelineKey},
	}
	for _, option := range options {
		if option != nil {
			option(definition)
		}
	}
	return definition
}
