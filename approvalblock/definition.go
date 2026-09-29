// Package approvalblock provides a serializable Go authoring DSL for
// request approval blocks.
package approvalblock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultTimeout = 50 * time.Millisecond
	MaximumTimeout = time.Second

	MaximumConditionDepth       = 12
	MaximumConditionNodes       = 128
	MaximumBlockDocumentBytes   = 64 * 1024
	MaximumExpressionBytes      = 4 * 1024
	MaximumRegexBytes           = 512
	MaximumRequestInputBytes    = 64 * 1024
	MaximumComprehensionEntries = 1_000
	MaximumEvaluationCost       = 10_000
	// MaximumCELBlocksPerRequest bounds the condition evaluations one run
	// segment performs: cel blocks and branch conditions share it.
	MaximumCELBlocksPerRequest = 1_000
)

type BlockType string
type Outcome string
type Operator string

const (
	TypeCEL BlockType = "cel"
)

// MaximumPipelineBlocks bounds one pipeline document regardless of type,
// counting every block inside a branch.
const MaximumPipelineBlocks = 1_000

const (
	Next    Outcome = "next"
	Approve Outcome = "approve"
	Deny    Outcome = "deny"
	Human   Outcome = "human"
)

const (
	Exists             Operator = "exists"
	NotExists          Operator = "not_exists"
	Equals             Operator = "equals"
	NotEquals          Operator = "not_equals"
	IsNull             Operator = "is_null"
	IsNotNull          Operator = "is_not_null"
	In                 Operator = "in"
	NotIn              Operator = "not_in"
	Contains           Operator = "contains"
	NotContains        Operator = "not_contains"
	StartsWith         Operator = "starts_with"
	EndsWith           Operator = "ends_with"
	Regex              Operator = "regex"
	GreaterThan        Operator = "greater_than"
	GreaterThanOrEqual Operator = "greater_than_or_equal"
	LessThan           Operator = "less_than"
	LessThanOrEqual    Operator = "less_than_or_equal"
	IsTrue             Operator = "is_true"
	IsFalse            Operator = "is_false"
	ContainsAny        Operator = "contains_any"
	ContainsAll        Operator = "contains_all"
	IsEmpty            Operator = "is_empty"
	IsNotEmpty         Operator = "is_not_empty"
)

// Definition is the canonical block document produced by the Go DSL and
// accepted by the internal compiler. Type selects the configuration variant;
// exactly one configuration matching Type is set: one of core's pointers,
// or Config for a kind an edition registered. The wire form carries the
// variant under a single "config" key.
type Definition struct {
	ID          string
	Name        string
	Type        BlockType
	Enabled     bool
	Timeout     string
	MaxAttempts int
	Always      *AlwaysConfig
	CEL         *CELConfig
	Webhook     *WebhookConfig
	Branch      *BranchConfig
	// Config is the configuration of a registered kind (RegisterKind).
	Config Config
}

type CELConfig struct {
	When    Condition `json:"when"`
	OnMatch Outcome   `json:"on_match"`
	Reason  string    `json:"reason"`
	// EscalationPath is the key of the escalation path a human outcome
	// hands the request to. Empty inherits the pipeline default.
	EscalationPath string `json:"escalation_path,omitempty"`
	// AskAgentOwner sends a human outcome to the agent's owner, with
	// EscalationPath as the fallback for a request without one.
	AskAgentOwner bool `json:"ask_agent_owner,omitempty"`
}

// TypeLimits are the envelope limits one block type accepts.
type TypeLimits struct {
	DefaultTimeout  time.Duration
	MaximumTimeout  time.Duration
	MinimumAttempts int
	MaximumAttempts int
}

// typeLimits are core's own kinds; RegisterKind adds an edition's.
var typeLimits = map[BlockType]TypeLimits{
	TypeAlways:  {DefaultTimeout: DefaultTimeout, MaximumTimeout: MaximumTimeout, MinimumAttempts: 1, MaximumAttempts: 1},
	TypeCEL:     {DefaultTimeout: DefaultTimeout, MaximumTimeout: MaximumTimeout, MinimumAttempts: 1, MaximumAttempts: 1},
	TypeWebhook: {DefaultTimeout: DefaultWebhookTimeout, MaximumTimeout: MaximumWebhookTimeout, MinimumAttempts: 1, MaximumAttempts: MaximumWebhookAttempts},
	TypeBranch:  {DefaultTimeout: DefaultTimeout, MaximumTimeout: MaximumTimeout, MinimumAttempts: 1, MaximumAttempts: 1},
}

