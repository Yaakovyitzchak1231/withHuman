package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/cel-go/common/types"
	"github.com/withHumanAI/withHuman/approvalblock"
)

func TestConditionMatchesAndDoesNotMatch(t *testing.T) {
	condition := approvalblock.All(
		approvalblock.Field("/request/tool").In("stripe.refund", "stripe.partial_refund"),
		approvalblock.Field("/request/arguments/amount_cents").LessThan(1000),
		approvalblock.Field("/request/arguments/customer/email").Regex(`^[^@]+@example\.com$`),
		approvalblock.Field("/request/arguments/customer/labels").Contains("priority"),
	)
	block := mustCompile(t, conditionDefinition(condition))

	matched := execute(t, block, blockInput(`{
		"amount_cents":900,
		"customer":{"email":"buyer@example.com","labels":["trial","priority"]}
	}`))
	if matched.Outcome != BlockEnter || matched.ErrorCode != "" {
		t.Fatalf("unexpected matching result: %+v", matched)
	}
	if len(matched.Evidence.Trace) != 5 || matched.Evidence.Cost == 0 {
		t.Fatalf("matching evaluation lacks evidence: %+v", matched.Evidence)
	}

	nonMatching := execute(t, block, blockInput(`{
		"amount_cents":1900,
		"customer":{"email":"buyer@example.com","labels":["priority"]}
	}`))
	if nonMatching.Outcome != BlockNext || nonMatching.Reason != "" || nonMatching.ErrorCode != "" {
		t.Fatalf("unexpected non-matching result: %+v", nonMatching)
	}
}

func TestConditionMissingNullAndMalformedInputs(t *testing.T) {
	t.Run("missing does not match ordinary operator", func(t *testing.T) {
		block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/arguments/missing").NotEquals("blocked")))
		result := execute(t, block, blockInput(`{}`))
		if result.Outcome != BlockNext {
			t.Fatalf("got %+v, want next", result)
		}
	})

	t.Run("not exists matches missing", func(t *testing.T) {
		block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/arguments/missing").NotExists()))
		result := execute(t, block, blockInput(`{}`))
		if result.Outcome != BlockEnter {
			t.Fatalf("got %+v, want enter", result)
		}
	})

	t.Run("null is present", func(t *testing.T) {
		block := mustCompile(t, conditionDefinition(approvalblock.All(
			approvalblock.Field("/request/arguments/value").Exists(),
			approvalblock.Field("/request/arguments/value").IsNull(),
		)))
		result := execute(t, block, blockInput(`{"value":null}`))
		if result.Outcome != BlockEnter {
			t.Fatalf("got %+v, want enter", result)
		}
	})

	for _, malformed := range []string{"", `{`, `[]`, `null`, `{"a":1,"a":2}`} {
		t.Run("malformed "+malformed, func(t *testing.T) {
			block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/tool").Equals("stripe.refund")))
			result := execute(t, block, blockInput(malformed))
			if result.Outcome != BlockHuman || result.ErrorCode != ErrorInvalidInput {
				t.Fatalf("got %+v, want safe invalid-input handoff", result)
			}
		})
	}
}

func TestConditionMatchesTheMCPServer(t *testing.T) {
	block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/server").Equals("stripe")))
	input := blockInput(`{}`)
	input.Request.Server = "stripe"
	if result := execute(t, block, input); result.Outcome != BlockEnter || result.ErrorCode != "" {
		t.Fatalf("got %+v, want the request's server to match", result)
	}
	input.Request.Server = "paypal"
	if result := execute(t, block, input); result.Outcome != BlockNext || result.ErrorCode != "" {
		t.Fatalf("got %+v, want another server to fall through", result)
	}
}

