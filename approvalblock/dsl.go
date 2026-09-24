package approvalblock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

type Option func(*Definition)

func Define(id string, options ...Option) *Definition {
	definition := &Definition{
		ID:          id,
		Type:        TypeCEL,
		Enabled:     true,
		Timeout:     DefaultTimeout.String(),
		MaxAttempts: 1,
		CEL:         &CELConfig{},
	}
	for _, option := range options {
		if option != nil {
			option(definition)
		}
	}
	return definition
}

func Name(name string) Option {
	return func(definition *Definition) { definition.Name = name }
}

func Enabled(enabled bool) Option {
	return func(definition *Definition) { definition.Enabled = enabled }
}

func Timeout(timeout time.Duration) Option {
	return func(definition *Definition) { definition.Timeout = timeout.String() }
}

// MaxAttempts sets how many attempts the block's runtime may make, within
// the type's limits.
func MaxAttempts(attempts int) Option {
	return func(definition *Definition) { definition.MaxAttempts = attempts }
}

func OnMatch(outcome Outcome) Option {
	return func(definition *Definition) {
		if definition.CEL != nil {
			definition.CEL.OnMatch = outcome
		}
	}
}

// When sets the condition of a cel block or a branch.
func When(condition Condition) Option {
	return func(definition *Definition) {
		switch {
		case definition.CEL != nil:
			definition.CEL.When = condition
		case definition.Branch != nil:
			definition.Branch.When = condition
		}
	}
}

// Reason sets the static reason recorded on the block's decisions, for
// whichever configuration variant the definition carries.
func Reason(reason string) Option {
	return func(definition *Definition) {
		switch {
		case definition.Always != nil:
			definition.Always.Reason = reason
		case definition.CEL != nil:
			definition.CEL.Reason = reason
		case definition.Webhook != nil:
			definition.Webhook.Reason = reason
		case definition.Branch != nil:
			definition.Branch.Reason = reason
		default:
			if reasoned, ok := definition.Config.(ReasonedConfig); ok {
				reasoned.SetReason(reason)
			}
		}
	}
}

type FieldBuilder struct {
	path string
}

func Field(path string) FieldBuilder {
	return FieldBuilder{path: path}
}

func (builder FieldBuilder) Exists() Condition          { return builder.unary(Exists) }
func (builder FieldBuilder) NotExists() Condition       { return builder.unary(NotExists) }
func (builder FieldBuilder) IsNull() Condition          { return builder.unary(IsNull) }
func (builder FieldBuilder) IsNotNull() Condition       { return builder.unary(IsNotNull) }
func (builder FieldBuilder) IsTrue() Condition          { return builder.unary(IsTrue) }
func (builder FieldBuilder) IsFalse() Condition         { return builder.unary(IsFalse) }
func (builder FieldBuilder) IsEmpty() Condition         { return builder.unary(IsEmpty) }
func (builder FieldBuilder) IsNotEmpty() Condition      { return builder.unary(IsNotEmpty) }
func (builder FieldBuilder) Equals(value any) Condition { return builder.binary(Equals, value) }
func (builder FieldBuilder) NotEquals(value any) Condition {
	return builder.binary(NotEquals, value)
}
func (builder FieldBuilder) In(values ...string) Condition {
	return builder.binary(In, values)
}
func (builder FieldBuilder) NotIn(values ...string) Condition {
	return builder.binary(NotIn, values)
}
func (builder FieldBuilder) Contains(value any) Condition {
	return builder.binary(Contains, value)
}
func (builder FieldBuilder) NotContains(value any) Condition {
	return builder.binary(NotContains, value)
}
func (builder FieldBuilder) StartsWith(prefixes ...string) Condition {
	return builder.binary(StartsWith, prefixes)
}
func (builder FieldBuilder) EndsWith(value string) Condition {
	return builder.binary(EndsWith, value)
}
func (builder FieldBuilder) Regex(value string) Condition {
	return builder.binary(Regex, value)
}
func (builder FieldBuilder) GreaterThan(value any) Condition {
	return builder.binary(GreaterThan, value)
}
func (builder FieldBuilder) GreaterThanOrEqual(value any) Condition {
	return builder.binary(GreaterThanOrEqual, value)
}
func (builder FieldBuilder) LessThan(value any) Condition {
	return builder.binary(LessThan, value)
}
func (builder FieldBuilder) LessThanOrEqual(value any) Condition {
	return builder.binary(LessThanOrEqual, value)
}
func (builder FieldBuilder) ContainsAny(values ...any) Condition {
	return builder.binary(ContainsAny, values)
}
func (builder FieldBuilder) ContainsAll(values ...any) Condition {
	return builder.binary(ContainsAll, values)
}

func (builder FieldBuilder) unary(operator Operator) Condition {
	return Condition{ID: uuid.NewString(), Field: builder.path, Operator: operator}
}

func (builder FieldBuilder) binary(operator Operator, value any) Condition {
	raw, err := json.Marshal(value)
	return Condition{ID: uuid.NewString(), Field: builder.path, Operator: operator, Value: raw, buildErr: err}
}

func All(conditions ...Condition) Condition {
	return Condition{ID: uuid.NewString(), All: conditions}
}

func Any(conditions ...Condition) Condition {
	return Condition{ID: uuid.NewString(), Any: conditions}
}

func Some(field string, where Condition) Condition {
	return Condition{ID: uuid.NewString(), Some: &Quantifier{Field: field, Where: where}}
}

func Every(field string, where Condition) Condition {
	return Condition{ID: uuid.NewString(), Every: &Quantifier{Field: field, Where: where}}
}

// ConditionID replaces the generated node ID. It is useful when updating a
// document while preserving the stable IDs used by audit explanations.
func ConditionID(id string, condition Condition) Condition {
	condition.ID = id
	return condition
}

// JSON validates and serializes the canonical block document.
func (definition Definition) JSON() ([]byte, error) {
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(definition)
}

// Parse decodes one strict canonical block document and validates it. Unknown
// fields and trailing JSON values are rejected.
func Parse(document []byte) (*Definition, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var definition Definition
	if err := decoder.Decode(&definition); err != nil {
		return nil, fmt.Errorf("decode approval block: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode approval block: document must contain one JSON value")
	}
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	return &definition, nil
}
