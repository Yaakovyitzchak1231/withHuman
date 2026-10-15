package telemetry_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/telemetry"
	"github.com/withHumanAI/withHuman/telemetry/telemetrytest"
	"go.opentelemetry.io/otel/attribute"
)

func TestStartLeavesMetricsOffWithoutAnExporter(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	t.Setenv("OTEL_METRICS_EXPORTER", "")
	shutdown, err := telemetry.Start(context.Background(), telemetry.Options{ServiceName: "withhuman-test", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestStartHonorsNone(t *testing.T) {
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	shutdown, err := telemetry.Start(context.Background(), telemetry.Options{ServiceName: "withhuman-test", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestInstrumentsRecordThroughTheInstalledProvider(t *testing.T) {
	telemetrytest.Install()
	ctx := context.Background()

	created := attribute.String("withhuman.approval.status", "pending")
	before := telemetrytest.Count(t, "withhuman.approval.requests.created", created)
	telemetry.ApprovalCreated(ctx, "pending")
	if got := telemetrytest.Count(t, "withhuman.approval.requests.created", created) - before; got != 1 {
		t.Fatalf("created delta = %v, want 1", got)
	}

	route := attribute.String("http.route", "/api/v1/requests/:id")
	failed := attribute.String("error.type", "500")
	before = telemetrytest.Count(t, "http.server.request.duration", route, failed)
	finish := telemetry.StartHTTPServerRequest(ctx, http.MethodGet, true)
	if got := telemetrytest.Count(t, "http.server.active_requests", attribute.String("url.scheme", "https")); got < 1 {
		t.Fatalf("active requests = %v, want at least 1", got)
	}
	finish("/api/v1/requests/:id", http.StatusInternalServerError)
	if got := telemetrytest.Count(t, "http.server.request.duration", route, failed) - before; got != 1 {
		t.Fatalf("request duration delta = %v, want 1", got)
	}

	unregister, err := telemetry.ObservePendingApprovals(func(context.Context) (int64, time.Time, error) {
		return 3, time.Now().Add(-time.Minute), nil
	})
	if err != nil {
		t.Fatalf("ObservePendingApprovals: %v", err)
	}
	if got := telemetrytest.Count(t, "withhuman.approval.pending.requests"); got != 3 {
		t.Fatalf("pending = %v, want 3", got)
	}
	if got := telemetrytest.Count(t, "withhuman.approval.pending.oldest_age"); got < 60 {
		t.Fatalf("oldest age = %v, want at least 60", got)
	}
	unregister()
	if got := telemetrytest.Count(t, "withhuman.approval.pending.requests"); got != 0 {
		t.Fatalf("pending after unregister = %v, want 0", got)
	}
}
