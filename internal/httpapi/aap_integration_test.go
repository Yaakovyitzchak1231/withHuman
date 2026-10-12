package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentapprovalprotocol/agentapprovalprotocol/adapters"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db/dbtest"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
	"github.com/withHumanAI/withHuman/internal/approvals"
	"github.com/withHumanAI/withHuman/internal/postgres"
)

type aapTestClock struct{ offset atomic.Int64 }

func (c *aapTestClock) Now() time.Time { return time.Now().UTC().Add(time.Duration(c.offset.Load())) }

// Only the policy choice and workflow scheduler are fixtures. HTTP handlers,
// service validation, ownership, idempotency and decision storage are production.
type aapTestEvaluator struct{}

func (aapTestEvaluator) Evaluate(_ context.Context, r domain.ApprovalRequest) (application.InitializeApprovalInput, error) {
	in := application.InitializeApprovalInput{Request: r}
	types := []string{"approval.created", "request_approval_pipeline.started", "request_approval_pipeline.completed"}
	if r.Tool != "pending" {
		in.Request.Status = domain.ApprovalApproved
		in.Decision = &domain.Decision{ID: domain.ID(uuid.NewString()), RequestID: r.ID, OrganizationID: r.OrganizationID, Source: domain.DecisionSourcePipeline, Status: domain.ApprovalApproved, Channel: "pipeline", Auth: domain.AuthContext{Method: "pipeline", Assurance: "automatic", AuthenticatedAt: r.CreatedAt}, IdempotencyKey: "decision:" + string(r.ID), DecidedAt: r.CreatedAt}
		types = append(types, "approval.decided")
	}
	for _, kind := range types {
		actor, actorID := domain.ActorSystem, domain.ID("")
		if kind == "approval.created" {
			actor, actorID = domain.ActorAgent, r.AgentID
		}
		in.AuditEvents = append(in.AuditEvents, domain.AuditEvent{ID: domain.ID(uuid.NewString()), OrganizationID: r.OrganizationID, EventType: kind, ActorType: actor, ActorID: actorID, SubjectType: "approval_request", SubjectID: r.ID, Data: json.RawMessage(`{}`), OccurredAt: r.CreatedAt})
	}
	return in, nil
}

type aapTestLifecycle struct {
	application.LifecycleOrchestrator
}

func (aapTestLifecycle) StartApproval(context.Context, application.StartApprovalInput) error {
	return nil
}
func (aapTestLifecycle) SubmitCancellation(context.Context, application.SubmitCancellationInput) error {
	return nil
}

func TestAAPReadRequiresAnInstance(t *testing.T) {
	for _, actor := range []domain.ActorType{domain.ActorHuman, domain.ActorAPIKey, domain.ActorAgent} {
		api := &API{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		router := gin.New()
		router.GET("/requests/:id", func(c *gin.Context) { setPrincipal(c, domain.Principal{ActorType: actor}); api.getApproval(c) })
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest("GET", "/requests/test", nil))
		if out.Code != http.StatusForbidden {
			t.Fatalf("%s read without instance: %d", actor, out.Code)
		}
	}
}

