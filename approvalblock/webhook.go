package approvalblock

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// TypeWebhook signs and posts the canonical request to an HTTPS endpoint the
// organization operates and maps its closed JSON answer onto a pipeline
// outcome, bounded by the outcomes the block allows.
const TypeWebhook BlockType = "webhook"

const (
	DefaultWebhookTimeout          = 5 * time.Second
	MaximumWebhookTimeout          = 30 * time.Second
	MaximumWebhookAttempts         = 3
	MaximumWebhookBlocksPerRequest = 5
	MaximumWebhookReasonChars      = 1_024
)

// WebhookInclude selects which untrusted or unmodeled request fields are
// sent besides tool, arguments, and the provider-owned agent fields. Same
// shape a kind that reads them shares.
type WebhookInclude struct {
	AgentReasoning   bool `json:"agent_reasoning"`
	Context          bool `json:"context"`
	PreviousMetadata bool `json:"previous_metadata"`
}

// WebhookConfig is the type-specific configuration of a webhook block. The
// endpoint (URL and signing secret) is a separate revisioned organization
// resource referenced by its stable key, so the immutable, content-hashed
// block never carries a secret and a new endpoint revision never
// re-revisions the pipelines that use it.
type WebhookConfig struct {
	EndpointKey string `json:"endpoint_key"`
	// AllowedOutcomes is the configured transition set: an answer outside
	// it fails closed to human. approve and deny must be granted explicitly.
	AllowedOutcomes []Outcome      `json:"allowed_outcomes"`
	Include         WebhookInclude `json:"include"`
	Reason          string         `json:"reason"`
	// EscalationPath is the key of the escalation path a human answer, or
	// a failure that falls closed to human, hands the request to. Empty
	// inherits the pipeline default.
	EscalationPath string `json:"escalation_path,omitempty"`
}

// Allows reports whether the endpoint may return outcome.
func (config WebhookConfig) Allows(outcome Outcome) bool {
	return slices.Contains(config.AllowedOutcomes, outcome)
}

func (config WebhookConfig) Validate() error {
	if !ValidWebhookEndpointKey(config.EndpointKey) {
		return errors.New("endpoint_key must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	if len(config.AllowedOutcomes) == 0 {
		return errors.New("allowed_outcomes must list at least one outcome")
	}
	seen := make(map[Outcome]struct{}, len(config.AllowedOutcomes))
	for _, outcome := range config.AllowedOutcomes {
		switch outcome {
		case Next, Human, Approve, Deny:
		default:
			return fmt.Errorf("allowed outcome %q must be next, human, approve, or deny", outcome)
		}
		if _, duplicate := seen[outcome]; duplicate {
			return fmt.Errorf("allowed outcome %q is duplicated", outcome)
		}
		seen[outcome] = struct{}{}
	}
	if strings.TrimSpace(config.Reason) == "" {
		return errors.New("block reason is required")
	}
	return validateOptionalEscalationPath(config.EscalationPath)
}

// DefaultWebhookAllowedOutcomes makes a new webhook block an enrichment or
// escalation step until an administrator grants automatic outcomes.
func DefaultWebhookAllowedOutcomes() []Outcome { return []Outcome{Next, Human} }

// DefineWebhook builds a webhook block document with the webhook defaults:
// a 5 second attempt timeout, three attempts, previous metadata included,
// and next or human as the allowed outcomes.
func DefineWebhook(id string, options ...Option) *Definition {
	definition := &Definition{
		ID:          id,
		Type:        TypeWebhook,
		Enabled:     true,
		Timeout:     DefaultWebhookTimeout.String(),
		MaxAttempts: MaximumWebhookAttempts,
		Webhook: &WebhookConfig{
			AllowedOutcomes: DefaultWebhookAllowedOutcomes(),
			Include:         WebhookInclude{PreviousMetadata: true},
		},
	}
	for _, option := range options {
		if option != nil {
			option(definition)
		}
	}
	return definition
}

// ValidWebhookEndpointKey reports whether key has the shape of a webhook
// endpoint key, the same registry key shape every keyed object uses.
func ValidWebhookEndpointKey(key string) bool {
	return escalationPathKeyPattern.MatchString(key)
}

// Endpoint sets the key of the webhook endpoint the block delivers to.
func Endpoint(key string) Option {
	return func(definition *Definition) {
		if definition.Webhook != nil {
			definition.Webhook.EndpointKey = key
		}
	}
}

// AllowedOutcomes replaces the outcomes the endpoint may return.
func AllowedOutcomes(outcomes ...Outcome) Option {
	return func(definition *Definition) {
		if definition.Webhook != nil {
			definition.Webhook.AllowedOutcomes = slices.Clone(outcomes)
		}
	}
}

// Sending selects the untrusted or unmodeled fields a webhook block sends.
func Sending(include WebhookInclude) Option {
	return func(definition *Definition) {
		if definition.Webhook != nil {
			definition.Webhook.Include = include
		}
	}
}
