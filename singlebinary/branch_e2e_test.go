package singlebinary

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
	"github.com/withHumanAI/withHuman/runtime"
)

// Branch blocks end to end: the whole product in one process, a webhook
// endpoint served by the test, and a pipeline whose refunds branch holds a
// webhook and a condition. A call the branch skips is decided on the
// request path without a delivery; refunds suspend in front of the webhook,
// and the worker's resume decides them inside the branch or, when nothing
// there decides, hands them to a person at the branch's end.
func TestBranchPipelineEndToEnd(t *testing.T) {
	cacheDir, binariesDir := embeddedpgtest.SharedDirs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t.Setenv("WITHHUMAN_SECRETS_KEY", strings.Repeat("k", 32))
	t.Setenv("WITHHUMAN_WEBHOOK_ALLOW_INSECURE", "true")
	t.Setenv("WITHHUMAN_WEBHOOK_ALLOW_PRIVATE_NETWORKS", "true")

	// The fraud service approves refunds under 100 and passes the rest on.
	var deliveries atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		deliveries.Add(1)
		var delivery struct {
			Request struct {
				Arguments struct {
					Amount float64 `json:"amount"`
				} `json:"arguments"`
			} `json:"request"`
		}
		if err := json.NewDecoder(request.Body).Decode(&delivery); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		outcome := "next"
		if delivery.Request.Arguments.Amount < 100 {
			outcome = "approve"
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"outcome":%q,"reason":"Scored."}`, outcome)
	}))
	defer endpoint.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The pipeline's conclusion is an audit event, which the open edition
	// discards; the test keeps them the way a keeping edition's sink
	// would, on the server (request path) and the worker (resumes) alike.
	recorder := &db.RecordingAudit{}
	server := openEdition()
	server.AuditSink = recorder
	unlock := embeddedpgtest.LockStartup(t)
	instance, err := Start(ctx, Config{Server: server, Worker: runtime.WorkerOptions{AuditSink: recorder},
		DataDir:             t.TempDir(),
		HTTPAddress:         "127.0.0.1:0",
		BootstrapSecret:     "branch-e2e-secret",
		Logger:              logger,
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
		"secret":            "branch-e2e-secret",
		"organization_slug": "acme",
		"organization_name": "Acme",
		"display_name":      "Branch Owner",
		"email":             "owner@example.com",
		"password":          "correct-horse-battery-staple",
	}, http.StatusCreated, nil)

	api.postJSON("/api/v1/agents", map[string]any{"name": "Billing", "slug": "billing"}, http.StatusCreated, nil)
	var enrolled struct {
		Credential struct {
			Token string `json:"token"`
		} `json:"credential"`
	}
	api.postJSON("/api/v1/agents/billing/instances", map[string]any{"name": "billing-runner"}, http.StatusCreated, &enrolled)
	token := enrolled.Credential.Token

	api.postJSON("/api/v1/webhook_endpoints/fraud-service/revisions", map[string]any{
		"name": "Fraud service", "url": endpoint.URL + "/approvals",
	}, http.StatusCreated, nil)
	api.request("POST", "/api/v1/webhook_endpoints/fraud-service/revisions/1/activate", nil, http.StatusOK, nil, "If-Match", `"0"`)

	condition := func(field, operator string, value any) map[string]any {
		return map[string]any{"id": uuid.NewString(), "field": field, "operator": operator, "value": value}
	}
	// The billing team's pipeline holds the branch; the entry pipeline
	// delegates the billing agent's requests to it.
	var created struct {
		Revision   int64 `json:"revision"`
		BlockCount int   `json:"block_count"`
	}
	api.postJSON("/api/v1/request-approval-pipelines", map[string]any{
		"pipeline_key": "billing", "name": "Billing",
		"blocks": []any{
			map[string]any{
				"id": "refunds", "name": "Refunds", "type": "branch", "enabled": true, "timeout": "50ms", "max_attempts": 1,
				"config": map[string]any{
					"when": condition("/request/tool", "equals", "stripe.refund"),
					"blocks": []any{
						map[string]any{
							"id": "fraud-check", "name": "Fraud service", "type": "webhook", "enabled": true, "timeout": "5s", "max_attempts": 1,
							"config": map[string]any{
								"endpoint_key": "fraud-service", "allowed_outcomes": []string{"next", "approve"},
								"include": map[string]any{"agent_reasoning": false, "context": false, "previous_metadata": true},
								"reason":  "The fraud service approved this refund.",
							},
						},
						map[string]any{
							"id": "large-refunds", "name": "Deny large refunds", "type": "branch", "enabled": true, "timeout": "50ms", "max_attempts": 1,
							"config": map[string]any{
								"when": condition("/request/arguments/amount", "greater_than", 1000), "reason": "",
								"blocks": []any{map[string]any{
									"id": "deny-large", "name": "Deny", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
									"config": map[string]any{"outcome": "deny", "reason": "Refunds over 1000 are never automatic."},
								}},
							},
						},
					},
					"reason": "Refunds the checks did not settle need a person.",
				},
			},
			map[string]any{
				"id": "everything-else", "name": "Approve everything else", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
				"config": map[string]any{"outcome": "approve", "reason": "Other calls are routine."},
			},
		},
	}, http.StatusCreated, &created)
	if created.BlockCount != 5 || created.Revision != 1 {
		t.Fatalf("created pipeline = %+v, want revision 1 with every block including those inside the branch", created)
	}
	var entry struct {
		Revision int64 `json:"revision"`
	}
	api.postJSON("/api/v1/request-approval-pipelines/entry/revisions", map[string]any{
		"blocks": []any{map[string]any{
			"id": "to-billing", "name": "Billing agent", "type": "delegate", "enabled": true, "timeout": "50ms", "max_attempts": 1,
			"config": map[string]any{"when": condition("/agent/slug", "equals", "billing"), "pipeline_key": "billing"},
		}},
	}, http.StatusCreated, &entry)
	api.request("POST", fmt.Sprintf("/api/v1/request-approval-pipelines/entry/revisions/%d/activate", entry.Revision), nil, http.StatusOK, nil, "If-Match", `"1"`)
	// The delegated pipeline cannot be archived while the entry names it.
	api.request("DELETE", "/api/v1/request-approval-pipelines/billing", nil, http.StatusConflict, nil)

	type approval struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	create := func(key, tool string, arguments map[string]any) approval {
		t.Helper()
		var created approval
		api.request("POST", "/api/aap/v1/requests", map[string]any{
			"tool": tool, "arguments": arguments, "timeout": "5m",
		}, http.StatusCreated, &created, "Authorization", "Bearer "+token, "Idempotency-Key", key)
		return created
	}
	settle := func(request approval) string {
		t.Helper()
		deadline := time.Now().Add(time.Minute)
		for {
			var current approval
			api.request("GET", "/api/aap/v1/requests/"+request.ID+"?wait=5s", nil, http.StatusOK, &current, "Authorization", "Bearer "+token)
			if current.Status != "pending" || time.Now().After(deadline) {
				return current.Status
			}
		}
	}

	// Not a refund: the branch is skipped on the request path.
	if read := create("branch-read", "Read", map[string]any{"path": "README.md"}); read.Status != "approved" || deliveries.Load() != 0 {
		t.Fatalf("a skipped branch must decide inline without a delivery: %+v, %d deliveries", read, deliveries.Load())
	}
	// Refunds suspend in front of the webhook and the worker decides.
	if status := settle(create("branch-small", "stripe.refund", map[string]any{"amount": 10})); status != "approved" {
		t.Fatalf("small refund %s, want approved by the fraud service", status)
	}
	if status := settle(create("branch-large", "stripe.refund", map[string]any{"amount": 5000})); status != "denied" {
		t.Fatalf("large refund %s, want denied inside the branch", status)
	}
	middle := create("branch-middle", "stripe.refund", map[string]any{"amount": 500})
	// The worker's resume completes the pipeline at the branch's end: the
	// completed event names the branch, and the branch's closing evaluated
	// event carries its configured reason.
	var completed struct {
		ReasonCode       string `json:"reason_code"`
		TerminalBlockID  string `json:"terminal_block_id"`
		TerminalBlockKey string `json:"terminal_block_key"`
	}
	deadline := time.Now().Add(time.Minute)
	for completed.TerminalBlockKey == "" && time.Now().Before(deadline) {
		for _, event := range recorder.OfType("request_approval_pipeline.completed") {
			if string(event.SubjectID) == middle.ID {
				if err := json.Unmarshal(event.Data, &completed); err != nil {
					t.Fatal(err)
				}
			}
		}
		if completed.TerminalBlockKey == "" {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if completed.ReasonCode != "end_of_branch" || completed.TerminalBlockKey != "refunds" {
		t.Fatalf("a refund nothing in the branch decided must reach a person at the branch's end: %+v", completed)
	}
	var closing struct {
		BlockType   string `json:"block_type"`
		Reason      string `json:"reason"`
		EndOfBranch bool   `json:"end_of_branch"`
	}
	for _, event := range recorder.OfType("request_approval_block.evaluated") {
		var data struct {
			BlockID     string `json:"block_id"`
			BlockType   string `json:"block_type"`
			Reason      string `json:"reason"`
			EndOfBranch bool   `json:"end_of_branch"`
		}
		if string(event.SubjectID) == middle.ID && json.Unmarshal(event.Data, &data) == nil && data.BlockID == completed.TerminalBlockID && data.EndOfBranch {
			closing.BlockType, closing.Reason, closing.EndOfBranch = data.BlockType, data.Reason, true
		}
	}
	if !closing.EndOfBranch || closing.BlockType != "branch" || closing.Reason != "Refunds the checks did not settle need a person." {
		t.Fatalf("the branch's closing event must carry its type and configured reason: %+v", closing)
	}
	var still approval
	api.request("GET", "/api/aap/v1/requests/"+middle.ID, nil, http.StatusOK, &still, "Authorization", "Bearer "+token)
	if still.Status != "pending" {
		t.Fatalf("the catch-all after the branch must not decide an entered refund: %s", still.Status)
	}
	if deliveries.Load() != 3 {
		t.Fatalf("got %d deliveries, want one per refund", deliveries.Load())
	}
}
