package toolsuggest

import (
	"testing"

	"github.com/withHumanAI/withHuman/agentruntime"
)

func boolPtr(value bool) *bool { return &value }

func TestSuggestRules(t *testing.T) {
	cases := []struct {
		name string
		tool Tool
		want Verdict
	}{
		{"shell builtin", Tool{Source: agentruntime.SourceBuiltin, Name: "Bash", Kind: agentruntime.ToolShell}, Verdict{true, ReasonShell}},
		{"edit builtin", Tool{Source: agentruntime.SourceBuiltin, Name: "Edit", Kind: agentruntime.ToolEdit}, Verdict{false, ReasonEditNotSuggested}},
		{"read builtin", Tool{Source: agentruntime.SourceBuiltin, Name: "Read", Kind: agentruntime.ToolRead}, Verdict{false, ReasonReadOnlyBuiltin}},
		{"other builtin", Tool{Source: agentruntime.SourceBuiltin, Name: "Task", Kind: agentruntime.ToolOther}, Verdict{false, ReasonReadOnlyBuiltin}},
		{"annotation read only wins over name", Tool{Source: agentruntime.SourceMCP, Name: "delete_preview", Annotations: &Annotations{ReadOnlyHint: boolPtr(true)}}, Verdict{false, ReasonAnnotationReadOnly}},
		{"annotation destructive", Tool{Source: agentruntime.SourceMCP, Name: "customer", Annotations: &Annotations{DestructiveHint: boolPtr(true)}}, Verdict{true, ReasonAnnotationDestructive}},
		{"annotation mutating", Tool{Source: agentruntime.SourceMCP, Name: "customer", Annotations: &Annotations{ReadOnlyHint: boolPtr(false)}}, Verdict{true, ReasonAnnotationMutating}},
		{"annotations silent falls through", Tool{Source: agentruntime.SourceMCP, Name: "get_customer", Annotations: &Annotations{IdempotentHint: boolPtr(true)}}, Verdict{false, "verb:get"}},
		{"mutating verb in name", Tool{Source: agentruntime.SourceMCP, Name: "create_refund"}, Verdict{true, "verb:create"}},
		{"first verb in name decides", Tool{Source: agentruntime.SourceMCP, Name: "get_and_delete"}, Verdict{false, "verb:get"}},
		{"noun after verb does not flip it", Tool{Source: agentruntime.SourceMCP, Name: "list_refunds"}, Verdict{false, "verb:list"}},
		{"camel case", Tool{Source: agentruntime.SourceMCP, Name: "sendEmail"}, Verdict{true, "verb:send"}},
		{"dotted read verb", Tool{Source: agentruntime.SourceMCP, Name: "customers.list"}, Verdict{false, "verb:list"}},
		{"plural verb", Tool{Source: agentruntime.SourceMCP, Name: "deletes_things"}, Verdict{true, "verb:delete"}},
		{"description mutating verb", Tool{Source: agentruntime.SourceMCP, Name: "prod", Description: "Deploy the current build to production. Returns a URL."}, Verdict{true, "verb:deploy"}},
		{"description read verb", Tool{Source: agentruntime.SourceMCP, Name: "prod", Description: "Fetch the current deployment. Then delete nothing."}, Verdict{false, "verb:fetch"}},
		{"name read verb beats description mutating verb", Tool{Source: agentruntime.SourceMCP, Name: "list_refunds", Description: "Create a list of refunds"}, Verdict{false, "verb:list"}},
		{"unknown", Tool{Source: agentruntime.SourceMCP, Name: "frobnicate", Description: "Frobnicates the widget"}, Verdict{false, ReasonUnknown}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Suggest(tc.tool)
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if again := Suggest(tc.tool); again != got {
				t.Fatalf("not deterministic: %+v then %+v", got, again)
			}
		})
	}
}

func TestCatalogVerdicts(t *testing.T) {
	for _, runtime := range agentruntime.All() {
		for _, builtin := range runtime.Builtins {
			verdict := Suggest(Tool{Runtime: runtime.Key, Source: agentruntime.SourceBuiltin, Name: builtin.Name, Kind: builtin.Kind})
			switch builtin.Kind {
			case agentruntime.ToolShell:
				if !verdict.Suggested {
					t.Fatalf("%s/%s: shell tool not suggested", runtime.Key, builtin.Name)
				}
			case agentruntime.ToolEdit:
				if verdict.Suggested {
					t.Fatalf("%s/%s: edit tool suggested", runtime.Key, builtin.Name)
				}
			default:
				if verdict.Suggested {
					t.Fatalf("%s/%s: read tool suggested", runtime.Key, builtin.Name)
				}
			}
		}
	}
	verdicts := SuggestAll([]Tool{{Source: agentruntime.SourceMCP, Name: "delete_x"}, {Source: agentruntime.SourceMCP, Name: "get_x"}})
	if len(verdicts) != 2 || !verdicts[0].Suggested || verdicts[1].Suggested {
		t.Fatalf("SuggestAll: %+v", verdicts)
	}
}

func TestTokens(t *testing.T) {
	got := tokens("mcp__stripe__createRefundV2")
	want := []string{"mcp", "stripe", "create", "refund", "v2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
