// Package toolsuggest decides which of an agent's tools onboarding should
// propose sending through a human. The rules are deterministic and
// explainable: every verdict carries a reason code the page shows next
// to the suggestion, and the same input always yields the same answer.
package toolsuggest

import (
	"strings"
	"unicode"

	"github.com/withHumanAI/withHuman/agentruntime"
)

// Annotations are the MCP tool annotations a server advertised. Nil
// pointers mean the server said nothing.
type Annotations struct {
	ReadOnlyHint    *bool
	DestructiveHint *bool
	IdempotentHint  *bool
	OpenWorldHint   *bool
}

// Tool is one discovered tool, from a runtime's built-in catalog or from
// an MCP server.
type Tool struct {
	Runtime     agentruntime.Key
	Name        string
	Description string
	Source      agentruntime.Source
	Server      string
	// Kind is set for built-in tools from the catalog.
	Kind        agentruntime.ToolKind
	Annotations *Annotations
}

// Verdict is the suggestion and the reason code behind it.
type Verdict struct {
	Suggested bool
	Reason    string
}

// Reason codes. The page maps them to prose.
const (
	ReasonShell                 = "shell"
	ReasonEditNotSuggested      = "edit_not_suggested"
	ReasonReadOnlyBuiltin       = "read_only_builtin"
	ReasonAnnotationReadOnly    = "annotation_read_only"
	ReasonAnnotationDestructive = "annotation_destructive"
	ReasonAnnotationMutating    = "annotation_mutating"
	ReasonUnknown               = "unknown"
	verbPrefix                  = "verb:"
)

var mutatingVerbs = words("delete remove destroy drop purge send email post publish deploy release push merge pay refund charge transfer withdraw execute exec run create insert upsert update write set patch put cancel revoke grant approve reject kill terminate restart shutdown rotate reset")

// irreversibleVerbs are the mutating verbs whose effect cannot be taken
// back: data gone, a message sent, money moved, a release out.
var irreversibleVerbs = words("delete remove destroy drop purge send email post publish deploy release push merge pay refund charge transfer withdraw cancel revoke kill terminate shutdown rotate reset")

var readVerbs = words("get list read search fetch describe find query show view count check lookup retrieve status preview validate ping echo head")

func words(list string) map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(list) {
		out[word] = true
	}
	return out
}

// Suggest applies the rules in order; the first that matches wins.
//
//  1. Built-in tools go by their catalog kind: shell tools are suggested,
//     edit tools are not by default, read and other tools are not.
//  2. MCP annotations: read-only is not suggested; destructive is; an
//     explicit read_only_hint of false is.
//  3. Verbs in the name, then in the first sentence of the description:
//     the first token that is a known verb decides, a mutating verb
//     suggests and a read verb does not. Names lead with their verb
//     (list_refunds, create_refund), so position beats severity.
//  4. Otherwise not suggested.
func Suggest(tool Tool) Verdict {
	if tool.Source == agentruntime.SourceBuiltin {
		switch tool.Kind {
		case agentruntime.ToolShell:
			return Verdict{Suggested: true, Reason: ReasonShell}
		case agentruntime.ToolEdit:
			return Verdict{Suggested: false, Reason: ReasonEditNotSuggested}
		default:
			return Verdict{Suggested: false, Reason: ReasonReadOnlyBuiltin}
		}
	}
	if annotations := tool.Annotations; annotations != nil {
		switch {
		case annotations.ReadOnlyHint != nil && *annotations.ReadOnlyHint:
			return Verdict{Suggested: false, Reason: ReasonAnnotationReadOnly}
		case annotations.DestructiveHint != nil && *annotations.DestructiveHint:
			return Verdict{Suggested: true, Reason: ReasonAnnotationDestructive}
		case annotations.ReadOnlyHint != nil && !*annotations.ReadOnlyHint:
			return Verdict{Suggested: true, Reason: ReasonAnnotationMutating}
		}
	}
	if verdict, ok := verbVerdict(tokens(tool.Name)); ok {
		return verdict
	}
	if verdict, ok := verbVerdict(tokens(firstSentence(tool.Description))); ok {
		return verdict
	}
	return Verdict{Suggested: false, Reason: ReasonUnknown}
}

// verbVerdict finds the first token that is a known verb and answers by
// which set it belongs to.
func verbVerdict(tokens []string) (Verdict, bool) {
	for _, token := range tokens {
		for _, candidate := range []string{token, strings.TrimSuffix(token, "s")} {
			if mutatingVerbs[candidate] {
				return Verdict{Suggested: true, Reason: verbPrefix + candidate}, true
			}
			if readVerbs[candidate] {
				return Verdict{Suggested: false, Reason: verbPrefix + candidate}, true
			}
		}
	}
	return Verdict{}, false
}

// Irreversible reports whether calling the tool may do something that
// cannot be undone: the server marks it destructive, or any word of its
// name is one whose effect stays (delete_repository, send_email,
// create_refund). It errs towards yes: onboarding never asks an agent to
// call such a tool as a test, and a tool wrongly kept out costs nothing.
func Irreversible(tool Tool) bool {
	if tool.Annotations != nil && tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
		return true
	}
	for _, token := range tokens(tool.Name) {
		if irreversibleVerbs[token] || irreversibleVerbs[strings.TrimSuffix(token, "s")] {
			return true
		}
	}
	return false
}

// SuggestAll judges every tool in order.
func SuggestAll(tools []Tool) []Verdict {
	verdicts := make([]Verdict, len(tools))
	for index, tool := range tools {
		verdicts[index] = Suggest(tool)
	}
	return verdicts
}

// tokens splits on separators and camel case and lower-cases the result.
func tokens(value string) []string {
	var out []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			out = append(out, strings.ToLower(current.String()))
			current.Reset()
		}
	}
	runes := []rune(value)
	for index, r := range runes {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.IsUpper(r) && index > 0 && unicode.IsLower(runes[index-1]) {
				flush()
			}
			current.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

func firstSentence(description string) string {
	description = strings.TrimSpace(description)
	for index, r := range description {
		if r == '.' || r == '\n' || r == '!' || r == '?' {
			return description[:index]
		}
	}
	return description
}
