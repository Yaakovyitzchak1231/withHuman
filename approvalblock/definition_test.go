package approvalblock_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/approvalblock"
)

const (
	rootID   = "21aa670a-734c-454c-a1e9-653d54be054b"
	toolID   = "5d79dc6a-5f7d-4e70-aeb1-dcd682e77322"
	amountID = "833f08e6-c7b4-47c6-927b-1d27a93cd957"
)

func TestDSLProducesCanonicalCELBlockDocument(t *testing.T) {
	block := approvalblock.Define(
		"small-refund",
		approvalblock.Name("Approve small refunds"),
		approvalblock.OnMatch(approvalblock.Approve),
		approvalblock.When(approvalblock.ConditionID(rootID, approvalblock.All(
			approvalblock.ConditionID(toolID, approvalblock.Field("/request/tool").In("stripe.refund", "stripe.partial_refund")),
			approvalblock.ConditionID(amountID, approvalblock.Field("/request/arguments/amount_cents").LessThan(1000)),
		))),
		approvalblock.Reason("Small refunds are pre-authorized."),
	)

	document, err := block.JSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := approvalblock.Parse(document)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ID != "small-refund" || parsed.Type != approvalblock.TypeCEL || !parsed.Enabled {
		t.Fatalf("unexpected block envelope: %+v", parsed)
	}
	if parsed.Timeout != "50ms" || parsed.MaxAttempts != 1 || parsed.CEL.OnMatch != approvalblock.Approve {
		t.Fatalf("unexpected CEL defaults/config: %+v", parsed)
	}
	if len(parsed.CEL.When.All) != 2 || parsed.CEL.When.All[0].ID != toolID {
		t.Fatalf("condition tree did not round trip: %+v", parsed.CEL.When)
	}
	var generic map[string]any
	if err := json.Unmarshal(document, &generic); err != nil {
		t.Fatal(err)
	}
	if generic["timeout"] != "50ms" {
		t.Fatalf("timeout is not encoded as a duration string: %s", document)
	}
}

func TestValidationRejectsProhibitedAndUntrustedFields(t *testing.T) {
	for _, path := range []string{
		"/previous_metadata/risk",
		"/request/agent_reasoning",
		"/request/context/run_id",
		"/request/timeout",
		"/agent/credential",
	} {
		t.Run(path, func(t *testing.T) {
			block := validBlock(approvalblock.Field(path).Equals("trusted"))
			if err := block.Validate(); err == nil || !strings.Contains(err.Error(), "not available") {
				t.Fatalf("got %v, want prohibited-field error", err)
			}
		})
	}
}

func TestValidationTypeChecksKnownFieldsAndConfiguredValues(t *testing.T) {
	tests := []struct {
		name      string
		condition approvalblock.Condition
	}{
		{name: "numeric known string", condition: approvalblock.Field("/request/tool").LessThan(2)},
		{name: "boolean known string", condition: approvalblock.Field("/agent/slug").IsTrue()},
		{name: "non-string set on known string", condition: approvalblock.Field("/agent/name").ContainsAll(1)},
		{name: "wrong numeric comparison", condition: approvalblock.Field("/request/arguments/amount").GreaterThan("ten")},
		{name: "wrong in comparison", condition: rawLeaf(approvalblock.In, `42`)},
		{name: "single prefix comparison", condition: rawLeaf(approvalblock.StartsWith, `"stripe."`)},
		{name: "wrong regex comparison", condition: rawLeaf(approvalblock.Regex, `42`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validBlock(test.condition).Validate(); err == nil {
				t.Fatal("validation unexpectedly succeeded")
			}
		})
	}
}

func TestValidationAcceptsSubstringSetsOnKnownStringFields(t *testing.T) {
	for _, condition := range []approvalblock.Condition{
		approvalblock.Field("/request/tool").ContainsAny("deploy", "delete"),
		approvalblock.Field("/agent/name").ContainsAll("prod", "worker"),
	} {
		if err := validBlock(condition).Validate(); err != nil {
			t.Fatalf("string set operator rejected on a known string field: %v", err)
		}
	}
}

