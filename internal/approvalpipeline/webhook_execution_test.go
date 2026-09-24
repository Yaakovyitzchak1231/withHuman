package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

// webhookFixture activates an organization pipeline of a CEL deny, a
// webhook block, and a CEL block that thresholds the webhook's metadata,
// then hands back an executor bound to the given deliverer.
func webhookFixture(t *testing.T, now time.Time, deliverer *fakeDeliverer, allowed ...approvalblock.Outcome) (*ExecutionService, *memoryPipelineRepository) {
	t.Helper()
	repository := newMemoryPipelineRepository()
	endpoints := &fakeEndpoints{endpoint: domain.WebhookEndpointRevision{ID: testEndpointRevisionID, EndpointKey: testEndpointKey, Revision: 1, IsActive: true, OrganizationID: "organization-1", Name: "fraud-service", URL: "https://fraud.example.com/hooks/approval"}, secret: "whsec_test"}
	runtime := WebhookRuntime{Endpoints: endpoints, Deliverer: deliverer, Backoff: func(int) time.Duration { return 0 }}
	registry := NewBlockRegistry(WithWebhooks(runtime), WithClock(fixedPipelineClock{now: now}))
	management := NewManagementService(repository, NewCompiledPipelineState(repository, registry), fixedPipelineClock{now: now})
	options := []approvalblock.Option{}
	if allowed != nil {
		options = append(options, approvalblock.AllowedOutcomes(allowed...))
	}
	definitions := []approvalblock.Definition{
		pipelineDefinition("deny-other", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("other")),
		webhookDefinition(options...),
		pipelineDefinition("approve-low-risk", approvalblock.Approve, approvalblock.Field("/previous_metadata/organization/fraud-check/risk_score").LessThan(50)),
	}
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "", definitions, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), "", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	compiled := NewCompiledPipelineState(repository, registry)
	if err := compiled.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	agents := executionAgentRepositoryStub{agent: domain.Agent{ID: "agent-1", OrganizationID: "organization-1", Name: "billing-agent", AgentSlug: "billing"}}
	return NewExecutionService(repository, agents, compiled, fixedPipelineClock{now: now.Add(time.Second)}), repository
}

