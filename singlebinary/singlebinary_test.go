package singlebinary

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// Boots the entire product in one process (embedded PostgreSQL, embedded
// Temporal, worker, HTTP server) and exercises a full approval lifecycle
// through the real HTTP APIs, including a durable expiry and a restart of
// the whole instance on the same data directory.
func TestSingleBinaryEndToEnd(t *testing.T) {
	cacheDir, binariesDir := embeddedpgtest.SharedDirs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	start := func() *Instance {
		unlock := embeddedpgtest.LockStartup(t)
		defer unlock()
		instance, err := Start(ctx, Config{Server: openEdition(),
			DataDir:             dataDir,
			HTTPAddress:         "127.0.0.1:0",
			BootstrapSecret:     "e2e-bootstrap-secret",
			Logger:              logger,
			PostgresCacheDir:    cacheDir,
			PostgresBinariesDir: binariesDir,
		})
		if err != nil {
			t.Fatalf("start single binary: %v", err)
		}
		return instance
	}

	booted := time.Now()
	instance := start()
	t.Logf("cold boot took %s", time.Since(booted))
	shutdownOnce := shutdownHelper(t, instance)
	defer shutdownOnce()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	api := &apiClient{t: t, client: client, base: instance.BaseURL}

	api.waitReady(ctx)

	// Bootstrap the owner; the session cookie lands in the jar.
	api.postJSON("/auth/bootstrap", map[string]any{
		"secret":            "e2e-bootstrap-secret",
		"organization_slug": "acme",
		"organization_name": "Acme",
		"display_name":      "E2E Owner",
		"email":             "owner@example.com",
		"password":          "correct-horse-battery-staple",
	}, http.StatusCreated, nil)

	testTeamArchiveAPI(t, api)
	testAgentLifecycleAPI(t, api)

	var agent struct {
		Slug string `json:"slug"`
	}
	api.postJSON("/api/v1/agents", map[string]any{
		"name": "e2e-agent", "slug": "release",
	}, http.StatusCreated, &agent)

	var instanceResponse struct {
		Credential struct {
			Token string `json:"token"`
		} `json:"credential"`
	}
	api.postJSON("/api/v1/agents/"+agent.Slug+"/instances", map[string]any{
		"name": "e2e-runner", "metadata": map[string]any{"environment": "test"},
	}, http.StatusCreated, &instanceResponse)
	token := instanceResponse.Credential.Token
	if token == "" {
		t.Fatal("no agent credential token issued")
	}

	// The AAP path: create a request (through Temporal and the in-process
	// worker), decide it, and read the terminal state back.
	var request struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	api.request("POST", "/api/aap/v1/requests", map[string]any{
		"tool": "deploy.production", "arguments": map[string]any{"version": "e2e"},
		"agent_reasoning": "end-to-end verification", "timeout": "2m",
	}, http.StatusCreated, &request, "Authorization", "Bearer "+token, "Idempotency-Key", "e2e-request-1")
	if request.Status != "pending" {
		t.Fatalf("expected pending request, got %q", request.Status)
	}
	var retried struct {
		ID string `json:"id"`
	}
	api.request("POST", "/api/aap/v1/requests", map[string]any{
		"tool": "deploy.production", "arguments": map[string]any{"version": "e2e"},
		"agent_reasoning": "end-to-end verification", "timeout": "2m",
	}, http.StatusCreated, &retried, "Authorization", "Bearer "+token, "Idempotency-Key", "e2e-request-1")
	if retried.ID != request.ID {
		t.Fatalf("idempotent retry returned %s, want %s", retried.ID, request.ID)
	}

	api.request("POST", "/api/v1/requests/"+request.ID+"/decision", map[string]any{
		"status": "approved", "note": "looks good", "channel": "web",
	}, http.StatusOK, nil, "Idempotency-Key", "e2e-decision-1")

	var decided struct {
		Status string `json:"status"`
	}
	api.request("GET", "/api/aap/v1/requests/"+request.ID, nil, http.StatusOK, &decided, "Authorization", "Bearer "+token)
	if decided.Status != "approved" {
		t.Fatalf("expected approved, got %q", decided.Status)
	}

	// Durable deadline: a 1s timeout must expire through the Temporal
	// timer even though nobody decides.
	var expiring struct {
		ID string `json:"id"`
	}
	api.request("POST", "/api/aap/v1/requests", map[string]any{
		"tool": "deploy.preview", "arguments": map[string]any{"version": "expired"},
		"agent_reasoning": "deadline check", "timeout": "1s",
	}, http.StatusCreated, &expiring, "Authorization", "Bearer "+token, "Idempotency-Key", "e2e-request-2")
	var expired struct {
		Status string `json:"status"`
	}
	api.request("GET", "/api/aap/v1/requests/"+expiring.ID+"?wait=10s", nil, http.StatusOK, &expired, "Authorization", "Bearer "+token)
	if expired.Status != "expired" {
		t.Fatalf("expected expired, got %q", expired.Status)
	}

	// Restart the whole instance on the same data directory: state,
	// including agent credentials and terminal decisions, must survive.
	shutdownOnce()
	restarted := time.Now()
	second := start()
	t.Logf("warm restart took %s", time.Since(restarted))
	defer shutdownHelper(t, second)()

	api.base = second.BaseURL
	api.waitReady(ctx)
	var afterRestart struct {
		Status string `json:"status"`
	}
	api.request("GET", "/api/aap/v1/requests/"+request.ID, nil, http.StatusOK, &afterRestart, "Authorization", "Bearer "+token)
	if afterRestart.Status != "approved" {
		t.Fatalf("state lost across restart: %q", afterRestart.Status)
	}
}

func shutdownHelper(t *testing.T, instance *Instance) func() {
	done := false
	return func() {
		if done {
			return
		}
		done = true
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := instance.Shutdown(shutdownContext); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}
}

type apiClient struct {
	t      *testing.T
	client *http.Client
	base   string
}

func (a *apiClient) waitReady(ctx context.Context) {
	a.t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		response, err := a.client.Get(a.base + "/readyz")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
			if time.Now().After(deadline) {
				a.t.Fatalf("instance never became ready: %d %s", response.StatusCode, body)
			}
		} else if time.Now().After(deadline) {
			a.t.Fatalf("instance never became ready: %v", err)
		}
		select {
		case <-ctx.Done():
			a.t.Fatalf("context cancelled waiting for readiness: %v", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (a *apiClient) postJSON(path string, body any, wantStatus int, out any) {
	a.t.Helper()
	a.request("POST", path, body, wantStatus, out)
}

func (a *apiClient) request(method, path string, body any, wantStatus int, out any, headerPairs ...string) {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headerPairs); i += 2 {
		request.Header.Set(headerPairs[i], headerPairs[i+1])
	}
	response, err := a.client.Do(request)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		a.t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.StatusCode, wantStatus, payload)
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			a.t.Fatalf("%s %s: decode %q: %v", method, path, payload, err)
		}
	}
}
