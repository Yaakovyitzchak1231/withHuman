package routing

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/withHumanAI/withHuman/approvalblock"
)

func requestCondition() Condition {
	return Condition{Type: ConditionRequest, When: &approvalblock.Condition{
		ID: uuid.NewString(), Field: "/request/arguments/amount_cents",
		Operator: approvalblock.GreaterThanOrEqual, Value: json.RawMessage(`100000`),
	}}
}

func validPathDocument() PathDocument {
	return PathDocument{
		WorkingHours: []WorkingHoursSet{{
			ID: "uk-office", Name: "UK office hours", Timezone: "Europe/London",
			Intervals: []WeekdayInterval{{Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "18:00"}},
		}},
		Nodes: []Node{
			{ID: uuid.NewString(), Type: NodeDefer, WorkingHoursID: "uk-office", StandardOnly: true},
			{ID: uuid.NewString(), Type: NodeLevel,
				Targets:       []Target{{Type: TargetTeam, ID: uuid.NewString()}},
				EscalateAfter: "15m"},
			{ID: uuid.NewString(), Type: NodeIfElse,
				Conditions: []Condition{
					{Type: ConditionWorkingHours, WorkingHoursID: "uk-office", Active: true},
					{Type: ConditionUrgency, Urgency: UrgencyInterrupt},
					requestCondition(),
				},
				Then: []Node{{ID: uuid.NewString(), Type: NodeLevel,
					Targets:       []Target{{Type: TargetUser, ID: uuid.NewString()}},
					EscalateAfter: "30m", RaiseUrgency: true}},
				Else: []Node{{ID: uuid.NewString(), Type: NodeDefer, WorkingHoursID: "uk-office"}}},
		},
		Repeat: &Repeat{Times: 2, After: "1h"},
	}
}

func TestPathDocumentValidates(t *testing.T) {
	if err := validPathDocument().Validate(); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
}

func TestPathDocumentAcceptsRequestConditionsOnTheMCPServer(t *testing.T) {
	document := validPathDocument()
	document.Nodes[2].Conditions[2].When = &approvalblock.Condition{
		ID: uuid.NewString(), Field: "/request/server",
		Operator: approvalblock.Equals, Value: json.RawMessage(`"stripe"`),
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("request condition on the MCP server rejected: %v", err)
	}
}

func TestPathDocumentRoundTripsThroughJSON(t *testing.T) {
	document := validPathDocument()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded PathDocument
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded document rejected: %v", err)
	}
	if len(decoded.Nodes[2].Conditions) != 3 || decoded.Nodes[2].Conditions[2].When == nil {
		t.Fatalf("conditions lost in the round trip: %+v", decoded.Nodes[2].Conditions)
	}
}

