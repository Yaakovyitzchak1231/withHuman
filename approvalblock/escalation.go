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
// default. A branch's path is the one its own end hands a request to. With
// AskAgentOwner the path is the fallback.
func (definition Definition) EscalationPath() string {
	switch definition.Type {
	case TypeBranch:
		if definition.Branch != nil {
			return definition.Branch.EscalationPath
		}
	case TypeAlways:
		if definition.Always != nil {
			return definition.Always.EscalationPath
		}
	case TypeCEL:
		if definition.CEL != nil {
			return definition.CEL.EscalationPath
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
	case TypeAlways:
		return definition.Always != nil && definition.Always.AskAgentOwner
	case TypeCEL:
		return definition.CEL != nil && definition.CEL.AskAgentOwner
	case TypeWebhook:
		return definition.Webhook != nil && definition.Webhook.AskAgentOwner
	default:
		if routed, ok := definition.Config.(OwnerRoutedConfig); ok {
			return routed.AskAgentOwner()
		}
	}
	return false
}
