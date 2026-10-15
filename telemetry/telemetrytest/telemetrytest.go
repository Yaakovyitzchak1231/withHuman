// Package telemetrytest reads what a test recorded through package
// telemetry. Install binds the process's instruments to an in-memory
// reader once per test binary, because an instrument made before a
// provider exists binds to the first one installed; counts are therefore
// cumulative across a binary's tests, and a test compares before and
// after.
package telemetrytest

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var (
	once   sync.Once
	reader *sdkmetric.ManualReader
)

// Install makes the process's meter provider an in-memory one. Call it
// before the code under test records anything.
func Install() {
	once.Do(func() {
		reader = sdkmetric.NewManualReader()
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	})
}

// Count is the named metric's total over the points carrying every given
// attribute: a counter's sum, a histogram's observation count, or a
// gauge's value.
func Count(t testing.TB, name string, attributes ...attribute.KeyValue) float64 {
	t.Helper()
	Install()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	total := 0.0
	for _, scope := range collected.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			if recorded.Name != name {
				continue
			}
			switch data := recorded.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range data.DataPoints {
					if matches(point.Attributes, attributes) {
						total += float64(point.Value)
					}
				}
			case metricdata.Sum[float64]:
				for _, point := range data.DataPoints {
					if matches(point.Attributes, attributes) {
						total += point.Value
					}
				}
			case metricdata.Gauge[int64]:
				for _, point := range data.DataPoints {
					if matches(point.Attributes, attributes) {
						total += float64(point.Value)
					}
				}
			case metricdata.Gauge[float64]:
				for _, point := range data.DataPoints {
					if matches(point.Attributes, attributes) {
						total += point.Value
					}
				}
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					if matches(point.Attributes, attributes) {
						total += float64(point.Count)
					}
				}
			}
		}
	}
	return total
}

func matches(set attribute.Set, want []attribute.KeyValue) bool {
	for _, attribute := range want {
		value, ok := set.Value(attribute.Key)
		if !ok || value != attribute.Value {
			return false
		}
	}
	return true
}