func TestPathDocumentRejections(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*PathDocument)
		message string
	}{
		{"no nodes", func(d *PathDocument) { d.Nodes = nil }, "at least one node"},
		{"duplicate node id", func(d *PathDocument) { d.Nodes[1].ID = d.Nodes[0].ID }, "duplicated"},
		{"non uuid node id", func(d *PathDocument) { d.Nodes[0].ID = "first" }, "must be a UUID"},
		{"unknown working hours on defer", func(d *PathDocument) { d.Nodes[0].WorkingHoursID = "missing" }, "unknown working hours"},
		{"unknown working hours on branch", func(d *PathDocument) { d.Nodes[2].Conditions[0].WorkingHoursID = "missing" }, "unknown working hours"},
		{"level without targets", func(d *PathDocument) { d.Nodes[1].Targets = nil }, "between 1 and"},
		{"level escalate too fast", func(d *PathDocument) { d.Nodes[1].EscalateAfter = "5s" }, "must be a duration"},
		{"level with branch fields", func(d *PathDocument) { d.Nodes[1].WorkingHoursID = "uk-office" }, "another node type"},
		{"unknown target type", func(d *PathDocument) { d.Nodes[1].Targets[0].Type = "schedule" }, "must be user or team"},
		{"branch without conditions", func(d *PathDocument) { d.Nodes[2].Conditions = nil }, "between 1 and"},
		{"branch with legacy working hours", func(d *PathDocument) { d.Nodes[2].WorkingHoursID = "uk-office" }, "another node type"},
		{"unknown condition type", func(d *PathDocument) { d.Nodes[2].Conditions[0].Type = "moon_phase" }, "unknown condition type"},
		{"urgency condition without value", func(d *PathDocument) { d.Nodes[2].Conditions[1].Urgency = "" }, "must test standard or interrupt"},
		{"mixed condition fields", func(d *PathDocument) { d.Nodes[2].Conditions[1].WorkingHoursID = "uk-office" }, "another kind"},
		{"request condition without when", func(d *PathDocument) { d.Nodes[2].Conditions[2].When = nil }, "requires a when clause"},
		{"request condition on a forbidden field", func(d *PathDocument) {
			d.Nodes[2].Conditions[2].When.Field = "/request/context"
		}, "request condition"},
		{"duplicate condition id across branches", func(d *PathDocument) {
			duplicate := requestCondition()
			duplicate.When.ID = d.Nodes[2].Conditions[2].When.ID
			d.Nodes[2].Conditions = append(d.Nodes[2].Conditions, duplicate)
		}, "duplicated across the path"},
		{"bad interval order", func(d *PathDocument) {
			d.WorkingHours[0].Intervals[0].End = "08:00"
		}, "start before it ends"},
		{"bad timezone", func(d *PathDocument) { d.WorkingHours[0].Timezone = "Europe/Atlantis" }, "IANA timezone"},
		{"bad weekday", func(d *PathDocument) {
			d.WorkingHours[0].Intervals[0].Weekdays = []string{"monday"}
		}, "unknown weekday"},
		{"repeat out of range", func(d *PathDocument) { d.Repeat.Times = 10 }, "between 1 and"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := validPathDocument()
			testCase.mutate(&document)
			err := document.Validate()
			if err == nil || !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("Validate() = %v, want error containing %q", err, testCase.message)
			}
		})
	}
}

func TestPathDocumentRejectsExcessBranchDepth(t *testing.T) {
	node := Node{ID: uuid.NewString(), Type: NodeLevel,
		Targets: []Target{{Type: TargetUser, ID: uuid.NewString()}}, EscalateAfter: "15m"}
	for range MaximumBranchDepth {
		node = Node{ID: uuid.NewString(), Type: NodeIfElse,
			Conditions: []Condition{{Type: ConditionUrgency, Urgency: UrgencyStandard}}, Then: []Node{node}}
	}
	document := validPathDocument()
	document.Nodes = []Node{node}
	err := document.Validate()
	if err == nil || !strings.Contains(err.Error(), "nest at most") {
		t.Fatalf("Validate() = %v, want branch depth error", err)
	}
}

func TestPathDocumentTargets(t *testing.T) {
	document := validPathDocument()
	targets := document.Targets()
	if len(targets) != 2 {
		t.Fatalf("Targets() = %v, want the team target and the nested user target", targets)
	}
	if targets[0].Type != TargetTeam || targets[1].Type != TargetUser {
		t.Fatalf("Targets() = %v, want document order (team first, then branch user)", targets)
	}

	duplicated := document.Nodes[1].Targets[0]
	document.Nodes[2].Then[0].Targets = append(document.Nodes[2].Then[0].Targets, duplicated)
	if got := document.Targets(); len(got) != 2 {
		t.Fatalf("Targets() = %v, want duplicates collapsed", got)
	}

	if got := (PathDocument{}).Targets(); len(got) != 0 {
		t.Fatalf("Targets() on an empty document = %v, want none", got)
	}
}

