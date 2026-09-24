package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/withHumanAI/withHuman/domain"
)

// ToolUseModel is the provider port behind the setup assistant: one turn
// of a conversation in which the model may call tools. The assistant's
// prompt, tool catalog, loop, transcript, and scope checks are product
// behavior and stay in core; an edition supplies the adapter that carries
// a turn across the wire (runtime.ServerOptions.AssistantModel), and
// without one the guided setup offers its starting rules alone.
// Adapters must not retry on their own.
type ToolUseModel interface {
	CompleteTurn(ctx context.Context, input ToolUseInput) (ToolUseOutput, error)
	// Identity names the provider and model the deployment configured.
	Identity() ModelIdentity
}

// ModelIdentity is the provider and model one adapter calls.
type ModelIdentity struct {
	Provider string
	Model    string
}

// ErrModelUnavailable is a transient provider failure (rate limit,
// overload, network) an adapter reports so the caller can say so or try
// again within its own budget.
var ErrModelUnavailable = errors.New("model is temporarily unavailable")

func (identity ModelIdentity) String() string {
	return identity.Provider + "/" + identity.Model
}

// ToolUseTool is one tool the model may call.
type ToolUseTool struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object describing the tool's input.
	InputSchema json.RawMessage
}

// ToolUseRole is who authored a message.
type ToolUseRole string

const (
	ToolUseRoleUser      ToolUseRole = "user"
	ToolUseRoleAssistant ToolUseRole = "assistant"
)

// ToolUseBlockType is the kind of one content block.
type ToolUseBlockType string

const (
	ToolUseText   ToolUseBlockType = "text"
	ToolUseCall   ToolUseBlockType = "tool_use"
	ToolUseResult ToolUseBlockType = "tool_result"
)

// ToolUseBlock is one provider-neutral content block: text, a tool
// call the model made (ID, Name, Input), or the result the caller hands
// back for one call (ToolUseID, Content, IsError). The JSON shape is
// what the transcript persists.
type ToolUseBlock struct {
	Type      ToolUseBlockType `json:"type"`
	Text      string           `json:"text,omitempty"`
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Input     json.RawMessage  `json:"input,omitempty"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	Content   string           `json:"content,omitempty"`
	IsError   bool             `json:"is_error,omitempty"`
}

// ToolUseMessage is one message of the conversation. Tool results travel
// in a user message, as every provider expects.
type ToolUseMessage struct {
	Role   ToolUseRole    `json:"role"`
	Blocks []ToolUseBlock `json:"blocks"`
}

type ToolUseInput struct {
	SystemPrompt    string
	Messages        []ToolUseMessage
	Tools           []ToolUseTool
	MaxOutputTokens int
}

func (input ToolUseInput) Validate() error {
	if err := requiredString("system prompt", input.SystemPrompt); err != nil {
		return err
	}
	if len(input.Messages) == 0 {
		return fmt.Errorf("at least one message is required: %w", domain.ErrInvalid)
	}
	for index, message := range input.Messages {
		if message.Role != ToolUseRoleUser && message.Role != ToolUseRoleAssistant {
			return fmt.Errorf("message %d has role %q: %w", index, message.Role, domain.ErrInvalid)
		}
		if len(message.Blocks) == 0 {
			return fmt.Errorf("message %d is empty: %w", index, domain.ErrInvalid)
		}
		for _, block := range message.Blocks {
			switch block.Type {
			case ToolUseText, ToolUseCall, ToolUseResult:
			default:
				return fmt.Errorf("message %d has a block of type %q: %w", index, block.Type, domain.ErrInvalid)
			}
		}
	}
	for _, tool := range input.Tools {
		if err := firstError([]error{requiredString("tool name", tool.Name), validJSON("tool input schema", tool.InputSchema)}); err != nil {
			return err
		}
	}
	if input.MaxOutputTokens <= 0 {
		return fmt.Errorf("max output tokens must be positive: %w", domain.ErrInvalid)
	}
	return nil
}

// ToolUseStop is why the model stopped: it finished (end_turn), it
// wants tools run (tool_use), it ran out of room (max_tokens), or it
// declined (refusal).
type ToolUseStop string

const (
	ToolUseStopEndTurn   ToolUseStop = "end_turn"
	ToolUseStopToolUse   ToolUseStop = "tool_use"
	ToolUseStopMaxTokens ToolUseStop = "max_tokens"
	ToolUseStopRefusal   ToolUseStop = "refusal"
)

type ToolUseOutput struct {
	// Blocks is the assistant's message: text and tool_use blocks.
	Blocks       []ToolUseBlock
	StopReason   ToolUseStop
	InputTokens  int64
	OutputTokens int64
	// ReasoningTokens is the part of OutputTokens the model spent on
	// hidden reasoning, when the provider reports it.
	ReasoningTokens int64
	// ProviderStop is the provider's own stop reason (finish_reason,
	// stop_reason) and ResponseID its id for the response, both for logs
	// and support requests. Adapters fill them even when they also
	// return an error.
	ProviderStop string
	ResponseID   string
}
