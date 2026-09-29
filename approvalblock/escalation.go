package approvalblock

import (
	"errors"
	"regexp"
)

var escalationPathKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ErrAgentOwnerFallbackRequired refuses asking the agent's owner without
// naming the escalation path a request without an owner falls back to.
var ErrAgentOwnerFallbackRequired = errors.New("ask_agent_owner requires escalation_path as the fallback")

// ValidEscalationPathKey reports whether key has the shape of an
// escalation path key. Whether the path exists is a repository question
// answered at revision creation and activation.
func ValidEscalationPathKey(key string) bool {
	return escalationPathKeyPattern.MatchString(key)
}

// ValidateRoute checks one route: an optional escalation path key, and
// asking the agent's owner only beside the path it falls back to.
func ValidateRoute(escalationPath string, askAgentOwner bool) error {
	if askAgentOwner && escalationPath == "" {
		return ErrAgentOwnerFallbackRequired
	}
	if escalationPath != "" && !ValidEscalationPathKey(escalationPath) {
		return errors.New("escalation_path must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	return nil
}

// EscalationPath returns the escalation path key the block's configuration
// names for its human outcome, or "" when the block inherits the pipeline
// default. A branch's path is the one its own end hands a request to. A
// delegate names none: the target pipeline owns its end.
func (definition Definition) EscalationPath() string {
	switch definition.Type {
	case TypeDelegate:
		return ""
	case TypeBranch:
		if definition.Branch != nil {
			return definition.Branch.EscalationPath
		}
	case TypeOutcome:
		if definition.Outcome != nil {
			return definition.Outcome.EscalationPath
		}
	case TypeWebhook:
		if definition.Webhook != nil {
			return definition.Webhook.EscalationPath
		}
	default:
		if routed, ok := definition.Config.(RoutedConfig); ok {
			return routed.EscalationPath()
		}
	}
	return ""
}

// AskAgentOwner reports whether the block's human outcome asks the agent's
// owner first. It travels with the block's own path: a block that names no
// path never asks, and inherits the pipeline default's route whole.
func (definition Definition) AskAgentOwner() bool {
	switch definition.Type {
	case TypeBranch:
		return definition.Branch != nil && definition.Branch.AskAgentOwner
	case TypeOutcome:
		return definition.Outcome != nil && definition.Outcome.AskAgentOwner
	case TypeWebhook:
		return definition.Webhook != nil && definition.Webhook.AskAgentOwner
	default:
		if routed, ok := definition.Config.(OwnerRoutedConfig); ok {
			return routed.AskAgentOwner()
		}
	}
	return false
}
