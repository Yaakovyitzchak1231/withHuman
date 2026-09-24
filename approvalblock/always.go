package approvalblock

import (
	"errors"
	"strings"
)

// TypeAlways returns a fixed outcome for every request that reaches it.
const TypeAlways BlockType = "always"

type AlwaysConfig struct {
	Outcome        Outcome `json:"outcome"`
	Reason         string  `json:"reason"`
	EscalationPath string  `json:"escalation_path,omitempty"`
}

func (config AlwaysConfig) Validate() error {
	if config.Outcome != Approve && config.Outcome != Deny && config.Outcome != Human {
		return errors.New("outcome must be approve, deny, or human")
	}
	if strings.TrimSpace(config.Reason) == "" {
		return errors.New("block reason is required")
	}
	if config.EscalationPath != "" && config.Outcome != Human {
		return errors.New("escalation_path requires outcome to be human")
	}
	return validateOptionalEscalationPath(config.EscalationPath)
}

// DefineAlways builds an unconditional block with the synchronous defaults.
func DefineAlways(id string, outcome Outcome, options ...Option) *Definition {
	definition := &Definition{
		ID: id, Type: TypeAlways, Enabled: true,
		Timeout: DefaultTimeout.String(), MaxAttempts: 1,
		Always: &AlwaysConfig{Outcome: outcome},
	}
	for _, option := range options {
		if option != nil {
			option(definition)
		}
	}
	return definition
}
