package loggermdc

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestConfigureCorrelatesInjectedAndGlobalLogging(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var output bytes.Buffer
	logger := Configure(slog.NewJSONHandler(&output, nil))
	ctx := With(context.Background(), Fields{KeyRequestID: "request-1", KeyOrganizationID: "org-1"})
	logger.InfoContext(ctx, "injected")
	slog.WarnContext(ctx, "global")
	logger.Info("background")

	decoder := json.NewDecoder(&output)
	for _, message := range []string{"injected", "global", "background"} {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != message {
			t.Fatalf("unexpected record: %#v", record)
		}
		if message == "background" {
			if _, leaked := record["request_id"]; leaked {
				t.Fatalf("request context leaked into background logging: %#v", record)
			}
		} else if record["request_id"] != "request-1" || record["organization_id"] != "org-1" {
			t.Fatalf("missing MDC: %#v", record)
		}
	}
}

func TestWrapPreservesConfigurationAndDoesNotDoubleWrap(t *testing.T) {
	var output bytes.Buffer
	logger := Wrap(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn})).With("service", "test"))
	if Wrap(logger) != logger {
		t.Fatal("logger was wrapped twice")
	}
	ctx := With(context.Background(), Fields{KeyTraceID: "trace-1"})
	logger.InfoContext(ctx, "filtered")
	logger.WarnContext(ctx, "visible")
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["trace_id"] != "trace-1" || record["service"] != "test" || record["msg"] != "visible" {
		t.Fatalf("logger configuration or MDC lost: %#v", record)
	}
}
