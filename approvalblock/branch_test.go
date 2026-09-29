package approvalblock_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/approvalblock"
)

func refundBranch(blocks ...approvalblock.Definition) *approvalblock.Definition {
	return approvalblock.DefineBranch("refunds", blocks,
		approvalblock.Name("Refunds"),
		approvalblock.When(approvalblock.ConditionID(rootID, approvalblock.Field("/request/tool").Equals("stripe.refund"))),
		approvalblock.Reason("Refunds nobody decided go to Payments."),
	)
}

// smallRefund is "if the refund is small, approve it": a branch holding
// one outcome block.
func smallRefund() approvalblock.Definition {
	return *approvalblock.DefineBranch("small-refund",
		[]approvalblock.Definition{
			*approvalblock.DefineOutcome("approve-small-refund", approvalblock.Approve,
				approvalblock.Name("Approve"), approvalblock.Reason("Small refunds are pre-authorized.")),
		},
		approvalblock.Name("Approve small refunds"),
		approvalblock.When(approvalblock.ConditionID(amountID, approvalblock.Field("/request/arguments/amount").LessThan(1000.50))),
	)
}

func TestBranchDocumentRoundTripsNestedBlocks(t *testing.T) {
	inner := approvalblock.DefineBranch("large", []approvalblock.Definition{
		*approvalblock.DefineOutcome("ask", approvalblock.Human, approvalblock.Name("Ask"), approvalblock.Reason("Large refunds need a person.")),
	},
		approvalblock.Name("Large refunds"),
		approvalblock.When(approvalblock.ConditionID(toolID, approvalblock.Field("/request/arguments/amount").GreaterThan(100000))),
		approvalblock.Reason("Large refunds nobody decided."),
	)
	block := refundBranch(smallRefund(), *inner)
	block.Branch.EscalationPath = "payments-review"

	document, err := block.JSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := approvalblock.Parse(document)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Type != approvalblock.TypeBranch || parsed.Timeout != "50ms" || parsed.MaxAttempts != 1 || parsed.Branch == nil {
		t.Fatalf("unexpected branch envelope: %+v", parsed)
	}
	if parsed.EscalationPath() != "payments-review" || parsed.Branch.Reason != "Refunds nobody decided go to Payments." {
		t.Fatalf("branch config did not round trip: %+v", parsed.Branch)
	}
	if len(parsed.Branch.Blocks) != 2 || parsed.Branch.Blocks[0].ID != "small-refund" || parsed.Branch.Blocks[1].Branch == nil {
		t.Fatalf("nested blocks did not round trip: %+v", parsed.Branch.Blocks)
	}
	if nested := parsed.Branch.Blocks[1].Branch.Blocks; len(nested) != 1 || nested[0].Outcome == nil {
		t.Fatalf("second level did not round trip: %+v", nested)
	}
	// Numbers keep their exact spelling through the one-pass decoder.
	config, err := parsed.Branch.Blocks[0].ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(config, []byte(`1000.5`)) {
		t.Fatalf("condition value changed: %s", config)
	}
	reencoded, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, document) {
		t.Fatalf("encoding is not stable:\n%s\n%s", document, reencoded)
	}
}

func TestBranchRejectsInvalidConfiguration(t *testing.T) {
	valid := refundBranch(smallRefund())
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid branch rejected: %v", err)
	}
	for name, mutate := range map[string]func(*approvalblock.Definition){
		"no blocks":     func(block *approvalblock.Definition) { block.Branch.Blocks = nil },
		"empty blocks":  func(block *approvalblock.Definition) { block.Branch.Blocks = []approvalblock.Definition{} },
		"bad path":      func(block *approvalblock.Definition) { block.Branch.EscalationPath = "Payments Review" },
		"two attempts":  func(block *approvalblock.Definition) { block.MaxAttempts = 2 },
		"slow":          func(block *approvalblock.Definition) { block.Timeout = "2s" },
		"no condition":  func(block *approvalblock.Definition) { block.Branch.When = approvalblock.Condition{} },
		"no config":     func(block *approvalblock.Definition) { block.Branch = nil },
		"invalid child": func(block *approvalblock.Definition) { block.Branch.Blocks[0].Branch.Blocks[0].Outcome.Reason = "" },
	} {
		t.Run(name, func(t *testing.T) {
			block := refundBranch(smallRefund())
			mutate(block)
			if err := block.Validate(); err == nil {
				t.Fatal("expected validation to fail")
			}
		})
	}

	// A branch's own reason is optional: one whose blocks end in an
	// outcome block never runs out.
	unexplained := refundBranch(smallRefund())
	unexplained.Branch.Reason = ""
	if err := unexplained.Validate(); err != nil {
		t.Fatalf("branch without a reason rejected: %v", err)
	}

	block := refundBranch(smallRefund())
	block.Branch.Blocks[0].Branch.Blocks[0].Outcome.Reason = ""
	if err := block.ValidateBlock(); err != nil {
		t.Fatalf("ValidateBlock visited a child: %v", err)
	}
	if err := block.Validate(); err == nil || !strings.Contains(err.Error(), `block "small-refund"`) {
		t.Fatalf("Validate did not name the invalid child: %v", err)
	}
}

