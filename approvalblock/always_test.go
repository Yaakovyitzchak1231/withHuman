package approvalblock_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/withHumanAI/withHuman/approvalblock"
)

func TestAlwaysDocument(t *testing.T) {
	for _, outcome := range []approvalblock.Outcome{approvalblock.Approve, approvalblock.Deny, approvalblock.Human} {
		t.Run(string(outcome), func(t *testing.T) {
			block := approvalblock.DefineAlways("remaining", outcome, approvalblock.Name("Remaining requests"), approvalblock.Reason("Configured reason."))
			if outcome == approvalblock.Human {
				block.Always.EscalationPath = "payments"
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
			if parsed.Type != approvalblock.TypeAlways || parsed.Timeout != "50ms" || parsed.MaxAttempts != 1 || parsed.EscalationPath() != block.Always.EscalationPath {
				t.Fatalf("unexpected definition: %+v", parsed)
			}
		})
	}
}

func TestAlwaysRejectsInvalidConfiguration(t *testing.T) {
	tests := map[string]func(*approvalblock.Definition){
		"missing config":  func(b *approvalblock.Definition) { b.Always = nil },
		"missing outcome": func(b *approvalblock.Definition) { b.Always.Outcome = "" },
		"next outcome":    func(b *approvalblock.Definition) { b.Always.Outcome = approvalblock.Next },
		"unknown outcome": func(b *approvalblock.Definition) { b.Always.Outcome = "maybe" },
		"missing reason":  func(b *approvalblock.Definition) { b.Always.Reason = " " },
		"missing name":    func(b *approvalblock.Definition) { b.Name = " " },
		"automatic path": func(b *approvalblock.Definition) {
			b.Always.Outcome = approvalblock.Approve
			b.Always.EscalationPath = "payments"
		},
		"invalid path": func(b *approvalblock.Definition) { b.Always.EscalationPath = "Payments!" },
		"timeout":      func(b *approvalblock.Definition) { b.Timeout = "2s" },
		"attempts":     func(b *approvalblock.Definition) { b.MaxAttempts = 2 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			block := approvalblock.DefineAlways("remaining", approvalblock.Human, approvalblock.Name("Remaining"), approvalblock.Reason("Review remaining requests."))
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
		block := approvalblock.DefineAlways("remaining", approvalblock.Human, approvalblock.Name("Remaining"), approvalblock.Reason("r"))
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