func TestConditionBuiltInToolHasNoMCPServer(t *testing.T) {
	input := blockInput(`{}`)
	input.Request.Tool = "Bash"
	for _, condition := range []approvalblock.Condition{
		approvalblock.Field("/request/server").Exists(),
		approvalblock.Field("/request/server").Equals("stripe"),
		approvalblock.Field("/request/server").NotEquals("stripe"),
		approvalblock.Field("/request/server").NotIn("stripe"),
		approvalblock.Field("/request/server").NotContains("stripe"),
		approvalblock.Field("/request/server").IsNull(),
		approvalblock.Field("/request/server").IsNotNull(),
	} {
		t.Run(string(condition.Operator), func(t *testing.T) {
			block := mustCompile(t, conditionDefinition(condition))
			if result := execute(t, block, input); result.Outcome != BlockNext || result.ErrorCode != "" {
				t.Fatalf("got %+v, want a missing server to fall through", result)
			}
		})
	}
	t.Run(string(approvalblock.NotExists), func(t *testing.T) {
		block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/server").NotExists()))
		if result := execute(t, block, input); result.Outcome != BlockEnter || result.ErrorCode != "" {
			t.Fatalf("got %+v, want not_exists to match a built-in tool", result)
		}
	})
}

func TestConditionRuntimeTypeErrorsRequireHumanReview(t *testing.T) {
	block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/arguments/amount_cents").LessThan(1000)))
	result := execute(t, block, blockInput(`{"amount_cents":"900"}`))
	if result.Outcome != BlockHuman || result.ErrorCode != ErrorEvaluation {
		t.Fatalf("got %+v, want evaluation error and human review", result)
	}
	if result.Reason != "" {
		t.Fatalf("runtime error leaked through reason: %q", result.Reason)
	}

	nested := mustCompile(t, conditionDefinition(approvalblock.Field("/request/arguments/customer/email").Equals("buyer@example.com")))
	result = execute(t, nested, blockInput(`{"customer":"not-an-object"}`))
	if result.Outcome != BlockHuman || result.ErrorCode != ErrorEvaluation {
		t.Fatalf("got %+v, want scalar traversal to require human review", result)
	}

	// A string target reads the configured values as substrings, so only
	// a non-string comparison or a non-container target is an error.
	set := mustCompile(t, conditionDefinition(approvalblock.Field("/request/arguments/command").ContainsAny("python3", 3)))
	result = execute(t, set, blockInput(`{"command":"pip install requests"}`))
	if result.Outcome != BlockHuman || result.ErrorCode != ErrorEvaluation {
		t.Fatalf("got %+v, want a numeric substring comparison to require human review", result)
	}
	result = execute(t, set, blockInput(`{"command":42}`))
	if result.Outcome != BlockHuman || result.ErrorCode != ErrorEvaluation {
		t.Fatalf("got %+v, want a scalar containment target to require human review", result)
	}
	miss := mustCompile(t, conditionDefinition(approvalblock.Field("/request/arguments/command").ContainsAny("python3", "sudo")))
	result = execute(t, miss, blockInput(`{"command":"du -hd 2 ~/Library/Caches | sort -h"}`))
	if result.Outcome != BlockNext || result.ErrorCode != "" {
		t.Fatalf("got %+v, want a string without any of the substrings to fall through", result)
	}
}