func webhookRequest(now time.Time) domain.ApprovalRequest {
	return domain.ApprovalRequest{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", AgentID: "agent-1", AgentInstanceID: "instance-1",
		AgentSlug: "billing", Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount_cents":900}`),
		IdempotencyKey: "key-1", SemanticHash: "hash", Status: domain.ApprovalPending,
		DeadlineAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
}

func TestWebhookBlockSuspendsThenEnrichesTheFollowingCELBlock(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	deliverer := &fakeDeliverer{script: []scriptedDelivery{ok(`{"outcome":"next","reason":"low risk","metadata":{"risk_score":12}}`)}}
	service, _ := webhookFixture(t, now, deliverer)
	request := webhookRequest(now)

	initialization, err := service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if initialization.Suspension == nil || initialization.Suspension.ResumePosition != 1 || initialization.Decision != nil {
		t.Fatalf("webhook block did not suspend the synchronous segment: %+v", initialization)
	}
	if len(deliverer.deliveries) != 0 {
		t.Fatal("the request path delivered a webhook")
	}
	if types := eventTypes(initialization.AuditEvents); !slices.Contains(types, "request_approval_pipeline.suspended") || slices.Contains(types, "request_approval_pipeline.completed") {
		t.Fatalf("unexpected events %v", types)
	}

	resumed, err := service.Resume(context.Background(), request, *initialization.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Decision == nil || resumed.Decision.Status != domain.ApprovalApproved {
		t.Fatalf("CEL block after the webhook did not approve on its metadata: %+v", resumed.Decision)
	}
	if len(deliverer.deliveries) != 1 || deliverer.deliveries[0].IdempotencyKey != string(request.ID)+":fraud-check" {
		t.Fatalf("delivery: %+v", deliverer.deliveries)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliverer.deliveries[0].Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["pipeline"].(map[string]any)["evaluation_id"] != string(initialization.Suspension.EvaluationID) || payload["preview"] != false {
		t.Fatalf("delivery payload did not carry the evaluation: %v", payload)
	}
	var evaluated []map[string]any
	for _, event := range resumed.AuditEvents {
		if event.EventType == "request_approval_block.evaluated" {
			var data map[string]any
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatal(err)
			}
			evaluated = append(evaluated, data)
		}
	}
	if len(evaluated) != 2 || evaluated[0]["block_type"] != "webhook" || evaluated[1]["block_key"] != "approve-low-risk" {
		t.Fatalf("resume evidence: %v", evaluated)
	}
	delivery := evaluated[0]["delivery"].(map[string]any)
	if delivery["endpoint_key"] != testEndpointKey || delivery["endpoint_revision"] != float64(1) || delivery["endpoint_host"] != "fraud.example.com" || delivery["http_status"] != float64(200) || delivery["reason"] != "low risk" {
		t.Fatalf("delivery evidence: %v", delivery)
	}
}

func TestWebhookBlockFailureHandsTheRequestToAHumanWithRetriesExhausted(t *testing.T) {
	now := time.Unix(11_000, 0).UTC()
	deliverer := &fakeDeliverer{script: []scriptedDelivery{status(503), status(503), status(503)}}
	service, _ := webhookFixture(t, now, deliverer)
	request := webhookRequest(now)
	initialization, err := service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := service.Resume(context.Background(), request, *initialization.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Decision != nil {
		t.Fatalf("a failed webhook decided the request: %+v", resumed.Decision)
	}
	var completed map[string]any
	for _, event := range resumed.AuditEvents {
		if event.EventType == "request_approval_pipeline.completed" {
			if err := json.Unmarshal(event.Data, &completed); err != nil {
				t.Fatal(err)
			}
		}
	}
	if completed["outcome"] != "human" || completed["reason_code"] != "retries_exhausted" || completed["terminal_block_key"] != "fraud-check" || completed["total_attempts"] != float64(4) {
		t.Fatalf("completion: %v", completed)
	}
}

func TestWebhookBlockCannotDecideBeyondItsAllowedOutcomes(t *testing.T) {
	now := time.Unix(12_000, 0).UTC()
	deliverer := &fakeDeliverer{script: []scriptedDelivery{ok(`{"outcome":"approve"}`)}}
	service, _ := webhookFixture(t, now, deliverer, approvalblock.Next, approvalblock.Human)
	request := webhookRequest(now)
	initialization, err := service.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := service.Resume(context.Background(), request, *initialization.Suspension)
	if err != nil || resumed.Decision != nil {
		t.Fatalf("disallowed approve decided the request: %+v %v", resumed.Decision, err)
	}
	for _, event := range resumed.AuditEvents {
		if event.EventType == "request_approval_block.evaluated" {
			var data map[string]any
			_ = json.Unmarshal(event.Data, &data)
			if data["error_code"] != "outcome_not_allowed" {
				t.Fatalf("evidence: %v", data)
			}
		}
	}
}

func TestManagementRejectsWebhookBlocksWhoseEndpointDoesNotExist(t *testing.T) {
	now := time.Unix(13_000, 0).UTC()
	repository := newMemoryPipelineRepository()
	endpoints := &fakeEndpoints{endpoint: domain.WebhookEndpointRevision{ID: testEndpointRevisionID, EndpointKey: testEndpointKey, Revision: 1, IsActive: true, OrganizationID: "organization-1"}, missing: true}
	registry := NewBlockRegistry(WithWebhooks(WebhookRuntime{Endpoints: endpoints, Deliverer: &fakeDeliverer{}}))
	service := NewManagementService(repository, NewCompiledPipelineState(repository, registry), fixedPipelineClock{now: now})
	_, err := service.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "", []approvalblock.Definition{webhookDefinition()}, "")
	if !errors.Is(err, domain.ErrWebhookEndpointNotFound) {
		t.Fatalf("missing endpoint accepted: %v", err)
	}
	if repository.createCalls != 0 {
		t.Fatal("revision was persisted despite the missing endpoint")
	}
}

func TestPreviewNamespacesMetadataByScopeAndSeedsPreviousMetadata(t *testing.T) {
	now := time.Unix(14_000, 0).UTC()
	repository := newMemoryPipelineRepository()
	deliverer := &fakeDeliverer{script: []scriptedDelivery{ok(`{"outcome":"next","metadata":{"risk_score":80}}`)}}
	endpoints := &fakeEndpoints{endpoint: domain.WebhookEndpointRevision{ID: testEndpointRevisionID, EndpointKey: testEndpointKey, Revision: 1, IsActive: true, OrganizationID: "organization-1", URL: "https://fraud.example.com/x"}, secret: "s"}
	registry := NewBlockRegistry(WithWebhooks(WebhookRuntime{Endpoints: endpoints, Deliverer: deliverer, Backoff: func(int) time.Duration { return 0 }}))
	service := NewManagementService(repository, NewCompiledPipelineState(repository, registry), fixedPipelineClock{now: now})
	definitions := []approvalblock.Definition{
		webhookDefinition(),
		pipelineDefinition("deny-high-risk", approvalblock.Deny, approvalblock.Field("/previous_metadata/agent/fraud-check/risk_score").GreaterThan(50)),
		pipelineDefinition("approve-verified", approvalblock.Approve, approvalblock.Field("/previous_metadata/organization/verifier/verified").IsTrue()),
	}
	sample := PreviewSample{
		Request: BlockRequest{Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount_cents":900}`)},
		Agent:   BlockAgent{Slug: "billing", Name: "billing-agent"},
	}
	result, err := service.Preview(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", definitions, "", sample)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BlockDeny || result.TerminalBlockKey != "deny-high-risk" {
		t.Fatalf("agent preview did not namespace the webhook metadata under agent: %+v", result)
	}
	if len(deliverer.deliveries) != 1 {
		t.Fatalf("preview delivered %d times", len(deliverer.deliveries))
	}
	var payload map[string]any
	_ = json.Unmarshal(deliverer.deliveries[0].Body, &payload)
	if payload["preview"] != true {
		t.Fatal("preview delivery was not flagged")
	}

	deliverer.script = []scriptedDelivery{ok(`{"outcome":"next","metadata":{"risk_score":10}}`)}
	sample.PreviousMetadata = map[string]json.RawMessage{"organization/verifier": json.RawMessage(`{"verified":true}`)}
	result, err = service.Preview(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", definitions, "", sample)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BlockApprove || result.TerminalBlockKey != "approve-verified" {
		t.Fatalf("seeded previous metadata was not visible: %+v", result)
	}
	sample.PreviousMetadata = map[string]json.RawMessage{"verifier": json.RawMessage(`{}`)}
	if _, err := service.Preview(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", definitions, "", sample); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unscoped previous metadata accepted: %v", err)
	}
}