// LimitsFor returns the envelope limits for a known block type, core's
// or a registered kind's.
func LimitsFor(blockType BlockType) (TypeLimits, bool) {
	if limits, known := typeLimits[blockType]; known {
		return limits, true
	}
	if kind, known := registeredKind(blockType); known {
		return kind.Limits, true
	}
	return TypeLimits{}, false
}

// KnownType reports whether blockType has a configuration variant.
func KnownType(blockType BlockType) bool {
	_, known := LimitsFor(blockType)
	return known
}

// definitionEnvelope is a block document without its configuration, the
// part decoded strictly at each level of a nested document.
type definitionEnvelope struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        BlockType `json:"type"`
	Enabled     bool      `json:"enabled"`
	Timeout     string    `json:"timeout"`
	MaxAttempts int       `json:"max_attempts"`
}

// definitionWire is the wire form of a definition. The configuration is a
// typed value rather than pre-encoded bytes, so one encoder pass writes a
// whole tree of branches instead of re-reading each nested level's output.
type definitionWire struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        BlockType `json:"type"`
	Enabled     bool      `json:"enabled"`
	Timeout     string    `json:"timeout"`
	MaxAttempts int       `json:"max_attempts"`
	Config      any       `json:"config"`
}

func (definition Definition) MarshalJSON() ([]byte, error) {
	encoded, err := definition.wire()
	if err != nil {
		return nil, err
	}
	return json.Marshal(encoded)
}

func (definition Definition) wire() (definitionWire, error) {
	config, err := definition.configWire()
	if err != nil {
		return definitionWire{}, err
	}
	return definitionWire{
		ID: definition.ID, Name: definition.Name, Type: definition.Type, Enabled: definition.Enabled,
		Timeout: definition.Timeout, MaxAttempts: definition.MaxAttempts, Config: config,
	}, nil
}

// UnmarshalJSON decodes the envelope and the configuration variant strictly:
// unknown fields at every level are rejected regardless of the outer
// decoder's settings, because a custom unmarshaler does not inherit them.
// The document is parsed once; a branch's blocks are converted from the
// parsed values, so decoding stays linear however deeply branches nest.
func (definition *Definition) UnmarshalJSON(data []byte) error {
	value, err := decodeValue(data)
	if err != nil {
		return err
	}
	decoded, err := definitionFromValue(value)
	if err != nil {
		return err
	}
	*definition = decoded
	return nil
}

// definitionFromValue converts one parsed block document. Small parts (the
// envelope, a leaf configuration) are re-encoded and decoded strictly; a
// branch configuration recurses into its parsed blocks.
func definitionFromValue(value any) (Definition, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return Definition{}, errors.New("block must be a JSON object")
	}
	config, hasConfig, err := takeMember(object, "config")
	if err != nil {
		return Definition{}, err
	}
	var envelope definitionEnvelope
	if err := decodeStrictValue(object, &envelope); err != nil {
		return Definition{}, err
	}
	definition := Definition{
		ID: envelope.ID, Name: envelope.Name, Type: envelope.Type, Enabled: envelope.Enabled,
		Timeout: envelope.Timeout, MaxAttempts: envelope.MaxAttempts,
	}
	if !hasConfig {
		return definition, nil
	}
	if definition.Type == TypeBranch {
		branch, err := branchConfigFromValue(config)
		if err != nil {
			return Definition{}, fmt.Errorf("decode branch block config: %w", err)
		}
		definition.Branch = branch
		return definition, nil
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return Definition{}, err
	}
	if err := definition.SetConfigJSON(raw); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