func TestConditionQuantifiersAndArrayOperators(t *testing.T) {
	tests := []struct {
		name      string
		condition approvalblock.Condition
		arguments string
		want      BlockOutcome
	}{
		{
			name: "some",
			condition: approvalblock.Some("/request/arguments/items",
				approvalblock.Field("/amount").GreaterThan(100)),
			arguments: `{"items":[{"amount":50},{"amount":150}]}`,
			want:      BlockEnter,
		},
		{
			name: "every",
			condition: approvalblock.Every("/request/arguments/items",
				approvalblock.Field("/enabled").IsTrue()),
			arguments: `{"items":[{"enabled":true},{"enabled":true}]}`,
			want:      BlockEnter,
		},
		{
			name: "empty every is false",
			condition: approvalblock.Every("/request/arguments/items",
				approvalblock.Field("").IsNotNull()),
			arguments: `{"items":[]}`,
			want:      BlockNext,
		},
		{
			name:      "contains all",
			condition: approvalblock.Field("/request/arguments/labels").ContainsAll("priority", "trial"),
			arguments: `{"labels":["trial","priority","paid"]}`,
			want:      BlockEnter,
		},
		{
			name:      "escaped pointer",
			condition: approvalblock.Field("/request/arguments/a~1b/~0key").Equals("value"),
			arguments: `{"a/b":{"~key":"value"}}`,
			want:      BlockEnter,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			block := mustCompile(t, conditionDefinition(test.condition))
			result := execute(t, block, blockInput(test.arguments))
			if result.Outcome != test.want || result.ErrorCode != "" {
				t.Fatalf("got %+v, want %q", result, test.want)
			}
		})
	}
}

