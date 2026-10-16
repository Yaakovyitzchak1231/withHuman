package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/httpconv"
	"go.temporal.io/sdk/client"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
)

// The attributes core's instruments carry beside the semantic
// conventions' own. Each is a closed vocabulary.
const (
	// AttributeApprovalStatus is a request's status: pending, approved,
	// denied, expired or cancelled.
	AttributeApprovalStatus = attribute.Key("withhuman.approval.status")
	// AttributeDecisionSource is who made the terminal decision: pipeline,
	// human, system (the deadline) or agent (the requester withdrew).
	AttributeDecisionSource = attribute.Key("withhuman.decision.source")
	// AttributePipelineStage is request (the server's synchronous run) or
	// resume (the worker finishing a suspended one).
	AttributePipelineStage = attribute.Key("withhuman.pipeline.stage")
	// AttributePipelineOutcome is how a run left the request: approved,
	// denied, human (waiting for a person) or suspended (before an
	// asynchronous block).
	AttributePipelineOutcome = attribute.Key("withhuman.pipeline.outcome")
	// AttributeBlockType is a pipeline block's registered type.
	AttributeBlockType = attribute.Key("withhuman.block.type")
	// AttributeBlockOutcome is what a block answered: approve, deny,
	// human, next or enter.
	AttributeBlockOutcome = attribute.Key("withhuman.block.outcome")
	// AttributeWebhookPurpose says why a signed webhook was sent.
	AttributeWebhookPurpose = attribute.Key("withhuman.webhook.purpose")
	// AttributeNotificationChannel is a registered channel's key.
	AttributeNotificationChannel = attribute.Key("withhuman.notification.channel")
	// AttributeNotificationMessage is prompt, digest or transactional.
	AttributeNotificationMessage = attribute.Key("withhuman.notification.message")
	// AttributeContactKind is a contact method's kind.
	AttributeContactKind = attribute.Key("withhuman.contact.kind")
	// AttributeEscalationEffect is what one escalation tick did:
	// level_entered, urgency_raised, deferred, repeat or exhausted.
	AttributeEscalationEffect = attribute.Key("withhuman.escalation.effect")
)

// Webhook purposes.
const (
	WebhookPipelineBlock    = "pipeline_block"
	WebhookEndpointTest     = "endpoint_test"
	WebhookAAPEvent         = "aap_event"
	WebhookAAPVerification  = "aap_verification"
	errorTypeTimeout        = "timeout"
	errorTypeDeliveryFailed = "delivery_failed"
)

// Notification message kinds.
const (
	NotificationPrompt        = "prompt"
	NotificationDigest        = "digest"
	NotificationTransactional = "transactional"
)

var (
	meter = Meter(ScopeName)

	// Second-scale boundaries for work a person can wait on.
	fastBoundaries = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
	// Boundaries for a human decision: seconds to a day.
	decisionBoundaries = []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600, 7200, 14400, 28800, 86400}

	httpServerDuration, _ = httpconv.NewServerRequestDuration(meter, metric.WithExplicitBucketBoundaries(fastBoundaries...))
	httpServerActive, _   = httpconv.NewServerActiveRequests(meter)

	approvalsCreated, _ = meter.Int64Counter("withhuman.approval.requests.created",
		metric.WithUnit("{request}"), metric.WithDescription("Approval requests created, by the status they started in."))
	approvalsResolved, _ = meter.Int64Counter("withhuman.approval.requests.resolved",
		metric.WithUnit("{request}"), metric.WithDescription("Approval requests that reached a terminal status."))
	approvalDecisionDuration, _ = meter.Float64Histogram("withhuman.approval.decision.duration",
		metric.WithUnit("s"), metric.WithDescription("Time from a request's creation to its terminal decision."),
		metric.WithExplicitBucketBoundaries(decisionBoundaries...))
	approvalsPending, _ = meter.Int64ObservableGauge("withhuman.approval.pending.requests",
		metric.WithUnit("{request}"), metric.WithDescription("Approval requests waiting for a decision."))
	approvalsPendingOldestAge, _ = meter.Float64ObservableGauge("withhuman.approval.pending.oldest_age",
		metric.WithUnit("s"), metric.WithDescription("How long the oldest pending approval request has waited."))

	pipelineEvaluationDuration, _ = meter.Float64Histogram("withhuman.pipeline.evaluation.duration",
		metric.WithUnit("s"), metric.WithDescription("Time to run an approval pipeline over one request."),
		metric.WithExplicitBucketBoundaries(fastBoundaries...))
	pipelineBlockDuration, _ = meter.Float64Histogram("withhuman.pipeline.block.duration",
		metric.WithUnit("s"), metric.WithDescription("Time to evaluate one pipeline block."),
		metric.WithExplicitBucketBoundaries(fastBoundaries...))

	webhookDeliveryDuration, _ = meter.Float64Histogram("withhuman.webhook.delivery.duration",
		metric.WithUnit("s"), metric.WithDescription("Time to deliver one signed webhook, from connect to the full response."),
		metric.WithExplicitBucketBoundaries(fastBoundaries...))

	notificationDispatchDuration, _ = meter.Float64Histogram("withhuman.notification.dispatch.duration",
		metric.WithUnit("s"), metric.WithDescription("Time for one notification channel to handle one message."),
		metric.WithExplicitBucketBoundaries(fastBoundaries...))
	notificationHolds, _ = meter.Int64Counter("withhuman.notification.holds",
		metric.WithUnit("{hold}"), metric.WithDescription("Approval prompts held for a digest by notification throttling."))
	escalationEffects, _ = meter.Int64Counter("withhuman.escalation.effects",
		metric.WithUnit("{effect}"), metric.WithDescription("Escalation ticks applied to pending requests, by effect."))

	dbConnections, _ = meter.Int64ObservableUpDownCounter("db.client.connection.count",
		metric.WithUnit("{connection}"), metric.WithDescription("Connections in the pool, by state."))
	dbConnectionsMax, _ = meter.Int64ObservableUpDownCounter("db.client.connection.max",
		metric.WithUnit("{connection}"), metric.WithDescription("The most connections the pool opens."))
	dbConnectionTimeouts, _ = meter.Int64ObservableCounter("db.client.connection.timeouts",
		metric.WithUnit("{timeout}"), metric.WithDescription("Connection acquisitions canceled before a connection was free."))
	dbAcquireWaits, _ = meter.Int64ObservableCounter("withhuman.db.pool.acquire.waits",
		metric.WithUnit("{acquire}"), metric.WithDescription("Connection acquisitions that waited for a free connection."))
	dbAcquireWaitTime, _ = meter.Float64ObservableCounter("withhuman.db.pool.acquire.wait_time",
		metric.WithUnit("s"), metric.WithDescription("Total time acquisitions spent waiting for a free connection."))
)