func TestValidationAcceptsTheMCPServerAsAKnownStringField(t *testing.T) {
	for _, condition := range []approvalblock.Condition{
		approvalblock.Field("/request/server").Equals("stripe"),
		approvalblock.Field("/request/server").NotExists(),
	} {
		if err := validBlock(condition).Validate(); err != nil {
			t.Fatalf("block condition on the MCP server rejected: %v", err)
		}
		// Escalation path request conditions validate through the same entry.
		if err := approvalblock.ValidateCondition(condition); err != nil {
			t.Fatalf("request condition on the MCP server rejected: %v", err)
		}
	}
	err := approvalblock.ValidateCondition(approvalblock.Field("/request/server").GreaterThan(1))
	if err == nil || !strings.Contains(err.Error(), "known string") {
		t.Fatalf("got %v, want the numeric comparison refused on a known string field", err)
	}
}

func TestValidationEnforcesAuthoringResourceLimits(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		condition := approvalblock.Field("/request/tool").Equals("deploy")
		for range approvalblock.MaximumConditionDepth {
			condition = approvalblock.All(condition)
		}
		if err := validBlock(condition).Validate(); err == nil || !strings.Contains(err.Error(), "depth") {
			t.Fatalf("got %v, want depth error", err)
		}
	})

	t.Run("nodes", func(t *testing.T) {
		conditions := make([]approvalblock.Condition, approvalblock.MaximumConditionNodes)
		for index := range conditions {
			conditions[index] = approvalblock.Field("/request/tool").Equals("deploy")
		}
		if err := validBlock(approvalblock.All(conditions...)).Validate(); err == nil || !strings.Contains(err.Error(), "condition count") {
			t.Fatalf("got %v, want node-count error", err)
		}
	})

	t.Run("regex size", func(t *testing.T) {
		condition := approvalblock.Field("/request/arguments/message").Regex(strings.Repeat("a", approvalblock.MaximumRegexBytes+1))
		if err := validBlock(condition).Validate(); err == nil || !strings.Contains(err.Error(), "regex exceeds") {
			t.Fatalf("got %v, want regex-size error", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		block := validBlock(approvalblock.Field("/request/tool").Equals("deploy"))
		approvalblock.Timeout(approvalblock.MaximumTimeout + time.Millisecond)(block)
		if err := block.Validate(); err == nil || !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("got %v, want timeout error", err)
		}
	})
}

func TestParseRejectsUnknownFieldsAndInvalidRegex(t *testing.T) {
	block := validBlock(approvalblock.Field("/request/arguments/value").Equals("ok"))
	document, err := block.JSON()
	if err != nil {
		t.Fatal(err)
	}
	document = append(document[:len(document)-1], []byte(`,"unknown":true}`)...)
	if _, err := approvalblock.Parse(document); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("got %v, want unknown-field error", err)
	}

	invalidRegex := validBlock(approvalblock.Field("/request/arguments/value").Regex("["))
	if err := invalidRegex.Validate(); err == nil || !strings.Contains(err.Error(), "regex is invalid") {
		t.Fatalf("got %v, want invalid-regex error", err)
	}
}

func validBlock(condition approvalblock.Condition) *approvalblock.Definition {
	return approvalblock.Define(
		"test-block",
		approvalblock.Name("Test block"),
		approvalblock.OnMatch(approvalblock.Approve),
		approvalblock.When(condition),
		approvalblock.Reason("Test reason."),
	)
}

func rawLeaf(operator approvalblock.Operator, value string) approvalblock.Condition {
	return approvalblock.Condition{
		ID:       "91887fa3-cf36-4b05-888d-77edbb25a939",
		Field:    "/request/arguments/value",
		Operator: operator,
		Value:    json.RawMessage(value),
	}
}

func TestEnvelopeDispatchesConfigurationByType(t *testing.T) {
	block := validBlock(approvalblock.Field("/request/tool").Equals("stripe.refund"))
	document, err := block.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(document, &generic); err != nil {
		t.Fatal(err)
	}
	if _, present := generic["config"]; !present {
		t.Fatalf("configuration is not encoded under config: %s", document)
	}
	for _, key := range []string{"cel", "CEL"} {
		if _, present := generic[key]; present {
			t.Fatalf("variant pointer leaked onto the wire as %q: %s", key, document)
		}
	}

	t.Run("unknown type is rejected", func(t *testing.T) {
		unknown := strings.Replace(string(document), `"type":"cel"`, `"type":"code"`, 1)
		if _, err := approvalblock.Parse([]byte(unknown)); err == nil || !strings.Contains(err.Error(), "unknown block type") {
			t.Fatalf("got %v, want unknown block type error", err)
		}
	})
	t.Run("unknown config field is rejected", func(t *testing.T) {
		extra := strings.Replace(string(document), `"config":{`, `"config":{"unexpected":1,`, 1)
		if _, err := approvalblock.Parse([]byte(extra)); err == nil || !strings.Contains(err.Error(), "unexpected") {
			t.Fatalf("got %v, want unknown field error", err)
		}
	})
	t.Run("missing variant fails validation", func(t *testing.T) {
		copy := *block
		copy.CEL = nil
		if err := copy.Validate(); err == nil || !strings.Contains(err.Error(), "cel configuration") {
			t.Fatalf("got %v, want missing configuration error", err)
		}
	})
	t.Run("attempts follow the type limits", func(t *testing.T) {
		copy := *block
		copy.MaxAttempts = 2
		if err := copy.Validate(); err == nil || !strings.Contains(err.Error(), "exactly 1 attempt") {
			t.Fatalf("got %v, want attempts limit error", err)
		}
	})
}

