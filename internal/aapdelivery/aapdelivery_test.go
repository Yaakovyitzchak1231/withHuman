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

// receiver is a harness endpoint that knows one registration's secret
// and checks every signature the way the protocol requires. With
// failFirst it answers the first notification 503 so the retry path runs.
type receiver struct {
	t              *testing.T
	registrationID string
	key            []byte
	failFirst      bool
	mu             sync.Mutex
	verifications  []map[string]string
	events         map[string][]byte
	notifications  int
}

func newReceiver(t *testing.T, seed byte, failFirst bool) (*receiver, string) {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index) + seed
	}
	return &receiver{t: t, registrationID: uuid.NewString(), key: key, failFirst: failFirst, events: map[string][]byte{}},
		"whsec_" + base64.StdEncoding.EncodeToString(key)
}

func (r *receiver) acknowledged() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
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
		if r.failFirst && r.notifications == 1 {
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

// fixture is one organization with an agent, a provisioner token, the
// production store, authn service with a verifier, the approvals service
// and a dispatcher, all on a clock the test can move forward.
type fixture struct {
	t                *testing.T
	ctx              context.Context
	store            *postgres.Store
	auth             *authn.Service
	approvals        *approvals.Service
	dispatcher       *aapdelivery.Dispatcher
	clock            *offsetClock
	org, user        domain.ID
	member           domain.ID
	provisionerToken string
}

func newFixture(t *testing.T) *fixture {
	ctx := context.Background()
	store, err := postgres.Open(ctx, dbtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	sealer, err := secrets.NewSealer(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	store.WithSealer(sealer)
	now := time.Now().UTC()
	f := &fixture{t: t, ctx: ctx, store: store, clock: &offsetClock{},
		org: domain.ID(uuid.NewString()), user: domain.ID(uuid.NewString()), member: domain.ID(uuid.NewString())}
	if _, err = store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: f.org, OrganizationSlug: "async", OrganizationName: "Async", UserID: f.user, MembershipID: f.member, DisplayName: "Owner", Email: "async@example.com", PasswordHash: "test", AdmissionSource: domain.AdmissionManual, Now: now}); err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{OrganizationID: f.org, ActorID: f.member, Name: "harness", AgentSlug: "harness", Kind: domain.AgentService, AgentQuota: application.UnlimitedQuota(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	provisionerID := domain.ID(uuid.NewString())
	if _, err := store.CreateAgentProvisioner(ctx, application.CreateAgentProvisionerInput{OrganizationID: f.org, AgentID: agent.ID, ActorID: f.member, Now: now,
		Provisioner: application.AgentProvisionerRecord{ID: provisionerID, SecretHash: authn.HashToken("provisioner-secret"), IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	f.provisionerToken = "whp_" + string(provisionerID) + "_provisioner-secret"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	policy := webhookdelivery.Policy{AllowInsecure: true, AllowPrivateNetworks: true}
	client := webhookdelivery.NewClient(policy)
	f.auth = authn.NewService(nil, store, nil, nil, entitlements.NewUnlimited(), nil, f.clock, authn.ServiceConfig{CredentialTTL: time.Hour})
	f.auth.RegisterInstanceDeliveries(aapdelivery.NewVerifier(client, policy, sealer, f.clock, logger))
	f.approvals = approvals.NewService(store, store, store, pendingEvaluator{}, nil, noLifecycle{}, entitlements.NewUnlimited(), f.clock)
	f.dispatcher = aapdelivery.NewDispatcher(store, client, sealer, f.clock, logger)
	return f
}

func (f *fixture) mint(request authn.MintInstanceRequest) (authn.MintResult, error) {
	request.Token = f.provisionerToken
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = uuid.NewString()
	}
	return f.auth.MintAgentInstance(f.ctx, request)
}

func (f *fixture) principal(credential domain.IssuedCredential) domain.Principal {
	principal, err := f.auth.ResolveCredential(f.ctx, credential.Token)
	if err != nil {
		f.t.Fatal(err)
	}
	return principal
}

func (f *fixture) create(credential domain.IssuedCredential, tool string) domain.ApprovalRequest {
	request, err := f.approvals.Create(f.ctx, f.principal(credential), approvals.CreateInput{Tool: tool, Arguments: json.RawMessage(`{"amount":4900}`), Timeout: time.Hour, TimeoutText: "1h", IdempotencyKey: uuid.NewString()})
	if err != nil {
		f.t.Fatal(err)
	}
	return request
}

func (f *fixture) decide(requestID domain.ID, status domain.ApprovalStatus) {
	operationID := domain.ID(uuid.NewString())
	if _, err := f.store.CreateDecisionOperation(f.ctx, application.CreateDecisionOperationInput{Operation: domain.DecisionOperation{ID: operationID, OrganizationID: f.org, RequestID: requestID, Status: domain.OperationPending, CreatedAt: f.clock.Now()}, IdempotencyKey: string(operationID), IntentHash: string(status)}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.store.ApplyDecision(f.ctx, application.ApplyDecisionInput{Command: domain.DecisionCommand{OperationID: operationID, RequestID: requestID, OrganizationID: f.org,
		Principal: domain.Principal{ActorType: domain.ActorHuman, ActorID: f.user, OrganizationID: f.org, MembershipID: f.member, Auth: domain.AuthContext{Method: "local_password", Assurance: "single_factor", AuthenticatedAt: f.clock.Now(), SessionID: domain.ID(uuid.NewString())}},
		Status:    status, Channel: "web", IdempotencyKey: string(operationID)}, Now: f.clock.Now()}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) dispatch() {
	if err := f.dispatcher.Dispatch(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func delivery(server *httptest.Server, harness *receiver, secret string) *application.ProposedInstanceDelivery {
	return &application.ProposedInstanceDelivery{Type: "webhook", URL: server.URL + "/aap/events", RegistrationID: domain.ID(harness.registrationID), SigningSecret: secret}
}

func TestAsynchronousDeliveryFromMintToAcknowledgedNotification(t *testing.T) {
	f := newFixture(t)
	harness, signingSecret := newReceiver(t, 1, true)
	server := httptest.NewServer(harness)
	defer server.Close()

	// A receiver that does not expect the registration refuses the
	// challenge, and no instance exists afterwards.
	stranger := delivery(server, harness, signingSecret)
	stranger.RegistrationID = domain.ID(uuid.NewString())
	if _, err := f.mint(authn.MintInstanceRequest{InstanceName: "stranger", Delivery: stranger}); !errors.Is(err, domain.ErrWebhookVerificationFailed) {
		t.Fatalf("an unexpected registration must fail verification, got %v", err)
	}
	unsupported := delivery(server, harness, signingSecret)
	unsupported.Type = "email"
	if _, err := f.mint(authn.MintInstanceRequest{Delivery: unsupported}); !errors.Is(err, domain.ErrUnsupportedDeliveryType) {
		t.Fatalf("expected unsupported delivery type, got %v", err)
	}
	unsafe := delivery(server, harness, signingSecret)
	unsafe.URL = "ftp://harness.example.com/aap"
	if _, err := f.mint(authn.MintInstanceRequest{Delivery: unsafe}); !errors.Is(err, domain.ErrInvalidWebhookURL) {
		t.Fatalf("expected invalid webhook URL, got %v", err)
	}

	minted, err := f.mint(authn.MintInstanceRequest{InstanceName: "async-runner", Delivery: delivery(server, harness, signingSecret)})
	if err != nil {
		t.Fatal(err)
	}
	if minted.AgentInstance.Delivery == nil || minted.AgentInstance.Delivery.URL != server.URL+"/aap/events" {
		t.Fatalf("minted instance does not report its receiver: %+v", minted.AgentInstance)
	}
	if len(harness.verifications) != 1 || harness.verifications[0]["instance_id"] != string(minted.AgentInstance.ID) {
		t.Fatalf("verification must name the minted instance: %+v", harness.verifications)
	}
	polling, err := f.mint(authn.MintInstanceRequest{InstanceName: "polling-runner"})
	if err != nil {
		t.Fatal(err)
	}
	asyncPrincipal := f.principal(minted.Credential)

	approvedInline := f.create(minted.Credential, "issue_refund")
	cancelled := f.create(minted.Credential, "pending")
	if _, err := f.approvals.Cancel(f.ctx, asyncPrincipal, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	decided := f.create(minted.Credential, "pending")
	f.decide(decided.ID, domain.ApprovalDenied)
	f.create(polling.Credential, "issue_refund")
	stillPending := f.create(minted.Credential, "pending")

	f.dispatch()
	if harness.notifications != 3 || len(harness.events) != 2 {
		t.Fatalf("first pass: %d attempts, %d acknowledged; want 3 and 2 with one 503", harness.notifications, len(harness.events))
	}
	f.clock.offset.Store(int64(2 * time.Hour))
	f.dispatch()
	f.dispatch()
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
		current, err := f.approvals.Get(f.ctx, asyncPrincipal, request.ID)
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

func TestProvisioningIsIdempotent(t *testing.T) {
	f := newFixture(t)
	harness, signingSecret := newReceiver(t, 1, false)
	server := httptest.NewServer(harness)
	defer server.Close()

	request := authn.MintInstanceRequest{IdempotencyKey: "fleet-runner-7", InstanceName: "runner-7", Metadata: json.RawMessage(`{"region":"eu","zone":1}`), Delivery: delivery(server, harness, signingSecret)}
	first, err := f.mint(request)
	if err != nil || first.Replayed {
		t.Fatalf("first: %+v, %v", first, err)
	}
	// The same input, spelled differently, is the same submission.
	request.Metadata = json.RawMessage(`{ "zone": 1, "region": "eu" }`)
	replay, err := f.mint(request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Credential.Token != first.Credential.Token || replay.AgentInstance.ID != first.AgentInstance.ID ||
		!replay.Credential.ExpiresAt.Equal(first.Credential.ExpiresAt) || replay.AgentInstance.Delivery == nil {
		t.Fatalf("a replay must return the original instance and credential: first %+v, replay %+v", first, replay)
	}
	if len(harness.verifications) != 1 {
		t.Fatalf("a replay must not verify the receiver again: %d verifications", len(harness.verifications))
	}
	f.principal(replay.Credential)
	changed := request
	changed.InstanceName = "runner-8"
	if _, err := f.mint(changed); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("a changed submission under the same key must conflict, got %v", err)
	}

	// A failed verification leaves the key free for a corrected retry,
	// which then creates the instance it reserved.
	unknownRegistration := domain.ID(uuid.NewString())
	failing := authn.MintInstanceRequest{IdempotencyKey: "fleet-runner-9", InstanceName: "runner-9", Delivery: delivery(server, harness, signingSecret)}
	failing.Delivery.RegistrationID = unknownRegistration
	if _, err := f.mint(failing); !errors.Is(err, domain.ErrWebhookVerificationFailed) {
		t.Fatalf("expected verification failure, got %v", err)
	}
	changedRegistration := failing
	changedRegistration.Delivery = delivery(server, harness, signingSecret)
	if _, err := f.mint(changedRegistration); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("another registration is a different submission under the same key, got %v", err)
	}
	harness.registrationID = string(unknownRegistration)
	recovered, err := f.mint(failing)
	if err != nil || recovered.Replayed {
		t.Fatalf("retrying the same submission after the receiver learns the registration: %+v, %v", recovered, err)
	}
}

func TestProvisionerManagesTheInstanceLifecycle(t *testing.T) {
	f := newFixture(t)
	oldHarness, oldSecret := newReceiver(t, 1, false)
	oldServer := httptest.NewServer(oldHarness)
	defer oldServer.Close()
	newHarness, newSecret := newReceiver(t, 100, false)
	newServer := httptest.NewServer(newHarness)
	defer newServer.Close()

	minted, err := f.mint(authn.MintInstanceRequest{InstanceName: "runner", Delivery: delivery(oldServer, oldHarness, oldSecret)})
	if err != nil {
		t.Fatal(err)
	}
	id := minted.AgentInstance.ID
	read, err := f.auth.GetProvisionedInstance(f.ctx, f.provisionerToken, id)
	if err != nil || read.Delivery == nil || read.Delivery.URL != oldServer.URL+"/aap/events" {
		t.Fatalf("read: %+v, %v", read, err)
	}

	// A queued event follows the receiver when it is replaced before
	// delivery, signed with the new secret.
	queued := f.create(minted.Credential, "issue_refund")
	name := "runner-renamed"
	replace := authn.UpdateInstanceRequest{Token: f.provisionerToken, IdempotencyKey: "replace-1", InstanceID: id, Name: &name,
		DeliveryChange: application.DeliveryReplaced, Delivery: delivery(newServer, newHarness, newSecret)}
	updated, err := f.auth.UpdateProvisionedInstance(f.ctx, replace)
	if err != nil || updated.Name != name || updated.Delivery == nil || updated.Delivery.URL != newServer.URL+"/aap/events" {
		t.Fatalf("update: %+v, %v", updated, err)
	}
	if len(newHarness.verifications) != 1 {
		t.Fatalf("the replacement must be verified: %d", len(newHarness.verifications))
	}
	f.dispatch()
	if oldHarness.acknowledged() != 0 || newHarness.acknowledged() != 1 {
		t.Fatalf("the queued event must go to the new receiver: old %d, new %d", oldHarness.acknowledged(), newHarness.acknowledged())
	}
	var event map[string]string
	for _, body := range newHarness.events {
		json.Unmarshal(body, &event)
	}
	if event["request_id"] != string(queued.ID) {
		t.Fatalf("unexpected event %v", event)
	}
	// Replaying the update answers the stored response without verifying
	// or applying anything again.
	again, err := f.auth.UpdateProvisionedInstance(f.ctx, replace)
	if err != nil || again.Name != name || len(newHarness.verifications) != 1 {
		t.Fatalf("replay: %+v, %v, %d verifications", again, err, len(newHarness.verifications))
	}
	// A failed replacement leaves the name and receiver as they were.
	other := "never-applied"
	failing := authn.UpdateInstanceRequest{Token: f.provisionerToken, IdempotencyKey: "replace-2", InstanceID: id, Name: &other,
		DeliveryChange: application.DeliveryReplaced, Delivery: delivery(oldServer, oldHarness, oldSecret)}
	failing.Delivery.RegistrationID = domain.ID(uuid.NewString())
	if _, err := f.auth.UpdateProvisionedInstance(f.ctx, failing); !errors.Is(err, domain.ErrWebhookVerificationFailed) {
		t.Fatalf("expected verification failure, got %v", err)
	}
	if read, _ := f.auth.GetProvisionedInstance(f.ctx, f.provisionerToken, id); read.Name != name || read.Delivery.URL != newServer.URL+"/aap/events" {
		t.Fatalf("a failed update changed the instance: %+v", read)
	}

	// Disabling the instance pauses delivery; enabling it resumes.
	paused := f.create(minted.Credential, "issue_refund")
	if _, err := f.store.SetAgentInstanceStatus(f.ctx, application.SetAgentInstanceStatusInput{OrganizationID: f.org, InstanceID: id, Status: domain.AgentDisabled, ActorID: f.member, Now: f.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	f.dispatch()
	if newHarness.acknowledged() != 1 {
		t.Fatal("a disabled instance must not be notified")
	}
	if _, err := f.store.SetAgentInstanceStatus(f.ctx, application.SetAgentInstanceStatusInput{OrganizationID: f.org, InstanceID: id, Status: domain.AgentActive, ActorID: f.member, Now: f.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	f.dispatch()
	if newHarness.acknowledged() != 2 {
		t.Fatalf("delivery must resume for request %s once the instance is enabled", paused.ID)
	}

	// Removing the receiver abandons what is still queued.
	pending := f.create(minted.Credential, "pending")
	f.decide(pending.ID, domain.ApprovalApproved)
	removed, err := f.auth.UpdateProvisionedInstance(f.ctx, authn.UpdateInstanceRequest{Token: f.provisionerToken, IdempotencyKey: "remove-1", InstanceID: id, DeliveryChange: application.DeliveryRemoved})
	if err != nil || removed.Delivery != nil {
		t.Fatalf("remove: %+v, %v", removed, err)
	}
	f.dispatch()
	if newHarness.acknowledged() != 2 {
		t.Fatal("an event queued before the receiver was removed must not be delivered")
	}

	// Deletion cancels pending requests without notifications, revokes
	// the credential, and is repeatable.
	if _, err := f.auth.UpdateProvisionedInstance(f.ctx, authn.UpdateInstanceRequest{Token: f.provisionerToken, IdempotencyKey: "readd-1", InstanceID: id,
		DeliveryChange: application.DeliveryReplaced, Delivery: delivery(newServer, newHarness, newSecret)}); err != nil {
		t.Fatal(err)
	}
	orphan := f.create(minted.Credential, "pending")
	_, cancelled, err := f.auth.DeleteProvisionedInstance(f.ctx, f.provisionerToken, id)
	if err != nil || len(cancelled) != 1 || cancelled[0] != orphan.ID {
		t.Fatalf("delete: cancelled %v, %v", cancelled, err)
	}
	if current, err := f.store.GetApproval(f.ctx, application.GetApprovalInput{OrganizationID: f.org, RequestID: orphan.ID}); err != nil || current.Status != domain.ApprovalCancelled {
		t.Fatalf("pending request after deletion: %+v, %v", current.Status, err)
	}
	f.dispatch()
	if newHarness.acknowledged() != 2 {
		t.Fatal("cancellations caused by deletion must not notify")
	}
	if _, err := f.auth.ResolveCredential(f.ctx, minted.Credential.Token); err == nil {
		t.Fatal("a deleted instance's credential must be revoked")
	}
	if _, _, err := f.auth.DeleteProvisionedInstance(f.ctx, f.provisionerToken, id); err != nil {
		t.Fatalf("deleting again must succeed, got %v", err)
	}
	if _, err := f.auth.GetProvisionedInstance(f.ctx, f.provisionerToken, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a deleted instance must read as absent, got %v", err)
	}
	if _, err := f.auth.UpdateProvisionedInstance(f.ctx, authn.UpdateInstanceRequest{Token: f.provisionerToken, IdempotencyKey: "late", InstanceID: id, Name: &name}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("updating a deleted instance must be not found, got %v", err)
	}
	if _, err := f.store.SetAgentInstanceStatus(f.ctx, application.SetAgentInstanceStatusInput{OrganizationID: f.org, InstanceID: id, Status: domain.AgentActive, ActorID: f.member, Now: f.clock.Now()}); !errors.Is(err, domain.ErrInstanceDeleted) {
		t.Fatalf("a deleted instance must not be re-enabled, got %v", err)
	}
	replay := authn.MintInstanceRequest{IdempotencyKey: "deleted-replay", InstanceName: "short-lived"}
	shortLived, err := f.mint(replay)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.auth.DeleteProvisionedInstance(f.ctx, f.provisionerToken, shortLived.AgentInstance.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mint(replay); !errors.Is(err, domain.ErrInstanceDeleted) {
		t.Fatalf("replaying the provisioning of a deleted instance must answer instance_deleted, got %v", err)
	}
}