// StartHTTPServerRequest counts a request in flight and returns the
// function that records it once answered with the route template it
// matched (empty when none did) and the status code.
func StartHTTPServerRequest(ctx context.Context, method string, secure bool) func(route string, status int) {
	started := time.Now()
	requestMethod, scheme := httpMethod(method), "http"
	if secure {
		scheme = "https"
	}
	httpServerActive.Add(ctx, 1, requestMethod, scheme)
	return func(route string, status int) {
		httpServerActive.Add(ctx, -1, requestMethod, scheme)
		attributes := []attribute.KeyValue{httpServerDuration.AttrResponseStatusCode(status)}
		if route != "" {
			attributes = append(attributes, httpServerDuration.AttrRoute(route))
		}
		if status >= http.StatusInternalServerError {
			attributes = append(attributes, semconv.ErrorTypeKey.String(strconv.Itoa(status)))
		}
		httpServerDuration.Record(ctx, time.Since(started).Seconds(), requestMethod, scheme, attributes...)
	}
}

func httpMethod(method string) httpconv.RequestMethodAttr {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return httpconv.RequestMethodAttr(method)
	}
	return httpconv.RequestMethodOther
}

// ApprovalCreated records a committed new request and the status it
// started in: pending, or the pipeline's approve or deny.
func ApprovalCreated(ctx context.Context, status string) {
	approvalsCreated.Add(ctx, 1, metric.WithAttributes(AttributeApprovalStatus.String(status)))
}

// ApprovalResolved records a request's committed terminal decision and
// how long it took from the request's creation.
func ApprovalResolved(ctx context.Context, status, source string, createdAt, decidedAt time.Time) {
	attributes := metric.WithAttributes(AttributeApprovalStatus.String(status), AttributeDecisionSource.String(source))
	approvalsResolved.Add(ctx, 1, attributes)
	approvalDecisionDuration.Record(ctx, max(decidedAt.Sub(createdAt), 0).Seconds(), attributes)
}

// PendingApprovals reads how many requests are pending and when the
// oldest was created (zero when none is).
type PendingApprovals func(ctx context.Context) (count int64, oldestCreatedAt time.Time, err error)

// ObservePendingApprovals reports the pending backlog on every
// collection until the returned function unregisters it.
func ObservePendingApprovals(read PendingApprovals) (func(), error) {
	registration, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		count, oldest, err := read(ctx)
		if err != nil {
			return err
		}
		observer.ObserveInt64(approvalsPending, count)
		age := 0.0
		if !oldest.IsZero() {
			age = max(time.Since(oldest), 0).Seconds()
		}
		observer.ObserveFloat64(approvalsPendingOldestAge, age)
		return nil
	}, approvalsPending, approvalsPendingOldestAge)
	if err != nil {
		return nil, err
	}
	return func() { _ = registration.Unregister() }, nil
}