func TestPathDocumentReachableTargets(t *testing.T) {
	requestIs := func(id string) Condition {
		return Condition{Type: ConditionRequest, When: &approvalblock.Condition{ID: id, Field: "/request/tool", Operator: approvalblock.Equals}}
	}
	officeHours := Condition{Type: ConditionWorkingHours, WorkingHoursID: "uk-office", Active: true}
	interrupt := Condition{Type: ConditionUrgency, Urgency: UrgencyInterrupt}
	target := func(id string) Target { return Target{Type: TargetUser, ID: id} }
	level := func(targets ...Target) Node {
		return Node{ID: uuid.NewString(), Type: NodeLevel, Targets: targets, EscalateAfter: "10m"}
	}
	ifElse := func(conditions []Condition, then, otherwise []Node) Node {
		return Node{ID: uuid.NewString(), Type: NodeIfElse, Conditions: conditions, Then: then, Else: otherwise}
	}
	ids := func(targets []Target) []string {
		out := make([]string, 0, len(targets))
		for _, target := range targets {
			out = append(out, target.ID)
		}
		return out
	}
	cases := []struct {
		name  string
		nodes []Node
		facts map[string]bool
		want  []string
	}{
		{name: "a top-level level is always reachable",
			nodes: []Node{level(target("a"))}, want: []string{"a"}},
		{name: "a true request condition reaches then only",
			nodes: []Node{ifElse([]Condition{requestIs("c")}, []Node{level(target("then"))}, []Node{level(target("else"))})},
			facts: map[string]bool{"c": true}, want: []string{"then"}},
		{name: "a false request condition reaches else only",
			nodes: []Node{ifElse([]Condition{requestIs("c")}, []Node{level(target("then"))}, []Node{level(target("else"))})},
			facts: map[string]bool{"c": false}, want: []string{"else"}},
		{name: "a missing fact reads false",
			nodes: []Node{ifElse([]Condition{requestIs("c")}, []Node{level(target("then"))}, []Node{level(target("else"))})},
			want:  []string{"else"}},
		{name: "working hours reach both branches",
			nodes: []Node{ifElse([]Condition{officeHours}, []Node{level(target("then"))}, []Node{level(target("else"))})},
			want:  []string{"then", "else"}},
		{name: "urgency reaches both branches",
			nodes: []Node{ifElse([]Condition{interrupt}, []Node{level(target("then"))}, []Node{level(target("else"))})},
			want:  []string{"then", "else"}},
		{name: "a true request condition beside working hours reaches both",
			nodes: []Node{ifElse([]Condition{requestIs("c"), officeHours}, []Node{level(target("then"))}, []Node{level(target("else"))})},
			facts: map[string]bool{"c": true}, want: []string{"then", "else"}},
		{name: "a false request condition beside working hours reaches else only",
			nodes: []Node{ifElse([]Condition{officeHours, requestIs("c")}, []Node{level(target("then"))}, []Node{level(target("else"))})},
			facts: map[string]bool{"c": false}, want: []string{"else"}},
		{name: "an empty branch reaches nobody on that side",
			nodes: []Node{ifElse([]Condition{requestIs("c")}, []Node{level(target("then"))}, nil)},
			want:  []string{}},
		{name: "nested branches compose",
			nodes: []Node{ifElse([]Condition{requestIs("outer")},
				[]Node{ifElse([]Condition{requestIs("inner")}, []Node{level(target("deep"))}, []Node{level(target("shallow"))})},
				[]Node{level(target("else"))})},
			facts: map[string]bool{"outer": true, "inner": false}, want: []string{"shallow"}},
		{name: "duplicates across branches collapse in walk order",
			nodes: []Node{level(target("a")), ifElse([]Condition{officeHours}, []Node{level(target("a"), target("b"))}, []Node{level(target("b"))})},
			want:  []string{"a", "b"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document := PathDocument{Nodes: test.nodes}
			got := ids(document.ReachableTargets(func(id string) bool { return test.facts[id] }))
			if len(got) != len(test.want) {
				t.Fatalf("ReachableTargets() = %v, want %v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("ReachableTargets() = %v, want %v", got, test.want)
				}
			}
		})
	}
}

