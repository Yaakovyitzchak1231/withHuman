package orchestration

import (
	"context"
	"testing"

	"github.com/withHumanAI/withHuman/loggermdc"
	commonpb "go.temporal.io/api/common/v1"
)

type testHeader struct{ header *commonpb.Header }

func newTestHeader() *testHeader {
	return &testHeader{header: &commonpb.Header{Fields: make(map[string]*commonpb.Payload)}}
}

func (h *testHeader) Set(key string, value *commonpb.Payload) { h.header.Fields[key] = value }
func (h *testHeader) Get(key string) (*commonpb.Payload, bool) {
	value, ok := h.header.Fields[key]
	return value, ok
}
func (h *testHeader) ForEachKey(handler func(string, *commonpb.Payload) error) error {
	for key, value := range h.header.Fields {
		if err := handler(key, value); err != nil {
			return err
		}
	}
	return nil
}

func TestMDCPropagatorRoundTripsAllowedFields(t *testing.T) {
	propagator := NewMDCPropagator()
	header := newTestHeader()
	input := loggermdc.With(context.Background(), loggermdc.Fields{
		loggermdc.KeyTraceID:    "trace-1",
		loggermdc.Key("custom"): "must-not-cross",
	})
	if err := propagator.Inject(input, header); err != nil {
		t.Fatal(err)
	}
	output, err := propagator.Extract(context.Background(), header)
	if err != nil {
		t.Fatal(err)
	}
	fields := loggermdc.From(output)
	if fields[loggermdc.KeyTraceID] != "trace-1" {
		t.Fatalf("trace field was not propagated: %#v", fields)
	}
	if _, ok := fields[loggermdc.Key("custom")]; ok {
		t.Fatalf("custom field crossed Temporal: %#v", fields)
	}
}
