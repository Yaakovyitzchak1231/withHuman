package orchestration

import (
	"context"

	"github.com/withHumanAI/withHuman/loggermdc"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
)

// NewMDCInterceptor connects Temporal's context-free logging API to the MDC
// carried by each workflow and activity. The SDK logger retains its own
// execution metadata and replay suppression.
func NewMDCInterceptor() interceptor.WorkerInterceptor { return &mdcInterceptor{} }

type mdcInterceptor struct {
	interceptor.WorkerInterceptorBase
}

func (*mdcInterceptor) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &mdcActivityInbound{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}}
}

func (*mdcInterceptor) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &mdcWorkflowInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

type mdcActivityInbound struct {
	interceptor.ActivityInboundInterceptorBase
}

func (i *mdcActivityInbound) Init(next interceptor.ActivityOutboundInterceptor) error {
	return i.Next.Init(&mdcActivityOutbound{ActivityOutboundInterceptorBase: interceptor.ActivityOutboundInterceptorBase{Next: next}})
}

func (i *mdcActivityInbound) ExecuteActivity(ctx context.Context, input *interceptor.ExecuteActivityInput) (any, error) {
	ctx = loggermdc.With(ctx, loggermdc.Fields{loggermdc.KeyWorkflowID: activity.GetInfo(ctx).WorkflowExecution.ID})
	return i.Next.ExecuteActivity(ctx, input)
}

type mdcActivityOutbound struct {
	interceptor.ActivityOutboundInterceptorBase
}

func (i *mdcActivityOutbound) GetLogger(ctx context.Context) log.Logger {
	return &mdcTemporalLogger{next: i.Next.GetLogger(ctx), fields: loggermdc.From(ctx)}
}

type mdcWorkflowInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}

func (i *mdcWorkflowInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&mdcWorkflowOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}})
}

func (i *mdcWorkflowInbound) ExecuteWorkflow(ctx workflow.Context, input *interceptor.ExecuteWorkflowInput) (any, error) {
	ctx = withWorkflowMDC(ctx, loggermdc.Fields{loggermdc.KeyWorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID})
	return i.Next.ExecuteWorkflow(ctx, input)
}

type mdcWorkflowOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
}

func (i *mdcWorkflowOutbound) GetLogger(ctx workflow.Context) log.Logger {
	return &mdcTemporalLogger{next: i.Next.GetLogger(ctx), fields: workflowMDC(ctx)}
}

type mdcTemporalLogger struct {
	next   log.Logger
	fields loggermdc.Fields
}

func (l *mdcTemporalLogger) Debug(message string, values ...any) {
	l.next.Debug(message, l.values(values)...)
}

func (l *mdcTemporalLogger) Info(message string, values ...any) {
	l.next.Info(message, l.values(values)...)
}

func (l *mdcTemporalLogger) Warn(message string, values ...any) {
	l.next.Warn(message, l.values(values)...)
}

func (l *mdcTemporalLogger) Error(message string, values ...any) {
	l.next.Error(message, l.values(values)...)
}

func (l *mdcTemporalLogger) values(values []any) []any {
	result := loggermdc.KeyValues(context.Background(), l.fields)
	for index := 0; index < len(values); index += 2 {
		key, _ := values[index].(string)
		if _, reserved := l.fields[loggermdc.Key(key)]; reserved && index+1 < len(values) {
			continue
		}
		result = append(result, values[index:min(index+2, len(values))]...)
	}
	return result
}
