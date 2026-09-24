package loggermdc

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
)

func TestWithIsImmutableAndLaterValuesOverride(t *testing.T) {
	original := Fields{KeyTraceID: "trace-1", Key("local"): "first"}
	parent := With(context.Background(), original)
	original[KeyTraceID] = "mutated"
	child := With(parent, Fields{KeyTraceID: "trace-2"})

	parentFields := From(parent)
	childFields := From(child)
	childFields[KeyTraceID] = "mutated-copy"
	if parentFields[KeyTraceID] != "trace-1" {
		t.Fatalf("parent was mutated: %#v", parentFields)
	}
	if got := From(child)[KeyTraceID]; got != "trace-2" {
		t.Fatalf("got child trace %q", got)
	}
}

func TestPropagatedDropsCustomKeys(t *testing.T) {
	fields := Propagated(Fields{KeyTraceID: "trace", Key("custom"): "secret"})
	if len(fields) != 1 || fields[KeyTraceID] != "trace" {
		t.Fatalf("unexpected propagated fields: %#v", fields)
	}
}

func TestKeyValuesUsesContextOverFallback(t *testing.T) {
	ctx := With(context.Background(), Fields{KeyTraceID: "context", KeyActorID: "actor-1"})
	values := KeyValues(ctx, Fields{KeyTraceID: "fallback", KeyOrganizationID: "org-1"})
	want := []any{"actor_id", "actor-1", "organization_id", "org-1", "trace_id", "context"}
	if len(values) != len(want) {
		t.Fatalf("got %#v, want %#v", values, want)
	}
	for index := range want {
		if values[index] != want[index] {
			t.Fatalf("got %#v, want %#v", values, want)
		}
	}
}

func TestHandlerAddsMDCWithStableOrderingAndMDCPrecedence(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{ReplaceAttr: removeTime})))
	ctx := With(context.Background(), Fields{KeyTraceID: "authoritative", KeyRequestID: "request-1"})
	logger.InfoContext(ctx, "handled", slog.String("trace_id", "wrong"), slog.String("z", "last"))

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["trace_id"] != "authoritative" || record["request_id"] != "request-1" {
		t.Fatalf("MDC missing or overridden: %#v", record)
	}
	if bytes.Index(output.Bytes(), []byte(`"request_id"`)) > bytes.Index(output.Bytes(), []byte(`"trace_id"`)) {
		t.Fatalf("MDC output is not stable: %s", output.String())
	}
}

func TestHandlerKeepsMDCOutsideMergedGroups(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{ReplaceAttr: removeTime}))).
		WithGroup("component").With("bound", "yes").WithGroup("operation")
	ctx := With(context.Background(), Fields{KeyTraceID: "trace-1"})
	logger.InfoContext(ctx, "grouped", "event", "yes")

	var record struct {
		TraceID   string `json:"trace_id"`
		Component struct {
			Bound     string `json:"bound"`
			Operation struct {
				Event string `json:"event"`
			} `json:"operation"`
		} `json:"component"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.TraceID != "trace-1" || record.Component.Bound != "yes" || record.Component.Operation.Event != "yes" {
		t.Fatalf("unexpected grouped record: %#v (%s)", record, output.String())
	}
}

func TestDerivedContextsAreSafeForConcurrentUse(t *testing.T) {
	parent := With(context.Background(), Fields{KeyTraceID: "parent"})
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			child := With(parent, Fields{KeyTraceID: "child"})
			if From(child)[KeyTraceID] != "child" || From(parent)[KeyTraceID] != "parent" {
				t.Error("derived context mutated its parent")
			}
		}()
	}
	wait.Wait()
}

func removeTime(_ []string, attr slog.Attr) slog.Attr {
	if attr.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return attr
}