func TestConditionEachLeafOperator(t *testing.T) {
	tests := []struct {
		name      string
		condition approvalblock.Condition
		arguments string
	}{
		{name: "exists", condition: approvalblock.Field("/request/arguments/value").Exists(), arguments: `{"value":null}`},
		{name: "not exists", condition: approvalblock.Field("/request/arguments/missing").NotExists(), arguments: `{}`},
		{name: "equals", condition: approvalblock.Field("/request/arguments/value").Equals(10), arguments: `{"value":10.0}`},
		{name: "not equals", condition: approvalblock.Field("/request/arguments/value").NotEquals("10"), arguments: `{"value":10}`},
		{name: "is null", condition: approvalblock.Field("/request/arguments/value").IsNull(), arguments: `{"value":null}`},
		{name: "is not null", condition: approvalblock.Field("/request/arguments/value").IsNotNull(), arguments: `{"value":false}`},
		{name: "in", condition: approvalblock.Field("/request/arguments/value").In("one", "two"), arguments: `{"value":"two"}`},
		{name: "not in", condition: approvalblock.Field("/request/arguments/value").NotIn("one", "two"), arguments: `{"value":"three"}`},
		{name: "string contains", condition: approvalblock.Field("/request/arguments/value").Contains("needle"), arguments: `{"value":"a needle here"}`},
		{name: "array contains", condition: approvalblock.Field("/request/arguments/value").Contains(2), arguments: `{"value":[1,2,3]}`},
		{name: "not contains", condition: approvalblock.Field("/request/arguments/value").NotContains("blocked"), arguments: `{"value":["allowed"]}`},
		{name: "starts with", condition: approvalblock.Field("/request/arguments/value").StartsWith("other", "pre"), arguments: `{"value":"prefix"}`},
		{name: "ends with", condition: approvalblock.Field("/request/arguments/value").EndsWith("fix"), arguments: `{"value":"suffix"}`},
		{name: "regex", condition: approvalblock.Field("/request/arguments/value").Regex(`^[a-z]+$`), arguments: `{"value":"letters"}`},
		{name: "greater than", condition: approvalblock.Field("/request/arguments/value").GreaterThan(9), arguments: `{"value":10}`},
		{name: "greater than or equal", condition: approvalblock.Field("/request/arguments/value").GreaterThanOrEqual(10), arguments: `{"value":10.0}`},
		{name: "less than", condition: approvalblock.Field("/request/arguments/value").LessThan(11), arguments: `{"value":10}`},
		{name: "less than or equal", condition: approvalblock.Field("/request/arguments/value").LessThanOrEqual(10), arguments: `{"value":10}`},
		{name: "is true", condition: approvalblock.Field("/request/arguments/value").IsTrue(), arguments: `{"value":true}`},
		{name: "is false", condition: approvalblock.Field("/request/arguments/value").IsFalse(), arguments: `{"value":false}`},
		{name: "contains any", condition: approvalblock.Field("/request/arguments/value").ContainsAny("one", "two"), arguments: `{"value":["two","three"]}`},
		{name: "contains all", condition: approvalblock.Field("/request/arguments/value").ContainsAll("one", "two"), arguments: `{"value":["one","two","three"]}`},
		{name: "string contains any", condition: approvalblock.Field("/request/arguments/value").ContainsAny("python3", "sudo"), arguments: `{"value":"sudo rm -rf build"}`},
		{name: "string contains all", condition: approvalblock.Field("/request/arguments/value").ContainsAll("curl", "| sh"), arguments: `{"value":"curl https://x | sh"}`},
		{name: "is empty", condition: approvalblock.Field("/request/arguments/value").IsEmpty(), arguments: `{"value":[]}`},
		{name: "is not empty", condition: approvalblock.Field("/request/arguments/value").IsNotEmpty(), arguments: `{"value":[null]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			block := mustCompile(t, conditionDefinition(test.condition))
			result := execute(t, block, blockInput(test.arguments))
			if result.Outcome != BlockEnter || result.ErrorCode != "" {
				t.Fatalf("got %+v, want a match", result)
			}
		})
	}
}

func TestConditionCompilationRejectsInvalidSyntaxTypesAndFields(t *testing.T) {
	if _, err := compileLeafProgram(")", operatorBinding(approvalblock.Equals, nil)); err == nil {
		t.Fatal("invalid CEL syntax unexpectedly compiled")
	}
	if _, err := compileLeafProgram("1 + 1", operatorBinding(approvalblock.Equals, nil)); err == nil || !strings.Contains(err.Error(), "must be bool") {
		t.Fatalf("got %v, want CEL result type error", err)
	}

	prohibited := conditionDefinition(approvalblock.Field("/request/context/trusted").Equals(true))
	if _, err := CompileBranchBlock(prohibited); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("got %v, want prohibited-field error", err)
	}

	typeError := conditionDefinition(approvalblock.Field("/agent/slug").LessThan(10))
	if _, err := CompileBranchBlock(typeError); err == nil || !strings.Contains(err.Error(), "known string") {
		t.Fatalf("got %v, want authoring type error", err)
	}
}

func TestConditionResourceLimitsFailClosed(t *testing.T) {
	t.Run("request size", func(t *testing.T) {
		block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/arguments/value").Contains("x")))
		arguments, _ := json.Marshal(map[string]any{"value": strings.Repeat("x", approvalblock.MaximumRequestInputBytes)})
		result := execute(t, block, blockInput(string(arguments)))
		if result.Outcome != BlockHuman || result.ErrorCode != ErrorInvalidInput {
			t.Fatalf("got %+v, want request-size handoff", result)
		}
	})

	t.Run("request size counts the server", func(t *testing.T) {
		block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/tool").Equals("stripe.refund")))
		input := blockInput(`{}`)
		input.Request.Server = strings.Repeat("s", approvalblock.MaximumRequestInputBytes)
		result := execute(t, block, input)
		if result.Outcome != BlockHuman || result.ErrorCode != ErrorInvalidInput {
			t.Fatalf("got %+v, want request-size handoff", result)
		}
	})

	t.Run("comprehension entries", func(t *testing.T) {
		block := mustCompile(t, conditionDefinition(approvalblock.Some("/request/arguments/items", approvalblock.Field("").Equals("match"))))
		items := make([]string, approvalblock.MaximumComprehensionEntries+1)
		arguments, _ := json.Marshal(map[string]any{"items": items})
		result := execute(t, block, blockInput(string(arguments)))
		if result.Outcome != BlockHuman || result.ErrorCode != ErrorResourceLimit {
			t.Fatalf("got %+v, want comprehension-limit handoff", result)
		}
	})

	t.Run("evaluation cost", func(t *testing.T) {
		block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/arguments/value").Contains("needle")))
		arguments, _ := json.Marshal(map[string]any{"value": strings.Repeat("a", approvalblock.MaximumEvaluationCost+1)})
		result := execute(t, block, blockInput(string(arguments)))
		if result.Outcome != BlockHuman || result.ErrorCode != ErrorCostLimit {
			t.Fatalf("got %+v, want cost-limit handoff", result)
		}
	})

	t.Run("expression size", func(t *testing.T) {
		conditions := make([]approvalblock.Condition, 100)
		for index := range conditions {
			conditions[index] = approvalblock.Field("/request/arguments/value").Equals(index)
		}
		if _, err := CompileBranchBlock(conditionDefinition(approvalblock.All(conditions...))); err == nil || !strings.Contains(err.Error(), "expression exceeds") {
			t.Fatalf("got %v, want expression-size error", err)
		}
	})

	t.Run("conditions per request", func(t *testing.T) {
		block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/tool").Equals("stripe.refund")))
		input := blockInput(`{}`)
		input.Budget = &BlockExecutionBudget{}
		for range approvalblock.MaximumConditionsPerRequest {
			result := execute(t, block, input)
			if result.Outcome != BlockEnter {
				t.Fatalf("budget failed early: %+v", result)
			}
		}
		result := execute(t, block, input)
		if result.Outcome != BlockHuman || result.ErrorCode != ErrorResourceLimit {
			t.Fatalf("got %+v, want condition-budget handoff", result)
		}
	})
}

func TestConditionEvaluationIsDeterministicAndIgnoresPreviousMetadata(t *testing.T) {
	definition := conditionDefinition(approvalblock.Any(
		approvalblock.Field("/agent/slug").Equals("billing"),
		approvalblock.Field("/request/arguments/amount").GreaterThan(500),
	))
	block := mustCompile(t, definition)
	input := blockInput(`{"amount":600}`)
	input.PreviousMetadata = map[string]json.RawMessage{"risk": json.RawMessage(`{"level":"low"}`)}
	want := execute(t, block, input)
	for range 50 {
		input.PreviousMetadata["risk"] = json.RawMessage(`{"level":"high","context":"untrusted"}`)
		got := execute(t, block, input)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("evaluation changed across identical trusted inputs:\nwant %+v\n got %+v", want, got)
		}
	}

	definition.Branch.When = approvalblock.Field("/agent/slug").Equals("nobody")
	if got := execute(t, block, input); got.Outcome != BlockEnter {
		t.Fatalf("compiled block was mutable through source definition: %+v", got)
	}
}

func TestExecutionContractSafelyHandlesErrorsPanicsAndAmbiguousResults(t *testing.T) {
	tests := []struct {
		name  string
		block RequestApprovalBlock
		code  BlockErrorCode
	}{
		{name: "error", block: fakeBlock(func(context.Context, RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
			return RequestApprovalBlockResult{}, errors.New("secret provider failure")
		}), code: ErrorEvaluation},
		{name: "typed error", block: fakeBlock(func(context.Context, RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
			return RequestApprovalBlockResult{}, executionError(ErrorCostLimit, errors.New("raw details"))
		}), code: ErrorCostLimit},
		{name: "ambiguous outcome", block: fakeBlock(func(context.Context, RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
			return RequestApprovalBlockResult{Outcome: "maybe"}, nil
		}), code: ErrorInvalidBlockOutput},
		{name: "untrusted error code", block: fakeBlock(func(context.Context, RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
			return RequestApprovalBlockResult{Outcome: BlockHuman, ErrorCode: "raw_provider_failure"}, nil
		}), code: ErrorInvalidBlockOutput},
		{name: "panic", block: fakeBlock(func(context.Context, RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
			panic("secret panic")
		}), code: ErrorInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ExecuteRequestApprovalBlock(context.Background(), test.block, blockInput(`{}`))
			if result.Outcome != BlockHuman || result.ErrorCode != test.code || result.Reason != "" {
				t.Fatalf("unsafe failure result: %+v", result)
			}
		})
	}
}

func TestConditionContextCancellationRequiresHumanReview(t *testing.T) {
	block := mustCompile(t, conditionDefinition(approvalblock.Field("/request/tool").Equals("stripe.refund")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := executeBranchCondition(ctx, block, blockInput(`{}`))
	if result.Outcome != BlockHuman || result.ErrorCode != ErrorTimeout {
		t.Fatalf("got %+v, want timeout handoff", result)
	}
}

type fakeBlock func(context.Context, RequestApprovalBlockInput) (RequestApprovalBlockResult, error)

func (block fakeBlock) Evaluate(ctx context.Context, input RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
	return block(ctx, input)
}

// conditionDefinition is a branch testing condition, the shortest way to
// run the condition engine through a real block: a match enters it.
func conditionDefinition(condition approvalblock.Condition) approvalblock.Definition {
	return *approvalblock.DefineBranch(
		"test-block",
		[]approvalblock.Definition{*approvalblock.DefineOutcome("inside", approvalblock.Approve, approvalblock.Reason("Configured reason."))},
		approvalblock.Name("Test block"),
		approvalblock.When(condition),
	)
}

func blockInput(arguments string) RequestApprovalBlockInput {
	return RequestApprovalBlockInput{
		Request: BlockRequest{Tool: "stripe.refund", Arguments: json.RawMessage(arguments)},
		Agent:   BlockAgent{Slug: "billing", Name: "billing-agent"},
	}
}

func mustCompile(t *testing.T, definition approvalblock.Definition) *CompiledBranchBlock {
	t.Helper()
	block, err := CompileBranchBlock(definition)
	if err != nil {
		t.Fatal(err)
	}
	return block
}

func execute(t *testing.T, block RequestApprovalBlock, input RequestApprovalBlockInput) RequestApprovalBlockResult {
	t.Helper()
	return executeBranchCondition(context.Background(), block, input)
}

func TestOperatorBindingNeverReturnsAnAmbiguousCELValue(t *testing.T) {
	result := operatorBinding(approvalblock.Equals, nil)(types.String("one"), types.String("one"))
	if result != types.True {
		t.Fatalf("got %v, want true", result)
	}
}

func TestConditionReadsPreviousBlockMetadataByPipelineAndKey(t *testing.T) {
	block := mustCompile(t, conditionDefinition(approvalblock.All(
		approvalblock.Field("/previous_metadata/entry/fraud-check/risk_score").LessThan(50),
	)))
	input := blockInput(`{"amount_cents":900}`)
	input.PreviousMetadata = map[string]json.RawMessage{"entry/fraud-check": json.RawMessage(`{"risk_score":12,"band":"low"}`)}
	if result := execute(t, block, input); result.Outcome != BlockEnter {
		t.Fatalf("threshold on previous metadata did not match: %+v", result)
	}
	input.PreviousMetadata = map[string]json.RawMessage{"billing/fraud-check": json.RawMessage(`{"risk_score":12}`)}
	if result := execute(t, block, input); result.Outcome != BlockNext || result.ErrorCode != "" {
		t.Fatalf("metadata under another pipeline matched: %+v", result)
	}
	input.PreviousMetadata = nil
	if result := execute(t, block, input); result.Outcome != BlockNext || result.ErrorCode != "" {
		t.Fatalf("absent block did not fall through: %+v", result)
	}
	absent := mustCompile(t, conditionDefinition(approvalblock.All(
		approvalblock.Field("/previous_metadata/entry/fraud-check").NotExists(),
	)))
	if result := execute(t, absent, input); result.Outcome != BlockEnter {
		t.Fatalf("not_exists did not match an absent block: %+v", result)
	}
	for _, path := range []string{"/previous_metadata", "/previous_metadata/entry", "/previous_metadata/Bad_Key/x", "/previous_metadata/entry/"} {
		if _, err := CompileBranchBlock(conditionDefinition(approvalblock.Field(path).Exists())); err == nil {
			t.Fatalf("path %q compiled", path)
		}
	}
}
