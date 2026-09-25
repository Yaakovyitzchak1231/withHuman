package aapdelivery_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/db/dbtest"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
	"github.com/withHumanAI/withHuman/internal/aapdelivery"
	"github.com/withHumanAI/withHuman/internal/approvals"
	"github.com/withHumanAI/withHuman/internal/postgres"
	"github.com/withHumanAI/withHuman/internal/secrets"
	"github.com/withHumanAI/withHuman/internal/webhookdelivery"
)

type offsetClock struct{ offset atomic.Int64 }

func (c *offsetClock) Now() time.Time { return time.Now().UTC().Add(time.Duration(c.offset.Load())) }

// pendingEvaluator leaves a "pending" tool for a reviewer and approves
// every other tool inline, the two ways a request can reach its decision.
type pendingEvaluator struct{}

func (pendingEvaluator) Evaluate(_ context.Context, r domain.ApprovalRequest) (application.InitializeApprovalInput, error) {
	in := application.InitializeApprovalInput{Request: r}
	kinds := []string{"approval.created", "request_approval_pipeline.started", "request_approval_pipeline.completed"}
	if r.Tool != "pending" {
		in.Request.Status = domain.ApprovalApproved
		in.Decision = &domain.Decision{ID: domain.ID(uuid.NewString()), RequestID: r.ID, OrganizationID: r.OrganizationID,
			Source: domain.DecisionSourcePipeline, Status: domain.ApprovalApproved, Channel: "pipeline",
			Auth:           domain.AuthContext{Method: "pipeline", Assurance: "automatic", AuthenticatedAt: r.CreatedAt},
			IdempotencyKey: "decision:" + string(r.ID), DecidedAt: r.CreatedAt}
		kinds = append(kinds, "approval.decided")
	}
	for _, kind := range kinds {
		actor, actorID := domain.ActorSystem, domain.ID("")
		if kind == "approval.created" {
			actor, actorID = domain.ActorAgent, r.AgentID
		}
		in.AuditEvents = append(in.AuditEvents, domain.AuditEvent{ID: domain.ID(uuid.NewString()), OrganizationID: r.OrganizationID,
			EventType: kind, ActorType: actor, ActorID: actorID, SubjectType: "approval_request", SubjectID: r.ID,
			Data: json.RawMessage(`{}`), OccurredAt: r.CreatedAt})
	}
	return in, nil
}

type noLifecycle struct {
	application.LifecycleOrchestrator
}

func (noLifecycle) StartApproval(context.Context, application.StartApprovalInput) error { return nil }
func (noLifecycle) SubmitCancellation(context.Context, application.SubmitCancellationInput) error {
	return nil
}

