package singlebinary

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// The agent-owner route end to end: the organization pipeline's default
// asks the agent's owner with a fallback path. A personal agent's request
// goes to the person who registered its instance, who approves their own
// agent's request; a service agent's request, which has no owner, takes
// the fallback path at routing time.
func TestAgentOwnerRouteEndToEnd(t *testing.T) {
	cacheDir, binariesDir := embeddedpgtest.SharedDirs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	unlock := embeddedpgtest.LockStartup(t)
	instance, err := Start(ctx, Config{Server: openEdition(),
		DataDir:             t.TempDir(),
		HTTPAddress:         "127.0.0.1:0",
		BootstrapSecret:     "owner-e2e-secret",
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		PostgresCacheDir:    cacheDir,
		PostgresBinariesDir: binariesDir,
	})
	unlock()
	if err != nil {
		t.Fatalf("start single binary: %v", err)
	}
	defer shutdownHelper(t, instance)()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	api := &apiClient{t: t, client: &http.Client{Jar: jar, Timeout: 30 * time.Second}, base: instance.BaseURL}
	api.waitReady(ctx)
	api.postJSON("/auth/bootstrap", map[string]any{
		"secret": "owner-e2e-secret", "organization_slug": "acme", "organization_name": "Acme",
		"display_name": "Agent Owner", "email": "owner@example.com", "password": "correct-horse-battery-staple",
	}, http.StatusCreated, nil)
	var me struct {
		Principal struct {
			MembershipID string `json:"membership_id"`
		} `json:"principal"`
	}
	api.request("GET", "/api/v1/me", nil, http.StatusOK, &me)
	ownerID := me.Principal.MembershipID

	api.postJSON("/api/v1/escalation-paths", map[string]any{
		"path_key": "on-call", "name": "On call", "working_hours": []any{},
		"nodes": []any{map[string]any{
			"id": uuid.NewString(), "type": "level", "escalate_after": "15m",
			"targets": []any{map[string]any{"type": "user", "id": ownerID}},
		}},
	}, http.StatusCreated, nil)
	api.request("POST", "/api/v1/escalation-paths/on-call/revisions/1/activate", nil, http.StatusOK, nil, "If-Match", `"0"`)

	var revision struct {
		Revision             int64 `json:"revision"`
		DefaultAskAgentOwner bool  `json:"default_ask_agent_owner"`
	}
	api.postJSON("/api/v1/request-approval-pipelines/entry/revisions", map[string]any{
		"blocks": []any{}, "default_escalation_path": "on-call", "default_ask_agent_owner": true,
	}, http.StatusCreated, &revision)
	if !revision.DefaultAskAgentOwner {
		t.Fatalf("revision = %+v", revision)
	}
	api.request("POST", fmt.Sprintf("/api/v1/request-approval-pipelines/entry/revisions/%d/activate", revision.Revision),
		nil, http.StatusOK, nil, "If-Match", `"1"`)

	register := func(kind, slug string) string {
		t.Helper()
		api.postJSON("/api/v1/agents", map[string]any{"name": slug, "slug": slug, "kind": kind}, http.StatusCreated, nil)
		var registered struct {
			Credential struct {
				Token string `json:"token"`
			} `json:"credential"`
		}
		api.postJSON("/api/v1/agents/"+slug+"/instances", map[string]any{"name": slug + "-runner"}, http.StatusCreated, &registered)
		return registered.Credential.Token
	}
	type routingView struct {
		Outcome            string `json:"outcome"`
		EscalationPathKey  string `json:"escalation_path_key"`
		AgentOwnerFallback string `json:"agent_owner_fallback"`
		AgentOwner         *struct {
			ID string `json:"id"`
		} `json:"agent_owner"`
		Targets []struct {
			ID string `json:"id"`
		} `json:"targets"`
	}
	request := func(token, key string) (string, routingView) {
		t.Helper()
		var created struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		api.request("POST", "/api/aap/v1/requests", map[string]any{
			"tool": "Bash", "arguments": map[string]any{"command": "git push"}, "timeout": "5m",
		}, http.StatusCreated, &created, "Authorization", "Bearer "+token, "Idempotency-Key", key)
		if created.Status != "pending" {
			t.Fatalf("request %s is %s, want pending", key, created.Status)
		}
		var view routingView
		api.request("GET", "/api/v1/requests/"+created.ID+"/routing", nil, http.StatusOK, &view)
		return created.ID, view
	}

	// The personal agent's request goes to its owner, who approves it.
	personal, view := request(register("personal", "claude-code"), "owner-personal")
	if view.Outcome != "agent_owner" || view.AgentOwner == nil || view.AgentOwner.ID != ownerID ||
		view.EscalationPathKey != "on-call" || len(view.Targets) != 1 || view.Targets[0].ID != ownerID {
		t.Fatalf("personal agent routing = %+v", view)
	}
	api.request("POST", "/api/v1/requests/"+personal+"/decision", map[string]any{
		"status": "approved", "channel": "web",
	}, http.StatusOK, nil, "Idempotency-Key", "owner-personal-decision")
	var decided struct {
		Status string `json:"status"`
	}
	api.request("GET", "/api/v1/requests/"+personal, nil, http.StatusOK, &decided)
	if decided.Status != "approved" {
		t.Fatalf("the owner's own agent's request is %s, want approved", decided.Status)
	}

	// The service agent has no owner, so the fallback takes its request.
	_, view = request(register("service", "support-bot"), "owner-service")
	if view.Outcome != "escalated" || view.AgentOwner != nil || view.AgentOwnerFallback != "no_agent_owner" || view.EscalationPathKey != "on-call" {
		t.Fatalf("service agent routing = %+v", view)
	}
}
