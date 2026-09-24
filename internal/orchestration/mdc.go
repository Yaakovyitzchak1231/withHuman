package orchestration

import (
	"context"

	"github.com/withHumanAI/withHuman/loggermdc"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"
)

const temporalMDCHeader = "withhuman-mdc"

type (
	mdcPropagator      struct{}
	workflowMDCContext struct{}
)

// NewMDCPropagator carries the standard logging correlation fields through
// Temporal workflow and activity headers.
func NewMDCPropagator() workflow.ContextPropagator {
	return mdcPropagator{}
}

func (mdcPropagator) Inject(ctx context.Context, writer workflow.HeaderWriter) error {
	return injectMDC(loggermdc.Propagated(loggermdc.From(ctx)), writer)
}

func (mdcPropagator) Extract(ctx context.Context, reader workflow.HeaderReader) (context.Context, error) {
	fields, err := extractMDC(reader)
	if err != nil || len(fields) == 0 {
		return ctx, err
	}
	return loggermdc.With(ctx, fields), nil
}

func (mdcPropagator) InjectFromWorkflow(ctx workflow.Context, writer workflow.HeaderWriter) error {
	return injectMDC(workflowMDC(ctx), writer)
}

func (mdcPropagator) ExtractToWorkflow(ctx workflow.Context, reader workflow.HeaderReader) (workflow.Context, error) {
	fields, err := extractMDC(reader)
	if err != nil || len(fields) == 0 {
		return ctx, err
	}
	return withWorkflowMDC(ctx, fields), nil
}

func injectMDC(fields loggermdc.Fields, writer workflow.HeaderWriter) error {
	if len(fields) == 0 {
		return nil
	}
	payload, err := converter.GetDefaultDataConverter().ToPayload(fields)
	if err != nil {
		return err
	}
	writer.Set(temporalMDCHeader, payload)
	return nil
}

func extractMDC(reader workflow.HeaderReader) (loggermdc.Fields, error) {
	payload, ok := reader.Get(temporalMDCHeader)
	if !ok {
		return nil, nil
	}
	var fields loggermdc.Fields
	if err := converter.GetDefaultDataConverter().FromPayload(payload, &fields); err != nil {
		return nil, err
	}
	return loggermdc.Propagated(fields), nil
}

func withWorkflowMDC(ctx workflow.Context, fields loggermdc.Fields) workflow.Context {
	merged := workflowMDC(ctx)
	if merged == nil {
		merged = make(loggermdc.Fields)
	}
	for key, value := range loggermdc.Propagated(fields) {
		merged[key] = value
	}
	return replaceWorkflowMDC(ctx, merged)
}

func replaceWorkflowMDC(ctx workflow.Context, fields loggermdc.Fields) workflow.Context {
	propagated := loggermdc.Propagated(fields)
	if propagated == nil {
		propagated = make(loggermdc.Fields)
	}
	return workflow.WithValue(ctx, workflowMDCContext{}, propagated)
}

func workflowMDC(ctx workflow.Context) loggermdc.Fields {
	stored, _ := ctx.Value(workflowMDCContext{}).(loggermdc.Fields)
	if len(stored) == 0 {
		return nil
	}
	cloned := make(loggermdc.Fields, len(stored))
	for key, value := range stored {
		cloned[key] = value
	}
	return cloned
}
