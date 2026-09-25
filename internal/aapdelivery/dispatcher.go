package aapdelivery

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/internal/secrets"
	"github.com/withHumanAI/withHuman/internal/webhookdelivery"
	"github.com/withHumanAI/withHuman/loggermdc"
)

const (
	dispatchInterval = time.Second
	// claimLease keeps a claimed event from being attempted again while
	// its attempt is in flight; it outlasts the attempt timeout.
	claimLease   = time.Minute
	claimLimit   = 20
	firstBackoff = 5 * time.Second
	maxBackoff   = time.Hour
)

// Dispatcher delivers queued request.resolved events with exponential
// backoff and jitter until a receiver acknowledges with any 2xx or the
// retry window closes. Leases make it safe to run in every worker.
type Dispatcher struct {
	events application.RequestResolvedEventRepository
	client *webhookdelivery.Client
	sealer *secrets.Sealer
	clock  application.Clock
	logger *slog.Logger
}

func NewDispatcher(events application.RequestResolvedEventRepository, client *webhookdelivery.Client, sealer *secrets.Sealer, clock application.Clock, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{events: events, client: client, sealer: sealer, clock: clock, logger: logger}
}

func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(dispatchInterval)
	defer ticker.Stop()
	for {
		_ = d.Dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Dispatch attempts every event due now, once each.
func (d *Dispatcher) Dispatch(ctx context.Context) error {
	now := d.clock.Now()
	due, err := d.events.ClaimDueRequestResolvedEvents(ctx, application.ClaimDueRequestResolvedEventsInput{
		Now: now, LeaseUntil: now.Add(claimLease), Limit: claimLimit,
	})
	if err != nil {
		if ctx.Err() == nil {
			d.logger.ErrorContext(ctx, "AAP request.resolved claim failed; due events stay queued for the next pass", "error", err)
		}
		return err
	}
	for _, event := range due {
		d.attempt(ctx, event)
	}
	return nil
}

type resolvedEvent struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	InstanceID string `json:"instance_id"`
	RequestID  string `json:"request_id"`
	CreatedAt  string `json:"created_at"`
}

// EventBody is the exact notification body. It is built only from stored
// fields, so every retry sends the same bytes.
func EventBody(event application.RequestResolvedDelivery) []byte {
	body, _ := json.Marshal(resolvedEvent{
		ID: string(event.EventID), Type: "request.resolved", InstanceID: string(event.AgentInstanceID),
		RequestID: string(event.RequestID), CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
	return body
}

func (d *Dispatcher) attempt(ctx context.Context, event application.RequestResolvedDelivery) {
	ctx = loggermdc.With(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID:    string(event.OrganizationID),
		loggermdc.KeyAgentInstanceID:   string(event.AgentInstanceID),
		loggermdc.KeyApprovalRequestID: string(event.RequestID),
	})
	attempt := event.AttemptCount + 1
	var response webhookdelivery.Response
	secret, err := d.sealer.Open(application.InstanceDeliverySigningSecretLabel(event.OrganizationID, event.AgentInstanceID), event.SealedSigningSecret)
	if err == nil {
		body := EventBody(event)
		var headers http.Header
		if headers, err = webhookdelivery.StandardHeaders(secret, string(event.EventID), d.clock.Now(), body); err == nil {
			response, err = d.client.Deliver(ctx, webhookdelivery.Delivery{URL: event.URL, Headers: headers, Body: body, Timeout: AttemptTimeout})
		}
	}
	attemptedAt := d.clock.Now()
	record := application.RecordRequestResolvedAttemptInput{
		EventID: event.EventID, OrganizationID: event.OrganizationID, AttemptedAt: attemptedAt, StatusCode: response.StatusCode,
	}
	if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		record.Delivered = true
		d.logger.InfoContext(ctx, "AAP request.resolved delivery succeeded", "attempt", attempt, "status_code", response.StatusCode)
	} else if next := nextAttempt(attemptedAt, attempt, response); next.Before(event.RetryUntil) {
		record.NextAttemptAt = &next
		d.logger.WarnContext(ctx, "AAP request.resolved delivery failed; retrying later", "attempt", attempt,
			"status_code", response.StatusCode, "next_attempt_at", next, "error", err)
	} else {
		d.logger.ErrorContext(ctx, "AAP request.resolved delivery abandoned; the retry window has closed, so the harness must read the request itself",
			"attempt", attempt, "status_code", response.StatusCode, "error", err)
	}
	if err := d.events.RecordRequestResolvedAttempt(ctx, record); err != nil && ctx.Err() == nil {
		d.logger.ErrorContext(ctx, "AAP request.resolved attempt could not be recorded; the event will be attempted again after its lease", "error", err)
	}
}

// nextAttempt doubles the wait from firstBackoff up to maxBackoff with
// jitter in its upper half, and honors a receiver's Retry-After on 429
// and 503 when it asks for longer.
func nextAttempt(now time.Time, attempt int, response webhookdelivery.Response) time.Time {
	wait := maxBackoff
	if attempt < 20 {
		wait = min(firstBackoff<<(attempt-1), maxBackoff)
	}
	wait = wait/2 + rand.N(wait/2+1)
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable {
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && time.Duration(seconds)*time.Second > wait {
			wait = time.Duration(seconds) * time.Second
		}
	}
	return now.Add(wait)
}