// receiver is a harness endpoint that knows one registration's secret,
// checks every signature the way the protocol requires, and fails the
// first notification it sees so the retry path runs.
type receiver struct {
	t              *testing.T
	registrationID string
	key            []byte
	mu             sync.Mutex
	verifications  []map[string]string
	events         map[string][]byte
	notifications  int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	id, timestamp := request.Header.Get("webhook-id"), request.Header.Get("webhook-timestamp")
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	if request.Header.Get("webhook-signature") != "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var message map[string]string
	if err := json.Unmarshal(body, &message); err != nil || message["id"] != id {
		http.Error(w, "body id does not match webhook-id", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch message["type"] {
	case "webhook.verification":
		if message["registration_id"] != r.registrationID {
			http.Error(w, "unknown registration", http.StatusNotFound)
			return
		}
		r.verifications = append(r.verifications, message)
		json.NewEncoder(w).Encode(map[string]string{"challenge": message["challenge"]})
	case "request.resolved":
		r.notifications++
		if r.notifications == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if previous, seen := r.events[id]; seen && string(previous) != string(body) {
			r.t.Errorf("event %s changed between attempts", id)
		}
		r.events[id] = body
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unknown type", http.StatusBadRequest)
	}
}

func TestAsynchronousDeliveryFromMintToAcknowledgedNotification(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, dbtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sealer, err := secrets.NewSealer(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	store.WithSealer(sealer)
	now := time.Now().UTC()
	org, user, member := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	if _, err = store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: org, OrganizationSlug: "async", OrganizationName: "Async", UserID: user, MembershipID: member, DisplayName: "Owner", Email: "async@example.com", PasswordHash: "test", AdmissionSource: domain.AdmissionManual, Now: now}); err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{OrganizationID: org, ActorID: member, Name: "harness", AgentSlug: "harness", AgentQuota: application.UnlimitedQuota(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	provisionerID := domain.ID(uuid.NewString())
	if _, err := store.CreateAgentProvisioner(ctx, application.CreateAgentProvisionerInput{OrganizationID: org, AgentID: agent.ID, ActorID: member, Now: now,
		Provisioner: application.AgentProvisionerRecord{ID: provisionerID, SecretHash: authn.HashToken("provisioner-secret"), IssuedAt: now, ExpiresAt: now.Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	provisionerToken := "whp_" + string(provisionerID) + "_provisioner-secret"

	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	signingSecret := "whsec_" + base64.StdEncoding.EncodeToString(key)
	harness := &receiver{t: t, registrationID: uuid.NewString(), key: key, events: map[string][]byte{}}
	server := httptest.NewServer(harness)
	defer server.Close()

	clock := &offsetClock{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	policy := webhookdelivery.Policy{AllowInsecure: true, AllowPrivateNetworks: true}
	client := webhookdelivery.NewClient(policy)
	auth := authn.NewService(nil, store, nil, nil, entitlements.NewUnlimited(), nil, clock, authn.ServiceConfig{CredentialTTL: time.Hour})
	auth.RegisterInstanceDeliveries(aapdelivery.NewVerifier(client, policy, sealer, clock, logger))
	delivery := func(registrationID string) *application.ProposedInstanceDelivery {
		return &application.ProposedInstanceDelivery{Type: "webhook", URL: server.URL + "/aap/events", RegistrationID: domain.ID(registrationID), SigningSecret: signingSecret}
	}

	// A receiver that does not expect the registration refuses the
	// challenge, and no instance exists afterwards.
	if _, err := auth.MintAgentInstance(ctx, authn.MintInstanceRequest{Token: provisionerToken, InstanceName: "stranger", Delivery: delivery(uuid.NewString())}); !errors.Is(err, domain.ErrWebhookVerificationFailed) {
		t.Fatalf("an unexpected registration must fail verification, got %v", err)
	}
	unsupported := delivery(harness.registrationID)
	unsupported.Type = "email"
	if _, err := auth.MintAgentInstance(ctx, authn.MintInstanceRequest{Token: provisionerToken, Delivery: unsupported}); !errors.Is(err, domain.ErrUnsupportedDeliveryType) {
		t.Fatalf("expected unsupported delivery type, got %v", err)
	}

	minted, err := auth.MintAgentInstance(ctx, authn.MintInstanceRequest{Token: provisionerToken, InstanceName: "async-runner", Delivery: delivery(harness.registrationID)})
	if err != nil {
		t.Fatal(err)
	}
	if minted.AgentInstance.Delivery == nil || minted.AgentInstance.Delivery.URL != server.URL+"/aap/events" {
		t.Fatalf("minted instance does not report its receiver: %+v", minted.AgentInstance)
	}
	if len(harness.verifications) != 1 || harness.verifications[0]["instance_id"] != string(minted.AgentInstance.ID) {
		t.Fatalf("verification must name the minted instance: %+v", harness.verifications)
	}
	polling, err := auth.MintAgentInstance(ctx, authn.MintInstanceRequest{Token: provisionerToken, InstanceName: "polling-runner"})
	if err != nil {
		t.Fatal(err)
	}

	service := approvals.NewService(store, store, store, pendingEvaluator{}, nil, noLifecycle{}, entitlements.NewUnlimited(), clock)
	create := func(credential domain.IssuedCredential, tool string) domain.ApprovalRequest {
		principal, err := auth.ResolveCredential(ctx, credential.Token)
		if err != nil {
			t.Fatal(err)
		}
		request, err := service.Create(ctx, principal, approvals.CreateInput{Tool: tool, Arguments: json.RawMessage(`{"amount":4900}`), Timeout: time.Hour, TimeoutText: "1h", IdempotencyKey: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
	asyncPrincipal, err := auth.ResolveCredential(ctx, minted.Credential.Token)
	if err != nil {
		t.Fatal(err)
	}

	approvedInline := create(minted.Credential, "issue_refund")
	cancelled := create(minted.Credential, "pending")
	if _, err := service.Cancel(ctx, asyncPrincipal, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	decided := create(minted.Credential, "pending")
	operationID := domain.ID(uuid.NewString())
	if _, err := store.CreateDecisionOperation(ctx, application.CreateDecisionOperationInput{Operation: domain.DecisionOperation{ID: operationID, OrganizationID: org, RequestID: decided.ID, Status: domain.OperationPending, CreatedAt: clock.Now()}, IdempotencyKey: string(operationID), IntentHash: "deny"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyDecision(ctx, application.ApplyDecisionInput{Command: domain.DecisionCommand{OperationID: operationID, RequestID: decided.ID, OrganizationID: org,
		Principal: domain.Principal{ActorType: domain.ActorHuman, ActorID: user, OrganizationID: org, MembershipID: member, Auth: domain.AuthContext{Method: "local_password", Assurance: "single_factor", AuthenticatedAt: clock.Now(), SessionID: domain.ID(uuid.NewString())}},
		Status:    domain.ApprovalDenied, Channel: "web", IdempotencyKey: string(operationID)}, Now: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	create(polling.Credential, "issue_refund")
	stillPending := create(minted.Credential, "pending")

	dispatcher := aapdelivery.NewDispatcher(store, client, sealer, clock, logger)
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if harness.notifications != 3 || len(harness.events) != 2 {
		t.Fatalf("first pass: %d attempts, %d acknowledged; want 3 and 2 with one 503", harness.notifications, len(harness.events))
	}
	clock.offset.Store(int64(2 * time.Hour))
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if harness.notifications != 4 || len(harness.events) != 3 {
		t.Fatalf("after the retry: %d attempts, %d acknowledged; want 4 and 3", harness.notifications, len(harness.events))
	}
	byRequest := map[string]map[string]string{}
	for id, body := range harness.events {
		var event map[string]string
		json.Unmarshal(body, &event)
		if event["id"] != id || event["type"] != "request.resolved" || event["instance_id"] != string(minted.AgentInstance.ID) || len(event) != 5 {
			t.Fatalf("event %s is not a bare request.resolved pointer: %s", id, body)
		}
		byRequest[event["request_id"]] = event
	}
	for _, request := range []domain.ApprovalRequest{approvedInline, cancelled, decided} {
		event, ok := byRequest[string(request.ID)]
		if !ok {
			t.Fatalf("no event for request %s", request.ID)
		}
		current, err := service.Get(ctx, asyncPrincipal, request.ID)
		if err != nil {
			t.Fatal(err)
		}
		if created, _ := time.Parse(time.RFC3339Nano, event["created_at"]); current.Decision == nil || !created.Equal(current.Decision.DecidedAt) {
			t.Fatalf("event created_at %s must equal decided_at of %+v", event["created_at"], current.Decision)
		}
	}
	if _, ok := byRequest[string(stillPending.ID)]; ok {
		t.Fatal("a pending request was announced")
	}
}
