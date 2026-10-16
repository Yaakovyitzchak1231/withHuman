// Package telemetry is withHuman's OpenTelemetry metrics: Start sets up
// the process's meter provider from the standard OTEL_* environment, and
// the record functions in instruments.go are every metric core emits.
// Instruments come from the global provider, so code records the same
// way whether or not the process exports anything; without an exporter
// configured the global provider stays the no-op one.
//
// Attributes are bounded vocabularies only: never an organization, agent,
// member, tool or request identifier. Per-organization usage is the
// entitlements port's job, not a metric's.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// ScopeName is the instrumentation scope core's own instruments use.
const ScopeName = "github.com/withHumanAI/withHuman"

// Options name the process the metrics describe.
type Options struct {
	// ServiceName is service.name unless OTEL_SERVICE_NAME or
	// OTEL_RESOURCE_ATTRIBUTES set one.
	ServiceName string
	// Version is service.version.
	Version string
	Logger  *slog.Logger
}

// Shutdown flushes the last metrics and stops the exporter.
type Shutdown func(context.Context) error

// Start installs the process's meter provider and Go runtime metrics.
// Export is configured by the standard OpenTelemetry variables:
// OTEL_EXPORTER_OTLP_ENDPOINT (and the rest of the OTLP family) sends
// OTLP, OTEL_METRICS_EXPORTER picks another exporter (prometheus serves
// a scrape endpoint on OTEL_EXPORTER_PROMETHEUS_PORT, console writes to
// stdout, none turns metrics off). With neither set, or with
// OTEL_SDK_DISABLED=true, metrics stay off and Start installs nothing.
func Start(ctx context.Context, options Options) (Shutdown, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	off := func(context.Context) error { return nil }
	if !exportConfigured() {
		logger.Info("metrics export off - set OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_METRICS_EXPORTER to export OpenTelemetry metrics")
		return off, nil
	}
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, err
	}
	if autoexport.IsNoneMetricReader(reader) {
		logger.Info("metrics export off - OTEL_METRICS_EXPORTER is none")
		return off, nil
	}
	// Later detectors win, so the environment overrides the defaults.
	described, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(options.ServiceName), semconv.ServiceVersion(options.Version)),
		resource.WithTelemetrySDK(), resource.WithHost(), resource.WithFromEnv())
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		_ = reader.Shutdown(ctx)
		return nil, err
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithResource(described), sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Warn("OpenTelemetry error - "+err.Error(), "error", err)
	}))
	if err := runtime.Start(runtime.WithMeterProvider(provider)); err != nil {
		_ = provider.Shutdown(ctx)
		return nil, err
	}
	logger.Info("metrics export on - "+exporterName(), "exporter", exporterName(), "service_name", options.ServiceName)
	return provider.Shutdown, nil
}

// Meter is a meter from the process's provider, for an edition's own
// instruments. Instruments made before Start are bound to the provider
// Start installs.
func Meter(name string, options ...metric.MeterOption) metric.Meter {
	return otel.Meter(name, options...)
}

func exportConfigured() bool {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return false
	}
	return os.Getenv("OTEL_METRICS_EXPORTER") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") != ""
}

func exporterName() string {
	if name := os.Getenv("OTEL_METRICS_EXPORTER"); name != "" {
		return name
	}
	return "otlp"
}