// ConfigJSON encodes the configuration variant selected by Type.
func (definition Definition) ConfigJSON() (json.RawMessage, error) {
	config, err := definition.configWire()
	if err != nil {
		return nil, err
	}
	return json.Marshal(config)
}

// configWire returns the configuration variant selected by Type as a value
// the encoder writes directly.
func (definition Definition) configWire() (any, error) {
	switch definition.Type {
	case TypeAlways:
		if definition.Always == nil {
			return nil, errors.New("always block requires an always configuration")
		}
		return definition.Always, nil
	case TypeCEL:
		if definition.CEL == nil {
			return nil, errors.New("cel block requires a cel configuration")
		}
		return definition.CEL, nil
	case TypeWebhook:
		if definition.Webhook == nil {
			return nil, errors.New("webhook block requires a webhook configuration")
		}
		return definition.Webhook, nil
	case TypeBranch:
		if definition.Branch == nil {
			return nil, errors.New("branch block requires a branch configuration")
		}
		return definition.Branch.wire()
	default:
		if _, known := registeredKind(definition.Type); !known {
			return nil, fmt.Errorf("unknown block type %q", definition.Type)
		}
		if definition.Config == nil {
			return nil, fmt.Errorf("%s block requires a %s configuration", definition.Type, definition.Type)
		}
		return definition.Config, nil
	}
}

// SetConfigJSON decodes config as the variant selected by Type.
func (definition *Definition) SetConfigJSON(config json.RawMessage) error {
	switch definition.Type {
	case TypeBranch:
		value, err := decodeValue(config)
		if err != nil {
			return fmt.Errorf("decode branch block config: %w", err)
		}
		decoded, err := branchConfigFromValue(value)
		if err != nil {
			return fmt.Errorf("decode branch block config: %w", err)
		}
		definition.Branch = decoded
		return nil
	case TypeAlways:
		var decoded AlwaysConfig
		if err := decodeStrict(config, &decoded); err != nil {
			return fmt.Errorf("decode always block config: %w", err)
		}
		definition.Always = &decoded
		return nil
	case TypeCEL:
		var decoded CELConfig
		if err := decodeStrict(config, &decoded); err != nil {
			return fmt.Errorf("decode cel block config: %w", err)
		}
		definition.CEL = &decoded
		return nil
	case TypeWebhook:
		var decoded WebhookConfig
		if err := decodeStrict(config, &decoded); err != nil {
			return fmt.Errorf("decode webhook block config: %w", err)
		}
		definition.Webhook = &decoded
		return nil
	default:
		kind, known := registeredKind(definition.Type)
		if !known {
			return fmt.Errorf("unknown block type %q", definition.Type)
		}
		decoded := kind.New()
		if err := decodeStrict(config, decoded); err != nil {
			return fmt.Errorf("decode %s block config: %w", definition.Type, err)
		}
		definition.Config = decoded
		return nil
	}
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("document must contain one JSON value")
	}
	return nil
}

// decodeValue parses one JSON document into generic values. Numbers stay
// json.Number so re-encoding a value reproduces it exactly.
func decodeValue(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("document must contain one JSON value")
	}
	return value, nil
}

// decodeStrictValue decodes a parsed value into destination with unknown
// fields rejected.
func decodeStrictValue(value any, destination any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return decodeStrict(raw, destination)
}

// takeMember removes the member named name from a parsed object and returns
// its value. encoding/json matches struct fields case-insensitively, so every
// case variant is the member; more than one is refused as ambiguous.
func takeMember(object map[string]any, name string) (any, bool, error) {
	found := ""
	var value any
	for key, candidate := range object {
		if !strings.EqualFold(key, name) {
			continue
		}
		if found != "" {
			return nil, false, fmt.Errorf("member %q is duplicated", name)
		}
		found, value = key, candidate
	}
	if found == "" {
		return nil, false, nil
	}
	delete(object, found)
	return value, true, nil
}