func TestWebhookDefinitionValidatesItsConfiguration(t *testing.T) {
	const endpointID = "fraud-service"
	block := approvalblock.DefineWebhook("fraud-check",
		approvalblock.Name("Fraud service review"),
		approvalblock.Endpoint(endpointID),
		approvalblock.Reason("Fraud service reviewed the request."),
	)
	document, err := block.JSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := approvalblock.Parse(document)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Type != approvalblock.TypeWebhook || parsed.Timeout != "5s" || parsed.MaxAttempts != 3 || parsed.Webhook == nil {
		t.Fatalf("unexpected webhook defaults: %+v", parsed)
	}
	if parsed.Webhook.EndpointKey != endpointID || !parsed.Webhook.Allows(approvalblock.Next) || !parsed.Webhook.Allows(approvalblock.Human) || parsed.Webhook.Allows(approvalblock.Approve) {
		t.Fatalf("unexpected allowed outcomes: %+v", parsed.Webhook)
	}
	if !parsed.Webhook.Include.PreviousMetadata || parsed.Webhook.Include.AgentReasoning || parsed.Webhook.Include.Context {
		t.Fatalf("unexpected include defaults: %+v", parsed.Webhook.Include)
	}
	if !strings.Contains(string(document), `"type":"webhook"`) || !strings.Contains(string(document), `"allowed_outcomes":["next","human"]`) {
		t.Fatalf("unexpected document %s", document)
	}

	invalid := map[string]*approvalblock.Definition{
		"endpoint key invalid": approvalblock.DefineWebhook("w", approvalblock.Name("w"), approvalblock.Endpoint("Fraud Service"), approvalblock.Reason("r")),
		"no outcomes":          approvalblock.DefineWebhook("w", approvalblock.Name("w"), approvalblock.Endpoint(endpointID), approvalblock.AllowedOutcomes(), approvalblock.Reason("r")),
		"unknown outcome":      approvalblock.DefineWebhook("w", approvalblock.Name("w"), approvalblock.Endpoint(endpointID), approvalblock.AllowedOutcomes("maybe"), approvalblock.Reason("r")),
		"duplicate outcome":    approvalblock.DefineWebhook("w", approvalblock.Name("w"), approvalblock.Endpoint(endpointID), approvalblock.AllowedOutcomes(approvalblock.Next, approvalblock.Next), approvalblock.Reason("r")),
		"missing reason":       approvalblock.DefineWebhook("w", approvalblock.Name("w"), approvalblock.Endpoint(endpointID)),
		"too many attempts":    approvalblock.DefineWebhook("w", approvalblock.Name("w"), approvalblock.Endpoint(endpointID), approvalblock.Reason("r"), approvalblock.MaxAttempts(4)),
		"timeout too long":     approvalblock.DefineWebhook("w", approvalblock.Name("w"), approvalblock.Endpoint(endpointID), approvalblock.Reason("r"), approvalblock.Timeout(31*time.Second)),
	}
	for name, definition := range invalid {
		if err := definition.Validate(); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
	if _, err := approvalblock.Parse([]byte(`{"id":"w","name":"w","type":"webhook","enabled":true,"timeout":"5s","max_attempts":3,"config":{"endpoint_id":"` + endpointID + `","allowed_outcomes":["next"],"include":{"agent_reasoning":false,"context":false,"previous_metadata":true},"reason":"r","url":"https://x"}}`)); err == nil {
		t.Fatal("unknown config field accepted")
	}
}
