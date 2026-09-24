package slackconnect

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// Embedded nil ports panic if acknowledgment ever starts performing I/O.
type interactionPorts struct {
	application.SlackConnector
	application.SlackMessenger
	application.SlackConnectionRepository
	application.SlackEventDispatcher
}

func interactionForm(payload string) string {
	return url.Values{"payload": {payload}}.Encode()
}

func TestSlackInteractionAcknowledgmentHasNoSideEffects(t *testing.T) {
	service := newTestService(t, &fakeConnector{}, newMemoryRepository())
	ports := &interactionPorts{}
	service.connector, service.messenger, service.connections, service.dispatcher = ports, ports, ports, ports
	service.contacts = nil
	body := interactionForm(`{"type":"block_actions","api_app_id":"A123","team":{"id":"T1"},"user":{"id":"U1"},"actions":[{"type":"button","action_id":"slack_generated_id","url":"https://approvals.example.com/queue/request-1"}],"response_url":"https://hooks.slack.com/unused"}`)
	timestamp, signature := signedEvent(body, service.clock.Now())
	for range 2 {
		if err := service.ReceiveInteraction(timestamp, signature, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSlackInteractionPayloadValidation(t *testing.T) {
	service := newTestService(t, &fakeConnector{}, newMemoryRepository())
	valid := interactionForm(`{"type":"block_actions","api_app_id":"A123"}`)
	for _, test := range []struct {
		name string
		body string
		want error
	}{
		{"wrong app", interactionForm(`{"type":"block_actions","api_app_id":"A_OTHER"}`), domain.ErrUnauthenticated},
		{"missing app", interactionForm(`{"type":"block_actions"}`), domain.ErrUnauthenticated},
		{"unsupported type", interactionForm(`{"type":"view_submission","api_app_id":"A123"}`), domain.ErrInvalid},
		{"missing type", interactionForm(`{"api_app_id":"A123"}`), domain.ErrInvalid},
		{"missing payload", "other=value", domain.ErrInvalid},
		{"empty payload", "payload=", domain.ErrInvalid},
		{"duplicate payload", valid + "&" + valid, domain.ErrInvalid},
		{"invalid form encoding", valid + "&other=%zz", domain.ErrInvalid},
		{"JSON instead of form", `{"type":"block_actions","api_app_id":"A123"}`, domain.ErrInvalid},
		{"invalid JSON", interactionForm(`{"type":`), domain.ErrInvalid},
		{"null JSON", interactionForm(`null`), domain.ErrInvalid},
		{"array JSON", interactionForm(`[]`), domain.ErrInvalid},
		{"wrong field type", interactionForm(`{"type":123,"api_app_id":"A123"}`), domain.ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			timestamp, signature := signedEvent(test.body, service.clock.Now())
			if err := service.ReceiveInteraction(timestamp, signature, []byte(test.body)); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

// Both callback formats must authenticate the exact bytes before decoding and
// enforce the same timestamp window, including correctly signed old requests.
func TestSlackCallbackAuthentication(t *testing.T) {
	service := newTestService(t, &fakeConnector{}, newMemoryRepository())
	for _, callback := range []struct {
		name    string
		body    string
		receive func(string, string, []byte) error
	}{
		{"interaction", interactionForm(`{"type":"block_actions","api_app_id":"A123"}`), service.ReceiveInteraction},
		{"event", `{"type":"url_verification","challenge":"verify-me"}`, func(timestamp, signature string, body []byte) error {
			_, err := service.ReceiveEvent(context.Background(), timestamp, signature, body)
			return err
		}},
	} {
		t.Run(callback.name, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				offset time.Duration
				want   error
			}{
				{"now", 0, nil},
				{"old boundary", -5 * time.Minute, nil},
				{"future boundary", 5 * time.Minute, nil},
				{"expired", -5*time.Minute - time.Second, domain.ErrUnauthenticated},
				{"future", 5*time.Minute + time.Second, domain.ErrUnauthenticated},
			} {
				t.Run(test.name, func(t *testing.T) {
					timestamp, signature := signedEvent(callback.body, service.clock.Now().Add(test.offset))
					if err := callback.receive(timestamp, signature, []byte(callback.body)); !errors.Is(err, test.want) {
						t.Fatalf("error = %v, want %v", err, test.want)
					}
				})
			}
			timestamp, signature := signedEvent(callback.body, service.clock.Now())
			for _, test := range []struct{ name, timestamp, signature, body string }{
				{"missing signature", timestamp, "", callback.body},
				{"bad signature", timestamp, "v0=bad", callback.body},
				{"missing timestamp", "", signature, callback.body},
				{"invalid timestamp", "not-a-time", signature, callback.body},
				{"tampered body", timestamp, signature, callback.body + " "},
				{"unsigned malformed body", timestamp, signature, "%zz"},
			} {
				t.Run(test.name, func(t *testing.T) {
					if err := callback.receive(test.timestamp, test.signature, []byte(test.body)); !errors.Is(err, domain.ErrUnauthenticated) {
						t.Fatalf("error = %v, want unauthenticated", err)
					}
				})
			}
		})
	}
}
