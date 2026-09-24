// Package loggermdc provides immutable logging fields carried by context.Context.
package loggermdc

import (
	"context"
	"sort"
)

// Key identifies one field in the mapped diagnostic context.
type Key string

const (
	KeyTraceID             Key = "trace_id"
	KeyRequestID           Key = "request_id"
	KeyOrganizationID      Key = "organization_id"
	KeyActorType           Key = "actor_type"
	KeyActorID             Key = "actor_id"
	KeyAgentInstanceID     Key = "agent_instance_id"
	KeyApprovalRequestID   Key = "approval_request_id"
	KeyWorkflowID          Key = "workflow_id"
	KeyDecisionOperationID Key = "decision_operation_id"
)

// Fields is a set of string correlation fields. Callers may define their own
// keys for process-local logging; Propagated retains only the standard keys.
type Fields map[Key]string

type contextKey struct{}

var propagatedKeys = map[Key]struct{}{
	KeyTraceID: {}, KeyRequestID: {}, KeyOrganizationID: {}, KeyActorType: {},
	KeyActorID: {}, KeyAgentInstanceID: {}, KeyApprovalRequestID: {},
	KeyWorkflowID: {}, KeyDecisionOperationID: {},
}

// With returns a derived context containing the existing fields plus fields.
// It never mutates either the parent context's fields or the supplied map.
func With(ctx context.Context, fields Fields) context.Context {
	merged := From(ctx)
	if merged == nil {
		merged = make(Fields, len(fields))
	}
	for key, value := range fields {
		if key != "" {
			merged[key] = value
		}
	}
	return context.WithValue(ctx, contextKey{}, merged)
}

// From returns a defensive copy of the fields in ctx.
func From(ctx context.Context) Fields {
	if ctx == nil {
		return nil
	}
	stored, _ := ctx.Value(contextKey{}).(Fields)
	if len(stored) == 0 {
		return nil
	}
	return clone(stored)
}

// Propagated returns only fields that are safe and useful across process
// boundaries. The returned map is always detached from its input.
func Propagated(fields Fields) Fields {
	propagated := make(Fields)
	for key, value := range fields {
		if _, ok := propagatedKeys[key]; ok {
			propagated[key] = value
		}
	}
	if len(propagated) == 0 {
		return nil
	}
	return propagated
}

// KeyValues returns deterministic key-value arguments suitable for logging
// APIs such as Temporal's logger. Context fields override fallback fields.
func KeyValues(ctx context.Context, fallback Fields) []any {
	fields := clone(fallback)
	if fields == nil {
		fields = make(Fields)
	}
	for key, value := range From(ctx) {
		fields[key] = value
	}
	keys := sortedKeys(fields)
	values := make([]any, 0, len(keys)*2)
	for _, key := range keys {
		values = append(values, string(key), fields[key])
	}
	return values
}

func clone(fields Fields) Fields {
	if len(fields) == 0 {
		return nil
	}
	cloned := make(Fields, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}

func sortedKeys(fields Fields) []Key {
	keys := make([]Key, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
