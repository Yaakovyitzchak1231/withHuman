package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/webhookdelivery"
)

const (
	testEndpointKey        = "fraud-service"
	testEndpointRevisionID = "3f4d0d0c-2a63-4f1e-9d3b-2f1c0b4b7a11"
)

// fakeEndpoints holds one endpoint key with one revision. missing means
// the key has no revision at all; inactive means it exists but no
// revision is active.
type fakeEndpoints struct {
	endpoint domain.WebhookEndpointRevision
	secret   string
	missing  bool
	inactive bool
	openErr  error
}

func (f *fakeEndpoints) ResolveWebhookEndpoints(_ context.Context, input application.ResolveWebhookEndpointsInput) (application.ResolvedWebhookEndpoints, error) {
	resolved := application.ResolvedWebhookEndpoints{Missing: []string{}, Inactive: []string{}}
	for _, key := range input.EndpointKeys {
		switch {
		case f.missing || key != f.endpoint.EndpointKey || input.OrganizationID != f.endpoint.OrganizationID:
			resolved.Missing = append(resolved.Missing, key)
		case f.inactive:
			resolved.Inactive = append(resolved.Inactive, key)
		}
	}
	return resolved, nil
}

func (f *fakeEndpoints) GetActiveWebhookEndpointRevision(_ context.Context, input application.GetActiveWebhookEndpointRevisionInput) (domain.WebhookEndpointRevision, error) {
	if f.missing || f.inactive || input.EndpointKey != f.endpoint.EndpointKey || input.OrganizationID != f.endpoint.OrganizationID {
		return domain.WebhookEndpointRevision{}, domain.ErrNotFound
	}
	return f.endpoint, nil
}

func (f *fakeEndpoints) OpenWebhookSigningSecret(context.Context, application.OpenWebhookSigningSecretInput) (string, error) {
	if f.openErr != nil {
		return "", f.openErr
	}
	return f.secret, nil
}

type scriptedDelivery struct {
	response webhookdelivery.Response
	err      error
}

type fakeDeliverer struct {
	script     []scriptedDelivery
	deliveries []webhookdelivery.Delivery
}

func (f *fakeDeliverer) Deliver(_ context.Context, delivery webhookdelivery.Delivery) (webhookdelivery.Response, error) {
	f.deliveries = append(f.deliveries, delivery)
	if len(f.script) == 0 {
		return webhookdelivery.Response{StatusCode: http.StatusOK, Body: []byte(`{"outcome":"next"}`)}, nil
	}
	next := f.script[0]
	f.script = f.script[1:]
	return next.response, next.err
}

func ok(body string) scriptedDelivery {
	return scriptedDelivery{response: webhookdelivery.Response{StatusCode: http.StatusOK, Body: []byte(body), Duration: 40 * time.Millisecond}}
}

func status(code int) scriptedDelivery {
	return scriptedDelivery{response: webhookdelivery.Response{StatusCode: code, Duration: 10 * time.Millisecond}}
}

func webhookDefinition(options ...approvalblock.Option) approvalblock.Definition {
	base := []approvalblock.Option{
		approvalblock.Name("Fraud service review"),
		approvalblock.Endpoint(testEndpointKey),
		approvalblock.Reason("Fraud service reviewed the request."),
	}
	return *approvalblock.DefineWebhook("fraud-check", append(base, options...)...)
}

func webhookInput() RequestApprovalBlockInput {
	input := scriptedInput()
	input.EvaluationID = "evaluation-1"
	input.Request.ID = "request-1"
	input.Request.CreatedAt = time.Unix(1_000, 0).UTC()
	input.Request.DeadlineAt = time.Unix(2_000, 0).UTC()
	return input
}

