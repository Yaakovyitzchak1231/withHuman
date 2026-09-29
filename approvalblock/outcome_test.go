package approvalblock_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/withHumanAI/withHuman/approvalblock"
)

func TestOutcomeDocument(t *testing.T) {
	for _, outcome := range []approvalblock.Outcome{approvalblock.Approve, approvalblock.Deny, approvalblock.Human} {
		t.Run(string(outcome), func(t *testing.T) {
			block := approvalblock.DefineOutcome("remaining", outcome, approvalblock.Name("Remaining requests"), approvalblock.Reason("Configured reason."))
			if outcome == approvalblock.Human {
				block.Outcome.EscalationPath = "payments"
			}
			document, err := block.JSON()
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := approvalblock.Parse(document)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(block, parsed) {
				t.Fatalf("document did not round trip: %s", document)
			}
			if parsed.Type != approvalblock.TypeOutcome || parsed.Timeout != "50ms" || parsed.MaxAttempts != 1 || parsed.EscalationPath() != block.Outcome.EscalationPath {
				t.Fatalf("unexpected definition: %+v", parsed)
			}
		})
	}
}

func TestOutcomeRejectsInvalidConfiguration(t *testing.T) {
	tests := map[string]func(*approvalblock.Definition){
		"missing config":  func(b *approvalblock.Definition) { b.Outcome = nil },
		"missing outcome": func(b *approvalblock.Definition) { b.Outcome.Outcome = "" },
		"next outcome":    func(b *approvalblock.Definition) { b.Outcome.Outcome = approvalblock.Next },
		"unknown outcome": func(b *approvalblock.Definition) { b.Outcome.Outcome = "maybe" },
		"missing reason":  func(b *approvalblock.Definition) { b.Outcome.Reason = " " },
		"missing name":    func(b *approvalblock.Definition) { b.Name = " " },
		"automatic path": func(b *approvalblock.Definition) {
			b.Outcome.Outcome = approvalblock.Approve
			b.Outcome.EscalationPath = "payments"
		},
		"invalid path": func(b *approvalblock.Definition) { b.Outcome.EscalationPath = "Payments!" },
		"timeout":      func(b *approvalblock.Definition) { b.Timeout = "2s" },
		"attempts":     func(b *approvalblock.Definition) { b.MaxAttempts = 2 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			block := approvalblock.DefineOutcome("remaining", approvalblock.Human, approvalblock.Name("Remaining"), approvalblock.Reason("Review remaining requests."))
			mutate(block)
			if err := block.Validate(); err == nil {
				t.Fatal("invalid definition accepted")
			}
		})
	}
	for _, config := range []string{
		`{"outcome":"approve","reason":"r","when":true}`,
		`{"outcome":"approve","reason":"r","on_match":"approve"}`,
		`{"outcome":"approve"}`,
	} {
		block := approvalblock.DefineOutcome("remaining", approvalblock.Human, approvalblock.Name("Remaining"), approvalblock.Reason("r"))
		raw, _ := block.JSON()
		var document map[string]json.RawMessage
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		document["config"] = json.RawMessage(config)
		raw, _ = json.Marshal(document)
		if _, err := approvalblock.Parse(raw); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
}