func TestPathDocumentMayReachNobody(t *testing.T) {
	requestIs := func(id string) Condition {
		return Condition{Type: ConditionRequest, When: &approvalblock.Condition{ID: id, Field: "/request/tool", Operator: approvalblock.Equals}}
	}
	level := func() Node {
		return Node{ID: uuid.NewString(), Type: NodeLevel, Targets: []Target{{Type: TargetUser, ID: uuid.NewString()}}, EscalateAfter: "10m"}
	}
	ifElse := func(then, otherwise []Node) Node {
		return Node{ID: uuid.NewString(), Type: NodeIfElse, Conditions: []Condition{requestIs(uuid.NewString())}, Then: then, Else: otherwise}
	}
	deferStep := Node{ID: uuid.NewString(), Type: NodeDefer, WorkingHoursID: "uk-office"}
	cases := []struct {
		name  string
		nodes []Node
		want  bool
	}{
		{name: "a single level always reaches someone", nodes: []Node{level()}, want: false},
		{name: "a then-only branch may reach nobody", nodes: []Node{ifElse([]Node{level()}, nil)}, want: true},
		{name: "levels on both sides always reach someone", nodes: []Node{ifElse([]Node{level()}, []Node{level()})}, want: false},
		{name: "a top-level level after a branch covers it", nodes: []Node{ifElse([]Node{level()}, nil), level()}, want: false},
		{name: "only deferrals reach nobody", nodes: []Node{deferStep}, want: true},
		{name: "nested branches must cover every walk",
			nodes: []Node{ifElse([]Node{ifElse([]Node{level()}, nil)}, []Node{level()})}, want: true},
		{name: "nested branches covered on every walk",
			nodes: []Node{ifElse([]Node{ifElse([]Node{level()}, []Node{level()})}, []Node{level()})}, want: false},
		{name: "an empty document reaches nobody", nodes: nil, want: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := (PathDocument{Nodes: test.nodes}).MayReachNobody(); got != test.want {
				t.Fatalf("MayReachNobody() = %v, want %v", got, test.want)
			}
		})
	}
}

func validTeamPolicyDocument() TeamPolicyDocument {
	return TeamPolicyDocument{
		WorkingHours: []WorkingHoursSet{{
			ID: "uk-office", Name: "UK office hours", Timezone: "Europe/London",
			Intervals: []WeekdayInterval{{Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "18:00"}},
		}},
		Nodes: []Node{
			{ID: uuid.NewString(), Type: NodeLevel,
				Targets:       []Target{{Type: TargetUser, ID: uuid.NewString()}},
				EscalateAfter: "10m"},
			{ID: uuid.NewString(), Type: NodeIfElse,
				Conditions: []Condition{{Type: ConditionWorkingHours, WorkingHoursID: "uk-office", Active: false}},
				Then: []Node{{ID: uuid.NewString(), Type: NodeLevel,
					Targets: []Target{{Type: TargetBroadcast}}, EscalateAfter: "30m", RaiseUrgency: true}},
			},
		},
	}
}

func TestTeamPolicyDocumentValidates(t *testing.T) {
	if err := validTeamPolicyDocument().Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	encoded, err := json.Marshal(validTeamPolicyDocument())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), `"type":"broadcast","id"`) {
		t.Fatalf("broadcast target serialized an id: %s", encoded)
	}
	var decoded TeamPolicyDocument
	if err := Decode(encoded, &decoded); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded policy rejected: %v", err)
	}
}