func compileWebhook(t *testing.T, definition approvalblock.Definition, endpoints *fakeEndpoints, deliverer *fakeDeliverer, entitlements application.Entitlements) *CompiledWebhookBlock {
	t.Helper()
	if endpoints == nil {
		endpoints = &fakeEndpoints{endpoint: domain.WebhookEndpointRevision{ID: testEndpointRevisionID, EndpointKey: testEndpointKey, Revision: 1, IsActive: true, OrganizationID: "organization-1", Name: "fraud-service", URL: "https://fraud.example.com/hooks/approval"}, secret: "whsec_test"}
	}
	block, err := CompileWebhookBlock(definition, &WebhookRuntime{Endpoints: endpoints, Deliverer: deliverer, Backoff: func(int) time.Duration { return 0 }},
		entitlements, fixedPipelineClock{now: time.Unix(5_000, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return block
}

func TestWebhookBlockMapsAnswersAndFailuresPerTheContract(t *testing.T) {
	cases := []struct {
		name     string
		allowed  []approvalblock.Outcome
		script   []scriptedDelivery
		outcome  BlockOutcome
		code     BlockErrorCode
		attempts int
		status   int
	}{
		{"next with metadata", nil, []scriptedDelivery{ok(`{"outcome":"next","reason":"low risk","metadata":{"risk_score":12}}`)}, BlockNext, "", 1, 200},
		{"human", nil, []scriptedDelivery{ok(`{"outcome":"human"}`)}, BlockHuman, "", 1, 200},
		{"approve when allowed", []approvalblock.Outcome{approvalblock.Approve, approvalblock.Next}, []scriptedDelivery{ok(`{"outcome":"approve"}`)}, BlockApprove, "", 1, 200},
		{"approve when not allowed fails closed", nil, []scriptedDelivery{ok(`{"outcome":"approve"}`)}, BlockHuman, ErrorOutcomeNotAllowed, 1, 200},
		{"5xx retries then succeeds", nil, []scriptedDelivery{status(503), ok(`{"outcome":"next"}`)}, BlockNext, "", 2, 200},
		{"5xx exhausts the budget", nil, []scriptedDelivery{status(500), status(502), status(503)}, BlockHuman, ErrorEndpointError, 3, 503},
		{"429 retries", nil, []scriptedDelivery{status(429), ok(`{"outcome":"next"}`)}, BlockNext, "", 2, 200},
		{"4xx does not retry", nil, []scriptedDelivery{status(404)}, BlockHuman, ErrorEndpointRejected, 1, 404},
		{"3xx is rejected", nil, []scriptedDelivery{status(302)}, BlockHuman, ErrorEndpointRejected, 1, 302},
		{"transport error retries", nil, []scriptedDelivery{{err: errors.New("connection refused")}, {err: errors.New("connection refused")}, {err: errors.New("connection refused")}}, BlockHuman, ErrorEndpointUnreachable, 3, 0},
		{"timeout", nil, []scriptedDelivery{{err: context.DeadlineExceeded}, {err: context.DeadlineExceeded}, {err: context.DeadlineExceeded}}, BlockHuman, ErrorTimeout, 3, 0},
		{"malformed answer does not retry", nil, []scriptedDelivery{ok(`{"outcome":"next","surprise":true}`)}, BlockHuman, ErrorInvalidBlockOutput, 1, 200},
		{"oversize answer", nil, []scriptedDelivery{{response: webhookdelivery.Response{StatusCode: 200}, err: webhookdelivery.ErrResponseTooLarge}}, BlockHuman, ErrorInvalidBlockOutput, 1, 200},
		{"blocked destination", nil, []scriptedDelivery{{err: webhookdelivery.ErrDestinationBlocked}}, BlockHuman, ErrorEndpointUnavailable, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := []approvalblock.Option{}
			if tc.allowed != nil {
				options = append(options, approvalblock.AllowedOutcomes(tc.allowed...))
			}
			deliverer := &fakeDeliverer{script: tc.script}
			usage := &recordingEntitlements{}
			block := compileWebhook(t, webhookDefinition(options...), nil, deliverer, usage)
			result := ExecuteRequestApprovalBlock(context.Background(), block, webhookInput())
			if result.Outcome != tc.outcome || result.ErrorCode != tc.code {
				t.Fatalf("got outcome=%s code=%q, want %s %q", result.Outcome, result.ErrorCode, tc.outcome, tc.code)
			}
			if len(deliverer.deliveries) != tc.attempts || attemptCount(result) != tc.attempts {
				t.Fatalf("attempts: deliveries=%d reported=%d want %d", len(deliverer.deliveries), attemptCount(result), tc.attempts)
			}
			if len(usage.usage) != tc.attempts || usage.usage[0].Dimension != application.DimensionWebhookDeliveries {
				t.Fatalf("usage not metered per attempt: %+v", usage.usage)
			}
			for _, record := range usage.usage {
				if err := record.Validate(); err != nil {
					t.Fatalf("usage record is not valid: %v", err)
				}
			}
			delivery := result.Evidence.Delivery
			if delivery == nil || delivery.EndpointKey != testEndpointKey || delivery.EndpointRevision != 1 || delivery.EndpointHost != "fraud.example.com" || delivery.HTTPStatus != tc.status {
				t.Fatalf("delivery evidence: %+v", delivery)
			}
			if tc.name == "next with metadata" {
				if string(result.Metadata["risk_score"]) != "12" || delivery.Reason != "low risk" || result.Reason != "Fraud service reviewed the request." {
					t.Fatalf("metadata and reasons: %+v %+v", result, delivery)
				}
			}
		})
	}
}

func TestWebhookBlockBuildsTheDeliveryPayload(t *testing.T) {
	deliverer := &fakeDeliverer{}
	definition := webhookDefinition(approvalblock.Sending(approvalblock.WebhookInclude{AgentReasoning: true, Context: true, PreviousMetadata: true}))
	block := compileWebhook(t, definition, nil, deliverer, nil)
	if result := ExecuteRequestApprovalBlock(context.Background(), block, webhookInput()); result.Outcome != BlockNext {
		t.Fatalf("unexpected result %+v", result)
	}
	delivery := deliverer.deliveries[0]
	if delivery.URL != "https://fraud.example.com/hooks/approval" || delivery.Secret != "whsec_test" || delivery.Timeout != 5*time.Second || delivery.IdempotencyKey != "request-1:fraud-check" {
		t.Fatalf("delivery envelope: %+v", delivery)
	}
	var payload map[string]any
	if err := json.Unmarshal(delivery.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["version"] != webhookdelivery.PayloadVersion || payload["type"] != webhookdelivery.TypeBlockDelivery || payload["preview"] != false ||
		payload["idempotency_key"] != "request-1:fraud-check" || payload["delivery_id"] != delivery.DeliveryID || payload["organization_id"] != "organization-1" {
		t.Fatalf("payload envelope: %v", payload)
	}
	request := payload["request"].(map[string]any)
	if request["id"] != "request-1" || request["tool"] != "stripe.refund" || request["agent_reasoning"] != "The customer was charged twice." ||
		request["created_at"] != "1970-01-01T00:16:40Z" || request["deadline_at"] != "1970-01-01T00:33:20Z" {
		t.Fatalf("payload request: %v", request)
	}
	if request["context"].(map[string]any)["run_id"] != "4821" {
		t.Fatalf("payload context: %v", request["context"])
	}
	previous := payload["previous_metadata"].(map[string]any)["organization"].(map[string]any)["risk"].(map[string]any)
	if previous["risk"] != "low" {
		t.Fatalf("payload previous metadata: %v", payload["previous_metadata"])
	}
	block2 := payload["block"].(map[string]any)
	if block2["id"] != "fraud-check" || block2["attempt"] != float64(1) || block2["max_attempts"] != float64(3) {
		t.Fatalf("payload block: %v", block2)
	}

	// Default include: no reasoning, no context, previous metadata only.
	deliverer = &fakeDeliverer{}
	block = compileWebhook(t, webhookDefinition(), nil, deliverer, nil)
	input := webhookInput()
	input.Preview, input.Request.ID = true, ""
	ExecuteRequestApprovalBlock(context.Background(), block, input)
	if err := json.Unmarshal(deliverer.deliveries[0].Body, &payload); err != nil {
		t.Fatal(err)
	}
	request = payload["request"].(map[string]any)
	if _, has := request["agent_reasoning"]; has {
		t.Fatal("agent reasoning sent without include")
	}
	if _, has := request["context"]; has {
		t.Fatal("context sent without include")
	}
	if payload["preview"] != true || payload["idempotency_key"].(string)[:8] != "preview:" || payload["pipeline"].(map[string]any)["evaluation_id"] != "preview" {
		t.Fatalf("preview envelope: %v", payload)
	}
}

func TestWebhookBlockFailsClosedWhenTheEndpointIsUnavailable(t *testing.T) {
	deliverer := &fakeDeliverer{}
	missing := &fakeEndpoints{endpoint: domain.WebhookEndpointRevision{ID: testEndpointRevisionID, EndpointKey: testEndpointKey, Revision: 1, IsActive: true, OrganizationID: "organization-1"}, missing: true}
	result := ExecuteRequestApprovalBlock(context.Background(), compileWebhook(t, webhookDefinition(), missing, deliverer, nil), webhookInput())
	if result.Outcome != BlockHuman || result.ErrorCode != ErrorEndpointUnavailable || len(deliverer.deliveries) != 0 {
		t.Fatalf("missing endpoint: %+v", result)
	}
	sealed := &fakeEndpoints{endpoint: domain.WebhookEndpointRevision{ID: testEndpointRevisionID, EndpointKey: testEndpointKey, Revision: 1, IsActive: true, OrganizationID: "organization-1", URL: "https://fraud.example.com/x"}, openErr: errors.New("sealed value did not open")}
	result = ExecuteRequestApprovalBlock(context.Background(), compileWebhook(t, webhookDefinition(), sealed, deliverer, nil), webhookInput())
	if result.Outcome != BlockHuman || result.ErrorCode != ErrorEndpointUnavailable || result.Evidence.Delivery == nil {
		t.Fatalf("unopenable secret: %+v", result)
	}
	budget := &BlockExecutionBudget{}
	input := webhookInput()
	input.Budget = budget
	block := compileWebhook(t, webhookDefinition(), nil, &fakeDeliverer{}, nil)
	for index := 0; index < approvalblock.MaximumWebhookBlocksPerRequest; index++ {
		ExecuteRequestApprovalBlock(context.Background(), block, input)
	}
	if result := ExecuteRequestApprovalBlock(context.Background(), block, input); result.ErrorCode != ErrorResourceLimit {
		t.Fatalf("sixth webhook block ran: %+v", result)
	}
}

func TestRegistryRequiresTheWebhookRuntimeToCompile(t *testing.T) {
	definitions := []approvalblock.Definition{webhookDefinition()}
	if _, err := NewBlockRegistry().compileDefinitions(definitions); !errors.Is(err, domain.ErrWebhookUnconfigured) {
		t.Fatalf("unconfigured registry compiled a webhook block: %v", err)
	}
	registry := NewBlockRegistry(WithWebhooks(WebhookRuntime{Endpoints: &fakeEndpoints{}, Deliverer: &fakeDeliverer{}}))
	compiled, err := registry.compileDefinitions(definitions)
	if err != nil || len(compiled) != 1 {
		t.Fatalf("configured registry failed: %v", err)
	}
	if runtime, _ := registry.runtime(approvalblock.TypeWebhook); !runtime.Asynchronous {
		t.Fatal("webhook runtime must be asynchronous")
	}
}

// An endpoint key without an active revision (archived after the pipeline
// was activated, or never activated) is an unavailable destination: the
// block falls closed to human and the evidence still names the key.
func TestWebhookBlockWithoutActiveEndpointRevisionFallsClosed(t *testing.T) {
	deliverer := &fakeDeliverer{}
	endpoints := &fakeEndpoints{endpoint: domain.WebhookEndpointRevision{ID: testEndpointRevisionID, EndpointKey: testEndpointKey, OrganizationID: "organization-1", URL: "https://fraud.example.com/x"}, secret: "s", inactive: true}
	block := compileWebhook(t, webhookDefinition(), endpoints, deliverer, &recordingEntitlements{})
	result := ExecuteRequestApprovalBlock(context.Background(), block, webhookInput())
	if result.Outcome != BlockHuman || result.ErrorCode != ErrorEndpointUnavailable {
		t.Fatalf("got outcome=%s code=%q, want human endpoint_unavailable", result.Outcome, result.ErrorCode)
	}
	if len(deliverer.deliveries) != 0 {
		t.Fatalf("delivered %d times to an archived endpoint", len(deliverer.deliveries))
	}
	if result.Evidence.Delivery == nil || result.Evidence.Delivery.EndpointKey != testEndpointKey || result.Evidence.Delivery.EndpointRevision != 0 {
		t.Fatalf("evidence: %+v", result.Evidence.Delivery)
	}
}
