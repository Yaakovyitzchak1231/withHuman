package approvalblock

import (
	"errors"
	"strings"
)

// TypeOutcome returns a fixed outcome for every request that reaches it.
// "If this, then that" is a branch whose blocks end in one.
const TypeOutcome BlockType = "outcome"

type OutcomeConfig struct {
	Outcome        Outcome `json:"outcome"`
	Reason         string  `json:"reason"`
	EscalationPath string  `json:"escalation_path,omitempty"`
	// AskAgentOwner sends the request to the agent's owner, with
	// EscalationPath as the fallback for a request without one.
	AskAgentOwner bool `json:"ask_agent_owner,omitempty"`
}

func (config OutcomeConfig) Validate() error {
	if config.Outcome != Approve && config.Outcome != Deny && config.Outcome != Human {
		return errors.New("outcome must be approve, deny, or human")
	}
	if strings.TrimSpace(config.Reason) == "" {
		return errors.New("block reason is required")
	}
	if config.EscalationPath != "" && config.Outcome != Human {
		return errors.New("escalation_path requires outcome to be human")
	}
	return ValidateRoute(config.EscalationPath, config.AskAgentOwner)
}

// DefineOutcome builds an outcome block with the synchronous defaults.
func DefineOutcome(id string, outcome Outcome, options ...Option) *Definition {
	definition := &Definition{
		ID: id, Type: TypeOutcome, Enabled: true,
		Timeout: DefaultTimeout.String(), MaxAttempts: 1,
		Outcome: &OutcomeConfig{Outcome: outcome},
	}
	for _, option := range options {
		if option != nil {
			option(definition)
		}
	}
	return definition
}
