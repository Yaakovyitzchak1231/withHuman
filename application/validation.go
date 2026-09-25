package application

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

func requiredID(field string, value domain.ID) error {
	if value == "" {
		return invalidField(field, "is required")
	}
	return nil
}

func requiredString(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalidField(field, "is required")
	}
	return nil
}

func validAgentSlug(value domain.AgentSlug) error {
	if !value.Valid() {
		return invalidField("agent slug", "must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	return nil
}

func validAgentKind(value domain.AgentKind) error {
	if !value.Valid() {
		return invalidField("agent kind", "must be service or personal")
	}
	return nil
}

func validPipelineScope(value domain.AgentSlug) error {
	if !domain.ValidPipelineScope(value) {
		return invalidField("pipeline scope", "must be empty for the organization scope or match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	return nil
}

func requiredTime(field string, value time.Time) error {
	if value.IsZero() {
		return invalidField(field, "is required")
	}
	return nil
}

func requiredTokenHash(field string, value []byte) error {
	if len(value) != sha256.Size {
		return invalidField(field, fmt.Sprintf("must be a %d-byte SHA-256 hash", sha256.Size))
	}
	return nil
}

func validJSON(field string, value json.RawMessage) error {
	if len(value) == 0 || !json.Valid(value) {
		return invalidField(field, "must be valid JSON")
	}
	return nil
}

func optionalJSON(field string, value json.RawMessage) error {
	if len(value) > 0 && !json.Valid(value) {
		return invalidField(field, "must be valid JSON")
	}
	return nil
}

func nonNegative(field string, value int64) error {
	if value < 0 {
		return invalidField(field, "must not be negative")
	}
	return nil
}

func firstError(checks []error) error {
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

func invalidField(field, reason string) error {
	return fmt.Errorf("%s %s: %w", field, reason, domain.ErrInvalid)
}

// validScopeSet accepts "everywhere" or a non-empty set of scopes of one
// kind, the shape a list query narrows by. An empty set is refused here
// because the service answers forbidden before building the query.
func validScopeSet(field string, set domain.ScopeSet, kind domain.ScopeKind) error {
	if set.All {
		return nil
	}
	if len(set.Scopes) == 0 {
		return invalidField(field, "must be everywhere or at least one scope")
	}
	for _, scope := range set.Scopes {
		if scope.Kind != kind || scope.ID == "" {
			return invalidField(field, "must only contain "+string(kind)+" scopes")
		}
	}
	return nil
}
