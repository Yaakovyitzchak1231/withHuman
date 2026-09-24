package webhookdelivery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/withHumanAI/withHuman/approvalblock"
)

const (
	// PayloadVersion versions the delivery body; the type field says which
	// use produced the delivery so one receiver can serve several.
	PayloadVersion       = "2026-09-01"
	TypeBlockDelivery    = "request_approval_block.webhook"
	TypeEndpointTest     = "webhook_endpoint.test"
	MaximumMetadataBytes = 16 * 1024
)

// Payload is the delivery body. Request fields beyond tool, arguments, and
// the provider-owned agent fields appear only when the use includes them.
type Payload struct {
	Version          string                                `json:"version"`
	Type             string                                `json:"type"`
	Preview          bool                                  `json:"preview"`
	DeliveryID       string                                `json:"delivery_id"`
	IdempotencyKey   string                                `json:"idempotency_key"`
	OrganizationID   string                                `json:"organization_id"`
	Block            *PayloadBlock                         `json:"block,omitempty"`
	Pipeline         *PayloadPipeline                      `json:"pipeline,omitempty"`
	Request          PayloadRequest                        `json:"request"`
	Agent            PayloadAgent                          `json:"agent"`
	PreviousMetadata map[string]map[string]json.RawMessage `json:"previous_metadata,omitempty"`
}

type PayloadBlock struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts"`
}

type PayloadPipeline struct {
	EvaluationID string `json:"evaluation_id"`
}

type PayloadRequest struct {
	ID             string          `json:"id,omitempty"`
	Tool           string          `json:"tool"`
	Server         string          `json:"server,omitempty"`
	Arguments      json.RawMessage `json:"arguments"`
	AgentReasoning *string         `json:"agent_reasoning,omitempty"`
	Context        json.RawMessage `json:"context,omitempty"`
	CreatedAt      *time.Time      `json:"created_at,omitempty"`
	DeadlineAt     *time.Time      `json:"deadline_at,omitempty"`
}

type PayloadAgent struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// NestMetadata turns the executor's "scope/block_key" map into the nested
// shape receivers and CEL conditions read. Keys without a scope are
// dropped.
func NestMetadata(flat map[string]json.RawMessage) map[string]map[string]json.RawMessage {
	nested := make(map[string]map[string]json.RawMessage)
	for key, value := range flat {
		scope, blockKey, found := strings.Cut(key, "/")
		if !found || scope == "" || blockKey == "" {
			continue
		}
		if nested[scope] == nil {
			nested[scope] = make(map[string]json.RawMessage)
		}
		nested[scope][blockKey] = value
	}
	return nested
}

// Answer is the closed response shape. Unknown fields are rejected.
type Answer struct {
	Outcome  approvalblock.Outcome      `json:"outcome"`
	Reason   string                     `json:"reason,omitempty"`
	Metadata map[string]json.RawMessage `json:"metadata,omitempty"`
}

var ErrInvalidAnswer = errors.New("webhook answer is invalid")

// ParseAnswer decodes and bounds an endpoint's answer. It does not check
// the block's allowed outcomes; that is the block's decision.
func ParseAnswer(body []byte) (Answer, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var answer Answer
	if err := decoder.Decode(&answer); err != nil {
		return Answer{}, fmt.Errorf("%w: %v", ErrInvalidAnswer, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Answer{}, fmt.Errorf("%w: body must contain one JSON value", ErrInvalidAnswer)
	}
	switch answer.Outcome {
	case approvalblock.Next, approvalblock.Human, approvalblock.Approve, approvalblock.Deny:
	default:
		return Answer{}, fmt.Errorf("%w: outcome %q is not next, human, approve, or deny", ErrInvalidAnswer, answer.Outcome)
	}
	answer.Reason = strings.TrimSpace(answer.Reason)
	if utf8.RuneCountInString(answer.Reason) > approvalblock.MaximumWebhookReasonChars {
		return Answer{}, fmt.Errorf("%w: reason exceeds %d characters", ErrInvalidAnswer, approvalblock.MaximumWebhookReasonChars)
	}
	for key, value := range answer.Metadata {
		if key == "" || !json.Valid(value) {
			return Answer{}, fmt.Errorf("%w: metadata must be an object of JSON values", ErrInvalidAnswer)
		}
	}
	if answer.Metadata != nil {
		encoded, err := json.Marshal(answer.Metadata)
		if err != nil || len(encoded) > MaximumMetadataBytes {
			return Answer{}, fmt.Errorf("%w: metadata exceeds %d bytes", ErrInvalidAnswer, MaximumMetadataBytes)
		}
	}
	return answer, nil
}