func TestAAPLibraryAgainstProvider(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, dbtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	org, user, member := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	if _, err = store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: org, OrganizationSlug: "aap", OrganizationName: "AAP", UserID: user, MembershipID: member, DisplayName: "Owner", Email: "aap@example.com", PasswordHash: "test", AdmissionSource: domain.AdmissionManual, Now: now}); err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService, OrganizationID: org, ActorID: member, Name: "adapter", AgentSlug: "test", AgentQuota: application.UnlimitedQuota(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	principals := map[string]domain.Principal{}
	for _, token := range []string{"instance-one", "instance-two"} {
		hash := sha256.Sum256([]byte(token))
		id := domain.ID(uuid.NewString())
		if _, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{OrganizationID: org, AgentID: agent.ID, ActorID: user, ActorType: domain.ActorHuman, Name: token, Metadata: json.RawMessage(`{}`), Credential: application.AgentCredentialRecord{ID: id, SecretHash: hash[:], IssuedAt: now, ExpiresAt: now.Add(time.Hour)}, Now: now}); err != nil {
			t.Fatal(err)
		}
		p, err := store.ResolveAgentCredential(ctx, application.ResolveAgentCredentialInput{CredentialID: id, SecretHash: hash[:], Now: now})
		if err != nil {
			t.Fatal(err)
		}
		principals[token] = p
	}
	clock := &aapTestClock{}
	service := approvals.NewService(store, store, store, aapTestEvaluator{}, nil, aapTestLifecycle{}, entitlements.NewUnlimited(), clock)
	api := &API{approvals: service, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		p, ok := principals[strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")]
		if !ok {
			c.AbortWithStatus(401)
			return
		}
		setPrincipal(c, p)
	})
	router.POST("/api/aap/v1/requests", api.createApproval)
	router.GET("/api/aap/v1/requests/:id", api.getApproval)
	router.DELETE("/api/aap/v1/requests/:id", api.cancelApproval)
	router.POST("/api/aap/v1/requests/:id/consume", api.consumeApproval)
	router.POST("/api/aap/v1/requests/:id/outcome", api.reportApprovalOutcome)
	var mode atomic.Int32
	var posts atomic.Int32
	created := make(chan domain.ID, 16)
	polling := make(chan domain.ID, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			polling <- domain.ID(strings.TrimPrefix(r.URL.Path, "/api/aap/v1/requests/"))
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		if r.Method == "POST" && r.URL.Path == "/api/aap/v1/requests" {
			posts.Add(1)
			var response aapApprovalResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &response)
			if response.Status == domain.ApprovalPending {
				created <- response.ID
			}
			if mode.CompareAndSwap(1, 0) {
				w.WriteHeader(503)
				return
			} // commit succeeded, response lost
		}
		if mode.Load() == 2 && rec.Code < 300 {
			var body map[string]any
			json.Unmarshal(rec.Body.Bytes(), &body)
			body["tool"] = "wrong-tool"
			rec.Body.Reset()
			json.NewEncoder(rec.Body).Encode(body)
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}))
	defer server.Close()
	request := func(method, path, token, key, body string) (int, aapApprovalResponse, string) {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var out aapApprovalResponse
		json.Unmarshal(raw, &out)
		return res.StatusCode, out, string(raw)
	}
	home, state := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0700)
	manager, err := adapters.NewWithEnvironment(adapters.Environment{Home: home, ConfigDir: state, Executable: "/fixture/withhuman", LookPath: func(string) (string, error) { return "", os.ErrNotExist }})
	if err != nil {
		t.Fatal(err)
	}
	adapter, _ := manager.Lookup("claude-code")
	if result, err := adapter.Install("instance-one", server.URL+"/api/aap"); err != nil || !result.Complete {
		t.Fatal(result, err)
	}
	hook := func(ctx context.Context, tool, attempt string) bool {
		payload, _ := json.Marshal(map[string]any{"tool_name": tool, "tool_input": map[string]any{"amount": json.Number("9007199254740993")}, "session_id": "test-session", "tool_use_id": attempt})
		var out bytes.Buffer
		err := adapter.RunHook(ctx, bytes.NewReader(payload), &out)
		return err == nil && out.Len() == 0
	}
	decide := func(id domain.ID) {
		n := time.Now().UTC()
		opid := domain.ID(uuid.NewString())
		if _, err := store.CreateDecisionOperation(ctx, application.CreateDecisionOperationInput{Operation: domain.DecisionOperation{ID: opid, OrganizationID: org, RequestID: id, Status: domain.OperationPending, CreatedAt: n}, IdempotencyKey: string(opid), IntentHash: "approve"}); err != nil {
			t.Error(err)
			return
		}
		_, err := store.ApplyDecision(ctx, application.ApplyDecisionInput{Command: domain.DecisionCommand{OperationID: opid, RequestID: id, OrganizationID: org, Principal: domain.Principal{ActorType: domain.ActorHuman, ActorID: user, OrganizationID: org, MembershipID: member, Auth: domain.AuthContext{Method: "local_password", Assurance: "single_factor", AuthenticatedAt: n, SessionID: domain.ID(uuid.NewString())}}, Status: domain.ApprovalApproved, Channel: "web", IdempotencyKey: string(opid)}, Now: n})
		if err != nil {
			t.Error(err)
		}
	}
	t.Run("immediate and lost-response retry", func(t *testing.T) {
		mode.Store(1)
		before := posts.Load()
		if !hook(ctx, "refund", "retry") {
			t.Fatal("library rejected real response")
		}
		if posts.Load()-before != 2 {
			t.Fatal("request was not retried")
		}
	})
	t.Run("polled human decision", func(t *testing.T) {
		done := make(chan bool, 1)
		go func() { done <- hook(ctx, "pending", "human") }()
		select {
		case id := <-created:
			decide(id)
		case <-time.After(5 * time.Second):
			t.Fatal("request not created")
		}
		select {
		case ok := <-done:
			if !ok {
				t.Fatal("polled decision rejected")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("hook did not finish")
		}
	})
	t.Run("atomic consumption", func(t *testing.T) {
		var wg sync.WaitGroup
		var allows atomic.Int32
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if hook(ctx, "refund", "concurrent") {
					allows.Add(1)
				}
			}()
		}
		wg.Wait()
		if allows.Load() != 1 {
			t.Fatalf("approved %d invocations", allows.Load())
		}
	})
	t.Run("cancel on abandonment", func(t *testing.T) {
		bounded, cancel := context.WithCancel(ctx)
		done := make(chan bool, 1)
		go func() { done <- hook(bounded, "pending", "cancel") }()
		var id domain.ID
		select {
		case id = <-created:
			for {
				if <-polling == id {
					break
				}
			}
			cancel()
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("not created")
		}
		if <-done {
			t.Fatal("cancelled call allowed")
		}
		got, err := store.GetApproval(ctx, application.GetApprovalInput{OrganizationID: org, RequestID: id})
		if err != nil || got.Status != domain.ApprovalCancelled || got.Decision.ExpiresAt != nil {
			t.Fatalf("cancellation: %+v %v", got, err)
		}
	})
	t.Run("mismatched response", func(t *testing.T) {
		mode.Store(2)
		defer mode.Store(0)
		if hook(ctx, "refund", "mismatch") {
			t.Fatal("mismatch allowed")
		}
	})
	t.Run("expired stored approval", func(t *testing.T) {
		clock.offset.Store(int64(-6 * time.Minute))
		defer clock.offset.Store(0)
		if hook(ctx, "refund", "stale") {
			t.Fatal("stale permission allowed")
		}
	})

	t.Run("wire fidelity and instance isolation", func(t *testing.T) {
		body := `{"tool":"refund","arguments":{"id":9007199254740993,"amount":1e3},"timeout":"300s","agent_reasoning":"exact reason","context":{"nested":[1,2]}}`
		code, first, raw := request("POST", "/api/aap/v1/requests", "instance-one", "same-key", body)
		if code != 201 {
			t.Fatal(code, raw)
		}
		if first.Timeout != "300s" || !strings.Contains(string(first.Arguments), "9007199254740993") || first.Decision == nil || first.Decision.ExpiresAt == nil || !first.Decision.ExpiresAt.Equal(first.Decision.DecidedAt.Add(5*time.Minute)) {
			t.Fatalf("contract lost: %s", raw)
		}
		for _, field := range []string{"organization_id", "agent_id", "reviewer", "auth", "queue", "source"} {
			if strings.Contains(raw, `"`+field) {
				t.Fatalf("product metadata leaked: %s", raw)
			}
		}
		_, retry, _ := request("POST", "/api/aap/v1/requests", "instance-one", "same-key", body)
		if retry.ID != first.ID || !retry.Decision.ExpiresAt.Equal(*first.Decision.ExpiresAt) {
			t.Fatal("retry extended or duplicated permission")
		}
		code, _, _ = request("POST", "/api/aap/v1/requests", "instance-one", "same-key", strings.Replace(body, "300s", "5m", 1))
		if code != 409 {
			t.Fatalf("changed timeout spelling accepted: %d", code)
		}
		code, second, _ := request("POST", "/api/aap/v1/requests", "instance-two", "same-key", body)
		if code != 201 || second.ID == first.ID {
			t.Fatal("instances share idempotency scope")
		}
		for _, method := range []string{"GET", "DELETE"} {
			code, _, raw = request(method, "/api/aap/v1/requests/"+string(first.ID), "instance-two", "", "")
			if code != 403 && code != 404 {
				t.Fatalf("cross-instance %s: %d %s", method, code, raw)
			}
		}
	})

	errorCode := func(raw string) string {
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal([]byte(raw), &body)
		return body.Error.Code
	}
	create := func(t *testing.T, tool, key string) aapApprovalResponse {
		t.Helper()
		code, created, raw := request("POST", "/api/aap/v1/requests", "instance-one", key, `{"tool":"`+tool+`","arguments":{"amount":1},"timeout":"5m"}`)
		if code != 201 {
			t.Fatal(code, raw)
		}
		return created
	}
	t.Run("consume over HTTP", func(t *testing.T) {
		approved := create(t, "refund", "consume-http")
		path := "/api/aap/v1/requests/" + string(approved.ID) + "/consume"
		code, consumed, raw := request("POST", path, "instance-one", "attempt-1", "")
		if code != 200 || consumed.Consumption == nil || consumed.Consumption.Outcome != nil || consumed.Status != domain.ApprovalApproved {
			t.Fatalf("consume: %d %s", code, raw)
		}
		if strings.Contains(raw, "attempt-1") || strings.Contains(raw, "idempotency") {
			t.Fatalf("the consume key leaked: %s", raw)
		}
		code, replay, raw := request("POST", path, "instance-one", "attempt-1", "{}")
		if code != 200 || !replay.Consumption.ConsumedAt.Equal(consumed.Consumption.ConsumedAt) {
			t.Fatalf("same-key replay: %d %s", code, raw)
		}
		code, _, raw = request("POST", path, "instance-one", "attempt-2", "")
		if code != 409 || errorCode(raw) != "already_consumed" || !strings.Contains(raw, consumed.Consumption.ConsumedAt.Format(time.RFC3339)) {
			t.Fatalf("another key: %d %s", code, raw)
		}
		code, read, raw := request("GET", "/api/aap/v1/requests/"+string(approved.ID), "instance-one", "", "")
		if code != 200 || read.Consumption == nil || !read.Consumption.ConsumedAt.Equal(consumed.Consumption.ConsumedAt) {
			t.Fatalf("read after consume: %d %s", code, raw)
		}
		code, recreated, raw := request("POST", "/api/aap/v1/requests", "instance-one", "consume-http", `{"tool":"refund","arguments":{"amount":1},"timeout":"5m"}`)
		if code != 201 || recreated.Consumption == nil {
			t.Fatalf("create replay after consume: %d %s", code, raw)
		}

		fresh := create(t, "refund", "consume-http-refusals")
		freshPath := "/api/aap/v1/requests/" + string(fresh.ID) + "/consume"
		for name, tc := range map[string]struct {
			token, key, body string
			status           int
			code             string
		}{
			"no key":           {"instance-one", "", "", 400, "invalid_request"},
			"a body field":     {"instance-one", "attempt", `{"note":"x"}`, 400, "invalid_request"},
			"not JSON":         {"instance-one", "attempt", `[`, 400, "invalid_request"},
			"another instance": {"instance-two", "attempt", "", 403, "forbidden"},
		} {
			code, _, raw := request("POST", freshPath, tc.token, tc.key, tc.body)
			if code != tc.status || errorCode(raw) != tc.code {
				t.Fatalf("%s: %d %s", name, code, raw)
			}
		}
		code, _, raw = request("POST", "/api/aap/v1/requests/"+uuid.NewString()+"/consume", "instance-one", "attempt", "")
		if code != 404 {
			t.Fatalf("unknown request: %d %s", code, raw)
		}

		pending := create(t, "pending", "consume-http-pending")
		<-created
		code, _, raw = request("POST", "/api/aap/v1/requests/"+string(pending.ID)+"/consume", "instance-one", "attempt", "")
		if code != 409 || errorCode(raw) != "not_approved" {
			t.Fatalf("pending: %d %s", code, raw)
		}

		clock.offset.Store(int64(-6 * time.Minute))
		stale := create(t, "refund", "consume-http-stale")
		clock.offset.Store(0)
		code, _, raw = request("POST", "/api/aap/v1/requests/"+string(stale.ID)+"/consume", "instance-one", "attempt", "")
		if code != 409 || errorCode(raw) != "approval_expired" {
			t.Fatalf("stale: %d %s", code, raw)
		}
	})
	t.Run("outcome over HTTP", func(t *testing.T) {
		approved := create(t, "refund", "outcome-http")
		path := "/api/aap/v1/requests/" + string(approved.ID) + "/outcome"
		code, _, raw := request("POST", path, "instance-one", "", `{"status":"succeeded"}`)
		if code != 409 || errorCode(raw) != "not_consumed" {
			t.Fatalf("before consume: %d %s", code, raw)
		}
		if code, _, raw := request("POST", "/api/aap/v1/requests/"+string(approved.ID)+"/consume", "instance-one", "attempt-1", ""); code != 200 {
			t.Fatalf("consume: %d %s", code, raw)
		}
		for name, body := range map[string]string{
			"unknown status": `{"status":"done"}`,
			"no status":      `{"note":"Refund issued"}`,
			"empty note":     `{"status":"succeeded","note":""}`,
			"long note":      `{"status":"succeeded","note":"` + strings.Repeat("é", 1001) + `"}`,
			"unknown field":  `{"status":"succeeded","detail":"x"}`,
		} {
			if code, _, raw := request("POST", path, "instance-one", "", body); code != 400 || errorCode(raw) != "invalid_request" {
				t.Fatalf("%s: %d %s", name, code, raw)
			}
		}
		if code, _, raw := request("POST", path, "instance-two", "", `{"status":"succeeded"}`); code != 403 {
			t.Fatalf("another instance: %d %s", code, raw)
		}
		code, reported, raw := request("POST", path, "instance-one", "", `{"status":"succeeded","note":"`+strings.Repeat("é", 1000)+`"}`)
		if code != 200 || reported.Consumption == nil || reported.Consumption.Outcome == nil || reported.Consumption.Outcome.Status != domain.ConsumptionSucceeded || reported.Status != domain.ApprovalApproved {
			t.Fatalf("outcome: %d %s", code, raw)
		}
		code, again, raw := request("POST", path, "instance-one", "", `{"status":"succeeded","note":"`+strings.Repeat("é", 1000)+`"}`)
		if code != 200 || !again.Consumption.Outcome.ReportedAt.Equal(reported.Consumption.Outcome.ReportedAt) {
			t.Fatalf("identical outcome: %d %s", code, raw)
		}
		if code, _, raw := request("POST", path, "instance-one", "", `{"status":"failed"}`); code != 409 || errorCode(raw) != "outcome_conflict" {
			t.Fatalf("conflicting outcome: %d %s", code, raw)
		}
	})
}