func TestTargetKindsAreEnforcedPerDocument(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*TeamPolicyDocument, *PathDocument)
		policy string
		path   string
	}{
		{"broadcast in a path", func(_ *TeamPolicyDocument, path *PathDocument) {
			path.Nodes[1].Targets = []Target{{Type: TargetBroadcast}}
		}, "", "broadcast targets belong to a team escalation policy"},
		{"team in a policy", func(policy *TeamPolicyDocument, _ *PathDocument) {
			policy.Nodes[0].Targets = []Target{{Type: TargetTeam, ID: uuid.NewString()}}
		}, "cannot target other teams", ""},
		{"broadcast with an id", func(policy *TeamPolicyDocument, _ *PathDocument) {
			policy.Nodes[0].Targets = []Target{{Type: TargetBroadcast, ID: uuid.NewString()}}
		}, "carries no id", ""},
		{"reserved round robin", func(policy *TeamPolicyDocument, path *PathDocument) {
			policy.Nodes[0].Targets = []Target{{Type: TargetRoundRobin}}
			path.Nodes[1].Targets = []Target{{Type: TargetRoundRobin}}
		}, "reserved", "reserved"},
		{"reserved on call", func(policy *TeamPolicyDocument, path *PathDocument) {
			policy.Nodes[0].Targets = []Target{{Type: TargetOnCall}}
			path.Nodes[1].Targets = []Target{{Type: TargetOnCall}}
		}, "reserved", "reserved"},
		{"two broadcasts in one level", func(policy *TeamPolicyDocument, _ *PathDocument) {
			policy.Nodes[0].Targets = []Target{{Type: TargetBroadcast}, {Type: TargetBroadcast}}
		}, "repeats a target", ""},
		{"user without an id", func(policy *TeamPolicyDocument, path *PathDocument) {
			policy.Nodes[0].Targets = []Target{{Type: TargetUser}}
			path.Nodes[1].Targets = []Target{{Type: TargetUser}}
		}, "must be a UUID", "must be a UUID"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			policy, path := validTeamPolicyDocument(), validPathDocument()
			testCase.mutate(&policy, &path)
			policyErr, pathErr := policy.Validate(), path.Validate()
			if testCase.policy == "" && policyErr != nil {
				t.Fatalf("policy rejected: %v", policyErr)
			}
			if testCase.policy != "" && (policyErr == nil || !strings.Contains(policyErr.Error(), testCase.policy)) {
				t.Fatalf("policy error = %v, want %q", policyErr, testCase.policy)
			}
			if testCase.path == "" && pathErr != nil {
				t.Fatalf("path rejected: %v", pathErr)
			}
			if testCase.path != "" && (pathErr == nil || !strings.Contains(pathErr.Error(), testCase.path)) {
				t.Fatalf("path error = %v, want %q", pathErr, testCase.path)
			}
		})
	}
}

func TestTeamPolicyDocumentTargets(t *testing.T) {
	policy := validTeamPolicyDocument()
	targets := policy.Targets()
	if len(targets) != 2 || targets[0].Type != TargetUser || targets[1].Type != TargetBroadcast {
		t.Fatalf("Targets() = %v, want the user then the broadcast marker", targets)
	}
	if !policy.Broadcasts() {
		t.Fatal("Broadcasts() = false, want true")
	}
	if ids := policy.UserTargetIDs(); len(ids) != 1 || ids[0] != targets[0].ID {
		t.Fatalf("UserTargetIDs() = %v", ids)
	}
	ordinals := policy.LevelOrdinals()
	if ordinals.Count != 2 || ordinals.Ordinal[policy.Nodes[0].ID] != 1 || ordinals.Ordinal[policy.Nodes[1].Then[0].ID] != 2 {
		t.Fatalf("LevelOrdinals() = %+v", ordinals)
	}
}

func TestDecodeRefusesUnknownFieldsAndTrailingContent(t *testing.T) {
	var document PathDocument
	if err := Decode(json.RawMessage(`{"working_hours":[],"nodes":[],"extra":1}`), &document); err == nil {
		t.Fatal("Decode accepted an unknown field")
	}
	if err := Decode(json.RawMessage(`{"working_hours":[],"nodes":[]} {}`), &document); err == nil {
		t.Fatal("Decode accepted trailing content")
	}
}
