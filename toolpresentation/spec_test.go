package toolpresentation

import (
	"errors"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/agentruntime"
)

func TestBuiltinEntriesValidate(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range Builtin() {
		if err := entry.Validate(); err != nil {
			t.Errorf("%s: %v", entry.Key(), err)
		}
		if seen[entry.Key()] {
			t.Errorf("%s: bound twice", entry.Key())
		}
		seen[entry.Key()] = true
	}
}

// Every shell and edit tool a runtime ships with has a presentation, so
// the calls onboarding gates by default never fall back to raw JSON.
func TestBuiltinCoversRuntimeShellAndEditTools(t *testing.T) {
	entries := Builtin()
	for _, runtime := range agentruntime.All() {
		for _, tool := range runtime.Builtins {
			if tool.Kind != agentruntime.ToolShell && tool.Kind != agentruntime.ToolEdit {
				continue
			}
			if _, ok := Resolve(entries, Selector{Runtime: string(runtime.Key), Tool: tool.Name}); !ok {
				t.Errorf("%s/%s has no presentation", runtime.Key, tool.Name)
			}
		}
	}
}

func TestResolvePrefersTheMostSpecificBinding(t *testing.T) {
	entries := []Entry{
		{Tool: "create_issue", Spec: Spec{Title: "any"}},
		{Runtime: "claude-code", Tool: "create_issue", Spec: Spec{Title: "runtime"}},
		{Server: "github", Tool: "create_issue", Spec: Spec{Title: "server"}},
		{Runtime: "claude-code", Server: "github", Tool: "create_issue", Spec: Spec{Title: "both"}},
	}
	cases := []struct {
		selector Selector
		want     string
	}{
		{Selector{Runtime: "claude-code", Server: "github", Tool: "create_issue"}, "both"},
		{Selector{Runtime: "codex", Server: "github", Tool: "create_issue"}, "server"},
		{Selector{Runtime: "claude-code", Server: "linear", Tool: "create_issue"}, "runtime"},
		{Selector{Runtime: "codex", Server: "linear", Tool: "create_issue"}, "any"},
	}
	for _, tc := range cases {
		entry, ok := Resolve(entries, tc.selector)
		if !ok || entry.Spec.Title != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.selector, entry.Spec.Title, tc.want)
		}
	}
	if _, ok := Resolve(entries, Selector{Tool: "other"}); ok {
		t.Errorf("unrelated tool resolved")
	}
}

func TestResolveFirstWinsAmongEquals(t *testing.T) {
	entries := []Entry{
		{Tool: "t", Spec: Spec{Title: "first"}},
		{Tool: "t", Spec: Spec{Title: "second"}},
	}
	entry, _ := Resolve(entries, Selector{Tool: "t"})
	if entry.Spec.Title != "first" {
		t.Errorf("got %q", entry.Spec.Title)
	}
}

func TestSpecValidation(t *testing.T) {
	valid := Spec{Title: "Edit {{/file_path}}", Fields: []Field{
		{Path: "/file_path", Kind: KindPath},
		{Kind: KindDiff, Before: "/old", After: "/new", Label: "Change"},
		{Path: "/edits", Kind: KindList, Item: &Field{Kind: KindDiff, Before: "old", After: "new"}},
		{Path: "/labels", Kind: KindList},
		{Path: "/a~1b~0c", Kind: KindText, Role: RoleDetail},
		{Path: "/content", Kind: KindCode, Language: "python"},
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
	cases := map[string]Spec{
		"long title":           {Title: strings.Repeat("x", MaxTitleLength+1)},
		"multiline title":      {Title: "a\nb"},
		"bad placeholder":      {Title: "Edit {{file_path}}"},
		"too many fields":      {Fields: make([]Field, MaxFields+1)},
		"unknown kind":         {Fields: []Field{{Path: "/a", Kind: "image"}}},
		"missing path":         {Fields: []Field{{Kind: KindText}}},
		"relative path":        {Fields: []Field{{Path: "a", Kind: KindText}}},
		"bad escape":           {Fields: []Field{{Path: "/a~2", Kind: KindText}}},
		"pair on text":         {Fields: []Field{{Kind: KindText, Before: "/a", After: "/b"}}},
		"half pair":            {Fields: []Field{{Kind: KindDiff, Before: "/a"}}},
		"pair with path":       {Fields: []Field{{Path: "/p", Kind: KindDiff, Before: "/a", After: "/b"}}},
		"item on text":         {Fields: []Field{{Path: "/a", Kind: KindText, Item: &Field{Kind: KindText}}}},
		"nested list item":     {Fields: []Field{{Path: "/a", Kind: KindList, Item: &Field{Kind: KindList, Item: &Field{Kind: KindText}}}}},
		"absolute item path":   {Fields: []Field{{Path: "/a", Kind: KindList, Item: &Field{Kind: KindText, Path: "/x"}}}},
		"item role":            {Fields: []Field{{Path: "/a", Kind: KindList, Item: &Field{Kind: KindText, Role: RoleDetail}}}},
		"language on prose":    {Fields: []Field{{Path: "/a", Kind: KindProse, Language: "go"}}},
		"language with spaces": {Fields: []Field{{Path: "/a", Kind: KindCode, Language: "go lang"}}},
		"unknown role":         {Fields: []Field{{Path: "/a", Kind: KindText, Role: "hidden"}}},
		"long label":           {Fields: []Field{{Path: "/a", Kind: KindText, Label: strings.Repeat("x", MaxLabelLength+1)}}},
	}
	for name, spec := range cases {
		err := spec.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v does not wrap ErrInvalid", name, err)
		}
	}
}

func TestEntryValidation(t *testing.T) {
	if err := (Entry{Tool: "Bash", Runtime: "claude-code", Server: "github-mcp"}).Validate(); err != nil {
		t.Fatalf("valid entry refused: %v", err)
	}
	cases := map[string]Entry{
		"empty tool":      {},
		"tool with space": {Tool: "a b"},
		"bad runtime":     {Tool: "t", Runtime: "Claude Code"},
		"bad server":      {Tool: "t", Server: "my server"},
		"bad spec":        {Tool: "t", Spec: Spec{Fields: []Field{{Kind: "nope"}}}},
	}
	for name, entry := range cases {
		if err := entry.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNormalizeFillsThePrimaryRole(t *testing.T) {
	spec := Spec{Title: " T ", Fields: []Field{{Path: "/a", Kind: KindText}, {Path: "/b", Kind: KindText, Role: RoleDetail}}}.Normalize()
	if spec.Title != "T" || spec.Fields[0].Role != RolePrimary || spec.Fields[1].Role != RoleDetail {
		t.Errorf("normalized %+v", spec)
	}
	if (Spec{}).Normalize().Fields == nil {
		t.Errorf("fields stayed nil")
	}
}
