package webhookdelivery

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPolicyRejectsShapesOutsidePublicHTTPS(t *testing.T) {
	strict := Policy{}
	for _, raw := range []string{
		"", "fraud.example.com/hook", "http://fraud.example.com/hook", "ftp://fraud.example.com",
		"https://user:pw@fraud.example.com/hook", "https://fraud.example.com/hook#frag",
		"https://127.0.0.1/hook", "https://10.1.2.3/hook", "https://169.254.169.254/latest",
		"https://[::1]/hook", "https://[fd00::1]/hook", "https://100.64.1.1/hook", "https://[64:ff9b::a00:1]/hook",
	} {
		if _, err := strict.ValidateURL(raw); !errors.Is(err, ErrDestinationBlocked) {
			t.Errorf("%q: got %v, want blocked", raw, err)
		}
	}
	if _, err := strict.ValidateURL("https://fraud.example.com:8443/hooks/approval?x=1"); err != nil {
		t.Fatalf("public https rejected: %v", err)
	}
	relaxed := Policy{AllowInsecure: true, AllowPrivateNetworks: true}
	if _, err := relaxed.ValidateURL("http://127.0.0.1:8080/hook"); err != nil {
		t.Fatalf("relaxed policy rejected loopback http: %v", err)
	}
	if !strict.AllowIP(net.ParseIP("93.184.216.34")) || strict.AllowIP(net.ParseIP("::ffff:192.168.1.1")) {
		t.Fatal("AllowIP misclassified a public or IPv4-mapped private address")
	}
}

func TestSignatureRoundTripAndRejections(t *testing.T) {
	body := []byte(`{"outcome":"next"}`)
	now := time.Unix(1_756_771_200, 0)
	header := Sign("whsec_secret", now, body)
	if !strings.HasPrefix(header, "t=1756771200,v1=") {
		t.Fatalf("unexpected header %q", header)
	}
	if err := VerifySignature("whsec_secret", header, body, now.Add(time.Minute), SignatureTolerance); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	cases := map[string]struct {
		secret string
		header string
		body   []byte
		now    time.Time
	}{
		"wrong secret":  {"other", header, body, now},
		"tampered body": {"whsec_secret", header, []byte(`{"outcome":"approve"}`), now},
		"stale":         {"whsec_secret", header, body, now.Add(6 * time.Minute)},
		"future":        {"whsec_secret", header, body, now.Add(-6 * time.Minute)},
		"malformed":     {"whsec_secret", "v1=abc", body, now},
	}
	for name, tc := range cases {
		if err := VerifySignature(tc.secret, tc.header, tc.body, tc.now, SignatureTolerance); !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("%s: got %v, want invalid signature", name, err)
		}
	}
}

func TestParseAnswerBoundsTheContract(t *testing.T) {
	answer, err := ParseAnswer([]byte(`{"outcome":"next","reason":"  low risk ","metadata":{"risk_score":12}}`))
	if err != nil || answer.Outcome != "next" || answer.Reason != "low risk" || string(answer.Metadata["risk_score"]) != "12" {
		t.Fatalf("valid answer: %+v %v", answer, err)
	}
	bad := map[string]string{
		"unknown field":   `{"outcome":"next","extra":1}`,
		"bad outcome":     `{"outcome":"maybe"}`,
		"missing outcome": `{"reason":"x"}`,
		"not json":        `<html>`,
		"two values":      `{"outcome":"next"} {"outcome":"deny"}`,
		"long reason":     `{"outcome":"next","reason":"` + strings.Repeat("r", 1_025) + `"}`,
		"big metadata":    `{"outcome":"next","metadata":{"blob":"` + strings.Repeat("m", 17*1024) + `"}}`,
	}
	for name, body := range bad {
		if _, err := ParseAnswer([]byte(body)); !errors.Is(err, ErrInvalidAnswer) {
			t.Errorf("%s: got %v, want invalid answer", name, err)
		}
	}
}

func testClient(t *testing.T) *Client {
	t.Helper()
	client := NewClient(Policy{AllowInsecure: true, AllowPrivateNetworks: true})
	client.now = func() time.Time { return time.Unix(1_756_771_200, 0) }
	return client
}

func TestClientSignsAndDeliversWithinTheLimits(t *testing.T) {
	var received http.Header
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"outcome":"next"}`))
	}))
	defer server.Close()
	body := []byte(`{"version":"2026-09-01"}`)
	response, err := testClient(t).Deliver(context.Background(), Delivery{
		URL: server.URL + "/hook", Secret: "whsec_secret", Body: body, Timeout: 2 * time.Second,
		DeliveryID: "delivery-1", IdempotencyKey: "request-1:block-1",
	})
	if err != nil || response.StatusCode != http.StatusOK || string(response.Body) != `{"outcome":"next"}` {
		t.Fatalf("delivery failed: %+v %v", response, err)
	}
	if string(receivedBody) != string(body) || received.Get("Content-Type") != "application/json" || received.Get("User-Agent") != UserAgent {
		t.Fatalf("request shape: body=%s headers=%v", receivedBody, received)
	}
	if received.Get(DeliveryIDHeader) != "delivery-1" || received.Get(IdempotencyKeyHeader) != "request-1:block-1" {
		t.Fatalf("delivery headers: %v", received)
	}
	if err := VerifySignature("whsec_secret", received.Get(SignatureHeader), receivedBody, time.Unix(1_756_771_200, 0), SignatureTolerance); err != nil {
		t.Fatalf("signature did not verify: %v", err)
	}
}

func TestClientDoesNotFollowRedirectsAndBoundsTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "/huge":
			_, _ = w.Write([]byte(strings.Repeat("x", MaximumResponseBytes+1)))
		case "/slow":
			time.Sleep(300 * time.Millisecond)
		}
	}))
	defer server.Close()
	client := testClient(t)
	response, err := client.Deliver(context.Background(), Delivery{URL: server.URL + "/redirect", Timeout: time.Second})
	if err != nil || response.StatusCode != http.StatusFound {
		t.Fatalf("redirect was followed or failed: %+v %v", response, err)
	}
	if _, err := client.Deliver(context.Background(), Delivery{URL: server.URL + "/huge", Timeout: time.Second}); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversize body: got %v", err)
	}
	if _, err := client.Deliver(context.Background(), Delivery{URL: server.URL + "/slow", Timeout: 50 * time.Millisecond}); !IsTimeout(err) {
		t.Fatalf("slow endpoint: got %v, want timeout", err)
	}
}

type fakeResolver struct{ ips []net.IPAddr }

func (r fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) { return r.ips, nil }

func TestClientRefusesHostsThatResolveIntoBlockedRanges(t *testing.T) {
	client := NewClient(Policy{AllowInsecure: true})
	client.resolver = fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("10.0.0.5")}}}
	_, err := client.Deliver(context.Background(), Delivery{URL: "http://rebinding.example/hook", Timeout: time.Second})
	if !errors.Is(err, ErrDestinationBlocked) {
		t.Fatalf("mixed public/private resolution was dialed: %v", err)
	}
}
