package runtime

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/internal/httpapi"
)

func startDrainingServer(t *testing.T, handler http.Handler, drain *httpapi.Drain, drainDelay, shutdownTimeout time.Duration) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan error, 1)
	go func() { done <- serveUntilDrained(ctx, server, listener, drain, drainDelay, shutdownTimeout, logger) }()
	return "http://" + listener.Addr().String(), cancel, done
}

// A stop signal used to end the process the moment Serve returned, which
// is the moment Shutdown begins: in-flight approval calls were cut off
// and the balancer kept routing to an address that no longer answered.
func TestServeUntilDrainedServesThroughTheDelayAndFinishesInFlightRequests(t *testing.T) {
	drain := httpapi.NewDrain()
	started := make(chan struct{})
	var finished atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(response http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(400 * time.Millisecond)
		finished.Store(true)
		_, _ = io.WriteString(response, "done")
	})
	mux.HandleFunc("/readyz", func(response http.ResponseWriter, _ *http.Request) {
		if drain.Draining() {
			response.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	base, stop, done := startDrainingServer(t, mux, drain, 200*time.Millisecond, 5*time.Second)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	slow := make(chan string, 1)
	go func() {
		response, err := client.Get(base + "/slow")
		if err != nil {
			slow <- err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		slow <- string(body)
	}()
	<-started
	stop()

	// During the delay the listener still accepts, and readiness says go
	// elsewhere.
	time.Sleep(50 * time.Millisecond)
	response, err := client.Get(base + "/readyz")
	if err != nil {
		t.Fatalf("a request during the drain delay was refused: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readiness during the drain delay: %d", response.StatusCode)
	}

	if got := <-slow; got != "done" {
		t.Fatalf("the in-flight request was cut off: %q", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the in-flight request finished")
	}
	if !finished.Load() {
		t.Fatal("serve returned before the in-flight request finished")
	}
	if _, err := client.Get(base + "/readyz"); err == nil {
		t.Fatal("the listener still accepts after serve returned")
	}
}

func TestServeUntilDrainedCutsOffWhatOutlivesTheShutdownTimeout(t *testing.T) {
	started := make(chan struct{})
	handler := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	})
	base, stop, done := startDrainingServer(t, handler, httpapi.NewDrain(), 0, 100*time.Millisecond)
	go func() {
		if response, err := http.Get(base); err == nil {
			response.Body.Close()
		}
	}()
	<-started
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a timed-out shutdown is not a failure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve waited past the shutdown timeout")
	}
}