// PipelineEvaluated records one pipeline run's duration and outcome; a
// non-empty errorType marks a run that failed before it could conclude.
func PipelineEvaluated(ctx context.Context, stage, outcome, errorType string, duration time.Duration) {
	attributes := []attribute.KeyValue{AttributePipelineStage.String(stage)}
	if outcome != "" {
		attributes = append(attributes, AttributePipelineOutcome.String(outcome))
	}
	if errorType != "" {
		attributes = append(attributes, semconv.ErrorTypeKey.String(errorType))
	}
	pipelineEvaluationDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attributes...))
}

// PipelineBlockEvaluated records one block's evaluation; errorCode is
// the block's error code when it handed the request to a person
// because it failed.
func PipelineBlockEvaluated(ctx context.Context, blockType, outcome, errorCode string, duration time.Duration) {
	attributes := []attribute.KeyValue{AttributeBlockType.String(blockType), AttributeBlockOutcome.String(outcome)}
	if errorCode != "" {
		attributes = append(attributes, semconv.ErrorTypeKey.String(errorCode))
	}
	pipelineBlockDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attributes...))
}

// WebhookDelivered records one webhook attempt. A transport failure has
// no status; timedOut separates the deadline from other failures. A
// status outside 2xx is the attempt's error type.
func WebhookDelivered(ctx context.Context, purpose string, status int, failed, timedOut bool, duration time.Duration) {
	attributes := []attribute.KeyValue{AttributeWebhookPurpose.String(purpose)}
	switch {
	case timedOut:
		attributes = append(attributes, semconv.ErrorTypeKey.String(errorTypeTimeout))
	case failed:
		attributes = append(attributes, semconv.ErrorTypeKey.String(errorTypeDeliveryFailed))
	case status < 200 || status > 299:
		attributes = append(attributes, semconv.ErrorTypeKey.String(strconv.Itoa(status)))
	}
	if status != 0 {
		attributes = append(attributes, semconv.HTTPResponseStatusCode(status))
	}
	webhookDeliveryDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attributes...))
}

// NotificationDispatched records one channel handling one message.
func NotificationDispatched(ctx context.Context, channel, message string, failed bool, duration time.Duration) {
	attributes := []attribute.KeyValue{AttributeNotificationChannel.String(channel), AttributeNotificationMessage.String(message)}
	if failed {
		attributes = append(attributes, semconv.ErrorTypeKey.String(errorTypeDeliveryFailed))
	}
	notificationDispatchDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attributes...))
}

// NotificationHeld records a prompt to one contact kind held for a
// digest.
func NotificationHeld(ctx context.Context, kind string) {
	notificationHolds.Add(ctx, 1, metric.WithAttributes(AttributeContactKind.String(kind)))
}

// EscalationEffect records one applied escalation effect.
func EscalationEffect(ctx context.Context, effect string) {
	escalationEffects.Add(ctx, 1, metric.WithAttributes(AttributeEscalationEffect.String(effect)))
}

// ObservePool reports a PostgreSQL pool's state, under the pool name
// that tells a process's pools apart, until the returned function
// unregisters it.
func ObservePool(name string, pool *pgxpool.Pool) (func(), error) {
	poolName := semconv.DBClientConnectionPoolName(name)
	used := metric.WithAttributes(poolName, semconv.DBClientConnectionStateUsed)
	idle := metric.WithAttributes(poolName, semconv.DBClientConnectionStateIdle)
	named := metric.WithAttributes(poolName)
	registration, err := meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		stat := pool.Stat()
		observer.ObserveInt64(dbConnections, int64(stat.AcquiredConns()), used)
		observer.ObserveInt64(dbConnections, int64(stat.IdleConns()), idle)
		observer.ObserveInt64(dbConnectionsMax, int64(stat.MaxConns()), named)
		observer.ObserveInt64(dbConnectionTimeouts, stat.CanceledAcquireCount(), named)
		observer.ObserveInt64(dbAcquireWaits, stat.EmptyAcquireCount(), named)
		observer.ObserveFloat64(dbAcquireWaitTime, stat.EmptyAcquireWaitTime().Seconds(), named)
		return nil
	}, dbConnections, dbConnectionsMax, dbConnectionTimeouts, dbAcquireWaits, dbAcquireWaitTime)
	if err != nil {
		return nil, err
	}
	return func() { _ = registration.Unregister() }, nil
}

// TemporalMetrics is the Temporal SDK's metrics handler over the
// process's provider: workflow and activity task latencies, schedule to
// start, failures and the client's requests, all under Temporal's own
// names.
func TemporalMetrics(logger *slog.Logger) client.MetricsHandler {
	return temporalotel.NewMetricsHandler(temporalotel.MetricsHandlerOptions{
		Meter: Meter("go.temporal.io/sdk"),
		OnError: func(err error) {
			if logger != nil {
				logger.Warn("Temporal metric could not be recorded - "+err.Error(), "error", err)
			}
		},
	})
}