func TestBranchDocumentSizeIsMeasuredPerBlock(t *testing.T) {
	children := make([]approvalblock.Definition, 0, 20)
	for index := range 20 {
		child := approvalblock.DefineOutcome(fmt.Sprintf("refund-%d", index), approvalblock.Human,
			approvalblock.Name("Refund"), approvalblock.Reason(strings.Repeat("r", 4_000)))
		children = append(children, *child)
	}
	block := refundBranch(children...)
	if err := block.Validate(); err != nil {
		t.Fatalf("children totalling more than one block's limit rejected the branch: %v", err)
	}
	block.Branch.Reason = strings.Repeat("r", approvalblock.MaximumBlockDocumentBytes)
	if err := block.ValidateBlock(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("an oversized branch document was accepted: %v", err)
	}
}

func TestBranchDecodingIsStrictAtEveryLevel(t *testing.T) {
	document, err := refundBranch(smallRefund()).JSON()
	if err != nil {
		t.Fatal(err)
	}
	for name, replacement := range map[string][2]string{
		"unknown nested config field": {`"outcome":"approve"`, `"outcome":"approve","surprise":true`},
		"unknown nested envelope":     {`"id":"small-refund"`, `"id":"small-refund","surprise":true`},
		"unknown branch config field": {`"reason":"Refunds nobody decided go to Payments."`, `"reason":"Refunds nobody decided go to Payments.","surprise":true`},
		"blocks not an array":         {`"blocks":[`, `"blocks":{"x":[`},
		"ambiguous config":            {`"id":"refunds"`, `"id":"refunds","Config":{}`},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := strings.Replace(string(document), replacement[0], replacement[1], 1)
			if mutated == string(document) {
				t.Fatalf("replacement %q did not apply to %s", replacement[0], document)
			}
			if _, err := approvalblock.Parse([]byte(mutated)); err == nil {
				t.Fatalf("accepted %s", mutated)
			}
		})
	}
}

func TestDSLOptionsIgnoreConfigurationsTheyDoNotApplyTo(t *testing.T) {
	condition := approvalblock.Field("/request/tool").Equals("Bash")
	outcome := approvalblock.DefineOutcome("outcome", approvalblock.Approve, approvalblock.When(condition), approvalblock.Reason("Fine."))
	if outcome.Outcome.Outcome != approvalblock.Approve || outcome.Outcome.Reason != "Fine." {
		t.Fatalf("When changed an outcome block: %+v", outcome.Outcome)
	}
	webhook := approvalblock.DefineWebhook("hook", approvalblock.Endpoint("fraud"), approvalblock.When(condition))
	if webhook.Webhook == nil || webhook.Webhook.EndpointKey != "fraud" {
		t.Fatal("webhook configuration lost")
	}
	branch := approvalblock.DefineBranch("branch", []approvalblock.Definition{smallRefund()}, approvalblock.When(condition), approvalblock.Reason("Nothing decided."))
	if branch.Branch.When.ID != condition.ID || branch.Branch.Reason != "Nothing decided." {
		t.Fatalf("When and Reason did not set the branch configuration: %+v", branch.Branch)
	}
}

// A chain of branches as deep as the pipeline limit allows, carrying one
// large leaf, must decode and encode in time proportional to its size.
func TestDeeplyNestedBranchesDecodeAndEncodeLinearly(t *testing.T) {
	if testing.Short() {
		t.Skip("timing guard")
	}
	document := nestedBranchDocument(approvalblock.MaximumPipelineBlocks-1, 512*1024)
	started := time.Now()
	var definition approvalblock.Definition
	if err := json.Unmarshal(document, &definition); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(definition); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("decoding and encoding %d nested branches took %s", approvalblock.MaximumPipelineBlocks-1, elapsed)
	}
}

func BenchmarkDeeplyNestedBranchDecode(b *testing.B) {
	document := nestedBranchDocument(approvalblock.MaximumPipelineBlocks-1, 512*1024)
	b.SetBytes(int64(len(document)))
	for b.Loop() {
		var definition approvalblock.Definition
		if err := json.Unmarshal(document, &definition); err != nil {
			b.Fatal(err)
		}
	}
}

func nestedBranchDocument(depth, leafBytes int) []byte {
	var builder strings.Builder
	for level := range depth {
		fmt.Fprintf(&builder, `{"id":"level-%d","name":"Level","type":"branch","enabled":true,"timeout":"50ms","max_attempts":1,"config":{"when":{"id":"00000000-0000-4000-8000-%012d","field":"/request/tool","operator":"equals","value":"stripe.refund"},"reason":"Nothing decided.","blocks":[`, level, level)
	}
	fmt.Fprintf(&builder, `{"id":"leaf","name":"Leaf","type":"outcome","enabled":true,"timeout":"50ms","max_attempts":1,"config":{"outcome":"human","reason":%q}}`, strings.Repeat("x", leafBytes))
	for range depth {
		builder.WriteString(`]}}`)
	}
	return []byte(builder.String())
}
