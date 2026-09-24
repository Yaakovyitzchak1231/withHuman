package approvalblock

import (
	"errors"
	"regexp"
)

var escalationPathKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidEscalationPathKey reports whether key has the shape of an
// escalation path key. Whether the path exists is a repository question
// answered at revision creation and activation.
func ValidEscalationPathKey(key string) bool {
	return escalationPathKeyPattern.MatchString(key)
}

// EscalationPath returns the escalation path key the block's configuration
// names for its human outcome, or "" when the block inherits the pipeline
// default. A branch's path is the one its own end hands a request to.
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

func validateOptionalEscalationPath(key string) error {
	if key == "" {
		return nil
	}
	if !ValidEscalationPathKey(key) {
		return errors.New("escalation_path must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	return nil
}