// Condition has exactly one shape: leaf, all, any, some, or every. Value is
// raw JSON so an omitted value remains distinguishable from a configured null.
type Condition struct {
	ID       string          `json:"id"`
	Field    string          `json:"field,omitempty"`
	Operator Operator        `json:"operator,omitempty"`
	Value    json.RawMessage `json:"value,omitempty"`
	All      []Condition     `json:"all,omitempty"`
	Any      []Condition     `json:"any,omitempty"`
	Some     *Quantifier     `json:"some,omitempty"`
	Every    *Quantifier     `json:"every,omitempty"`

	buildErr error
}

type Quantifier struct {
	Field string    `json:"field"`
	Where Condition `json:"where"`
}

var blockIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// Validate checks the block and, for a branch, every block inside it.
func (definition Definition) Validate() error {
	if err := definition.ValidateBlock(); err != nil {
		return err
	}
	if definition.Type != TypeBranch || definition.Branch == nil {
		return nil
	}
	for _, child := range definition.Branch.Blocks {
		if err := child.Validate(); err != nil {
			return fmt.Errorf("block %q: %w", child.ID, err)
		}
	}
	return nil
}

// ValidateBlock checks one block's own envelope and configuration. A
// branch's blocks are not visited, and its document is measured without
// them, so compiling each block of a tree once costs no more than the tree.
func (definition Definition) ValidateBlock() error {
	if !blockIDPattern.MatchString(definition.ID) {
		return errors.New("block id must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	if strings.TrimSpace(definition.Name) == "" {
		return errors.New("block name is required")
	}
	limits, known := LimitsFor(definition.Type)
	if !known {
		return fmt.Errorf("unknown block type %q", definition.Type)
	}
	if definition.MaxAttempts < limits.MinimumAttempts || definition.MaxAttempts > limits.MaximumAttempts {
		if limits.MinimumAttempts == limits.MaximumAttempts {
			return fmt.Errorf("%s blocks require exactly %d attempt(s)", definition.Type, limits.MinimumAttempts)
		}
		return fmt.Errorf("%s blocks allow between %d and %d attempts", definition.Type, limits.MinimumAttempts, limits.MaximumAttempts)
	}
	timeout, err := time.ParseDuration(definition.Timeout)
	if err != nil || timeout <= 0 || timeout > limits.MaximumTimeout {
		return fmt.Errorf("block timeout must be greater than zero and at most %s", limits.MaximumTimeout)
	}
	if err := definition.validateConfig(); err != nil {
		return err
	}
	own := definition
	if definition.Branch != nil {
		branch := *definition.Branch
		branch.Blocks = nil
		own.Branch = &branch
	}
	document, err := json.Marshal(own)
	if err != nil {
		return fmt.Errorf("block document must be serializable: %w", err)
	}
	if len(document) > MaximumBlockDocumentBytes {
		return fmt.Errorf("block document exceeds %d bytes", MaximumBlockDocumentBytes)
	}
	return nil
}

func (definition Definition) validateConfig() error {
	switch definition.Type {
	case TypeAlways:
		if definition.Always == nil {
			return errors.New("always block requires an always configuration")
		}
		return definition.Always.Validate()
	case TypeCEL:
		if definition.CEL == nil {
			return errors.New("cel block requires a cel configuration")
		}
		return definition.CEL.Validate()
	case TypeWebhook:
		if definition.Webhook == nil {
			return errors.New("webhook block requires a webhook configuration")
		}
		return definition.Webhook.Validate()
	case TypeBranch:
		if definition.Branch == nil {
			return errors.New("branch block requires a branch configuration")
		}
		return definition.Branch.Validate()
	default:
		if _, known := registeredKind(definition.Type); !known {
			return fmt.Errorf("unknown block type %q", definition.Type)
		}
		if definition.Config == nil {
			return fmt.Errorf("%s block requires a %s configuration", definition.Type, definition.Type)
		}
		return definition.Config.Validate()
	}
}

// Validate checks the CEL configuration's outcome, reason, and condition
// tree against the block limits.
func (config CELConfig) Validate() error {
	if config.OnMatch != Approve && config.OnMatch != Deny && config.OnMatch != Human {
		return errors.New("on_match must be approve, deny, or human")
	}
	if strings.TrimSpace(config.Reason) == "" {
		return errors.New("block reason is required")
	}
	if config.EscalationPath != "" && config.OnMatch != Human {
		return errors.New("escalation_path requires on_match to be human")
	}
	if err := ValidateRoute(config.EscalationPath, config.AskAgentOwner); err != nil {
		return err
	}
	state := validationState{ids: make(map[string]struct{})}
	return state.validateCondition(config.When, 1, false)
}

// ValidateCondition validates one standalone condition tree against the
// same shape, depth, node, and operator rules a block's when clause uses.
// Routing rules embed the condition envelope outside a block document.
func ValidateCondition(condition Condition) error {
	state := validationState{ids: make(map[string]struct{})}
	return state.validateCondition(condition, 1, false)
}

type validationState struct {
	ids   map[string]struct{}
	nodes int
}

func (state *validationState) validateCondition(condition Condition, depth int, relative bool) error {
	if condition.buildErr != nil {
		return condition.buildErr
	}
	if depth > MaximumConditionDepth {
		return fmt.Errorf("condition depth exceeds %d", MaximumConditionDepth)
	}
	state.nodes++
	if state.nodes > MaximumConditionNodes {
		return fmt.Errorf("condition count exceeds %d", MaximumConditionNodes)
	}
	if _, err := uuid.Parse(condition.ID); err != nil {
		return fmt.Errorf("condition id %q must be a UUID", condition.ID)
	}
	if _, exists := state.ids[condition.ID]; exists {
		return fmt.Errorf("condition id %q is duplicated", condition.ID)
	}
	state.ids[condition.ID] = struct{}{}

	isLeaf := condition.Operator != "" || condition.Field != "" || condition.Value != nil
	shapes := 0
	if isLeaf {
		shapes++
	}
	if condition.All != nil {
		shapes++
	}
	if condition.Any != nil {
		shapes++
	}
	if condition.Some != nil {
		shapes++
	}
	if condition.Every != nil {
		shapes++
	}
	if shapes != 1 {
		return fmt.Errorf("condition %q must have exactly one shape", condition.ID)
	}

	switch {
	case isLeaf:
		return validateLeaf(condition, relative)
	case condition.All != nil:
		return state.validateChildren(condition.ID, condition.All, depth, relative)
	case condition.Any != nil:
		return state.validateChildren(condition.ID, condition.Any, depth, relative)
	case condition.Some != nil:
		return state.validateQuantifier(condition.ID, *condition.Some, depth, relative)
	default:
		return state.validateQuantifier(condition.ID, *condition.Every, depth, relative)
	}
}

func (state *validationState) validateChildren(id string, children []Condition, depth int, relative bool) error {
	if len(children) == 0 {
		return fmt.Errorf("condition group %q must contain at least one child", id)
	}
	for _, child := range children {
		if err := state.validateCondition(child, depth+1, relative); err != nil {
			return err
		}
	}
	return nil
}

func (state *validationState) validateQuantifier(id string, quantifier Quantifier, depth int, relative bool) error {
	fieldType, err := validateField(quantifier.Field, relative)
	if err != nil {
		return fmt.Errorf("condition %q: %w", id, err)
	}
	if fieldType == fieldString {
		return fmt.Errorf("condition %q: quantifier field must be an array", id)
	}
	return state.validateCondition(quantifier.Where, depth+1, true)
}

func validateLeaf(condition Condition, relative bool) error {
	if condition.Operator == "" {
		return fmt.Errorf("condition %q: operator is required", condition.ID)
	}
	fieldType, err := validateField(condition.Field, relative)
	if err != nil {
		return fmt.Errorf("condition %q: %w", condition.ID, err)
	}
	if err := validateOperator(condition.Operator, condition.Value, fieldType); err != nil {
		return fmt.Errorf("condition %q: %w", condition.ID, err)
	}
	return nil
}

type knownFieldType int

const (
	fieldDynamic knownFieldType = iota
	fieldString
)

func validateField(path string, relative bool) (knownFieldType, error) {
	tokens, err := parseJSONPointer(path)
	if err != nil {
		return fieldDynamic, err
	}
	if relative {
		return fieldDynamic, nil
	}
	// The server is the MCP server that defines the tool; a built-in
	// tool has none, so only not_exists matches it.
	if len(tokens) == 2 && tokens[0] == "request" && (tokens[1] == "tool" || tokens[1] == "server") {
		return fieldString, nil
	}
	if len(tokens) >= 2 && tokens[0] == "request" && tokens[1] == "arguments" {
		return fieldDynamic, nil
	}
	if len(tokens) == 2 && tokens[0] == "agent" && (tokens[1] == "slug" || tokens[1] == "name") {
		return fieldString, nil
	}
	// Metadata an earlier block returned, nested by pipeline scope and
	// block key: /previous_metadata/organization/fraud-check/risk_score.
	if len(tokens) >= 3 && tokens[0] == "previous_metadata" && (tokens[1] == "organization" || tokens[1] == "agent") {
		return fieldDynamic, nil
	}
	return fieldDynamic, fmt.Errorf("field %q is not available to CEL blocks", path)
}

func parseJSONPointer(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("field %q must be a JSON Pointer", path)
	}
	parts := strings.Split(path[1:], "/")
	for index, part := range parts {
		var decoded strings.Builder
		for offset := 0; offset < len(part); offset++ {
			if part[offset] != '~' {
				decoded.WriteByte(part[offset])
				continue
			}
			if offset+1 >= len(part) || (part[offset+1] != '0' && part[offset+1] != '1') {
				return nil, fmt.Errorf("field %q contains an invalid JSON Pointer escape", path)
			}
			offset++
			if part[offset] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[index] = decoded.String()
	}
	return parts, nil
}

func validateOperator(operator Operator, raw json.RawMessage, fieldType knownFieldType) error {
	requiresValue := true
	switch operator {
	case Exists, NotExists, IsNull, IsNotNull, IsTrue, IsFalse, IsEmpty, IsNotEmpty:
		requiresValue = false
	case Equals, NotEquals, In, NotIn, Contains, NotContains, StartsWith, EndsWith, Regex,
		GreaterThan, GreaterThanOrEqual, LessThan, LessThanOrEqual, ContainsAny, ContainsAll:
	default:
		return fmt.Errorf("operator %q is not supported", operator)
	}
	if requiresValue && raw == nil {
		return fmt.Errorf("operator %q requires a value", operator)
	}
	if !requiresValue && raw != nil {
		return fmt.Errorf("operator %q does not accept a value", operator)
	}

	var value any
	if raw != nil {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("operator value must be valid JSON: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return errors.New("operator value must contain one JSON value")
		}
	}

	if fieldType == fieldString && !operatorSupportsStringField(operator, value) {
		return fmt.Errorf("operator %q is incompatible with a known string field", operator)
	}

	switch operator {
	case In, NotIn, StartsWith:
		if !isStringList(value) {
			return fmt.Errorf("operator %q requires an array of strings", operator)
		}
	case EndsWith, Regex:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("operator %q requires a string value", operator)
		}
		if operator == Regex {
			if len(text) > MaximumRegexBytes {
				return fmt.Errorf("regex exceeds %d bytes", MaximumRegexBytes)
			}
			if _, err := regexp.Compile(text); err != nil {
				return fmt.Errorf("regex is invalid: %w", err)
			}
		}
	case GreaterThan, GreaterThanOrEqual, LessThan, LessThanOrEqual:
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("operator %q requires a JSON number", operator)
		}
	case ContainsAny, ContainsAll:
		values, ok := value.([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("operator %q requires a non-empty array", operator)
		}
	}
	return nil
}

func operatorSupportsStringField(operator Operator, value any) bool {
	switch operator {
	case Exists, NotExists, Equals, NotEquals, IsNull, IsNotNull, In, NotIn,
		Contains, NotContains, StartsWith, EndsWith, Regex:
		if (operator == Equals || operator == NotEquals || operator == Contains || operator == NotContains) && value != nil {
			_, ok := value.(string)
			return ok
		}
		return true
	case ContainsAny, ContainsAll:
		return isStringList(value)
	default:
		return false
	}
}

func isStringList(value any) bool {
	values, ok := value.([]any)
	if !ok || len(values) == 0 {
		return false
	}
	for _, value := range values {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}
