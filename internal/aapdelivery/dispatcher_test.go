package aapdelivery

import (
	"net/http"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/internal/webhookdelivery"
)

func TestNextAttemptBacksOffWithinBounds(t *testing.T) {
	now := time.Unix(0, 0)
	for attempt, ceiling := range map[int]time.Duration{1: firstBackoff, 2: 2 * firstBackoff, 12: maxBackoff, 40: maxBackoff} {
		for range 50 {
			wait := nextAttempt(now, attempt, webhookdelivery.Response{StatusCode: http.StatusBadGateway}).Sub(now)
			if wait < ceiling/2 || wait > ceiling {
				t.Fatalf("attempt %d waited %s, want between %s and %s", attempt, wait, ceiling/2, ceiling)
			}
		}
	}
}

func TestNextAttemptHonorsALongerRetryAfter(t *testing.T) {
	now := time.Unix(0, 0)
	response := webhookdelivery.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"120"}}}
	if wait := nextAttempt(now, 1, response).Sub(now); wait != 2*time.Minute {
		t.Fatalf("Retry-After ignored: waited %s", wait)
	}
	response.StatusCode = http.StatusInternalServerError
	if wait := nextAttempt(now, 1, response).Sub(now); wait > firstBackoff {
		t.Fatalf("Retry-After applies only to 429 and 503: waited %s", wait)
	}
}
