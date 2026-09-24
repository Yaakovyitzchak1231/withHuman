package escalation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/routing"
)

const (
	teamAlpha domain.ID = "team-alpha"
	teamBeta  domain.ID = "team-beta"
)

var (
	// monday is 2026-09-14 08:00 UTC, 09:00 BST: the London office just opened.
	monday = utc(2026, time.September, 14, 8, 0)
	// fridayEvening is 2026-09-11 18:00 UTC, after the London office closed.
	fridayEvening = utc(2026, time.September, 11, 18, 0)
	// mondayOpen is when the London office next opens after fridayEvening.
	mondayOpen = utc(2026, time.September, 14, 8, 0)
)

func user(id string) routing.Target { return routing.Target{Type: routing.TargetUser, ID: id} }
func team(id domain.ID) routing.Target {
	return routing.Target{Type: routing.TargetTeam, ID: string(id)}
}
func broadcast() routing.Target { return routing.Target{Type: routing.TargetBroadcast} }

func level(id, after string, targets ...routing.Target) routing.Node {
	return routing.Node{ID: id, Type: routing.NodeLevel, Targets: targets, EscalateAfter: after}
}

func raisingLevel(id, after string, targets ...routing.Target) routing.Node {
	node := level(id, after, targets...)
	node.RaiseUrgency = true
	return node
}

func ifElse(id string, conditions []routing.Condition, then, otherwise []routing.Node) routing.Node {
	return routing.Node{ID: id, Type: routing.NodeIfElse, Conditions: conditions, Then: then, Else: otherwise}
}

func deferNode(id, workingHoursID string, standardOnly bool) routing.Node {
	return routing.Node{ID: id, Type: routing.NodeDefer, WorkingHoursID: workingHoursID, StandardOnly: standardOnly}
}

func workingHours(id string, active bool) routing.Condition {
	return routing.Condition{Type: routing.ConditionWorkingHours, WorkingHoursID: id, Active: active}
}

func urgencyIs(urgency routing.Urgency) routing.Condition {
	return routing.Condition{Type: routing.ConditionUrgency, Urgency: urgency}
}

func requestIs(conditionID string) routing.Condition {
	return routing.Condition{Type: routing.ConditionRequest, When: &approvalblock.Condition{ID: conditionID, Field: "/request/tool", Operator: approvalblock.Equals}}
}

func path(nodes ...routing.Node) routing.PathDocument {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	return routing.PathDocument{WorkingHours: []routing.WorkingHoursSet{londonOfficeHours(weekdays...)}, Nodes: nodes}
}

func policy(nodes ...routing.Node) routing.TeamPolicyDocument {
	return routing.TeamPolicyDocument{Nodes: nodes}
}

func program(document routing.PathDocument, teams map[domain.ID]routing.TeamPolicyDocument) Program {
	return ProgramFromDocuments(document, teams, routing.UrgencyStandard)
}

// trace compresses effects into "kind[scope]:node" tokens so tests can
// assert document order in one comparison.
func trace(effects []Effect) []string {
	tokens := make([]string, 0, len(effects))
	for _, effect := range effects {
		token := string(effect.Kind)
		if effect.Scope != "" {
			token += "[" + string(effect.Scope) + "]"
		}
		switch effect.Kind {
		case EffectRepeat:
			token += fmt.Sprintf(":%d", effect.Iteration)
		case EffectExhausted:
		default:
			token += ":" + effect.NodeID
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func expectTrace(t *testing.T, effects []Effect, want ...string) {
	t.Helper()
	got := trace(effects)
	// A nil expectation means no effects; trace always returns a slice.
	if want == nil {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effects\n got %s\nwant %s", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func expectWake(t *testing.T, m *Machine, want time.Time) {
	t.Helper()
	got, ok := m.NextWake()
	if !ok || !got.Equal(want) {
		t.Fatalf("NextWake() = %s, %v; want %s", got, ok, want)
	}
}

func expectNoWake(t *testing.T, m *Machine) {
	t.Helper()
	if got, ok := m.NextWake(); ok {
		t.Fatalf("NextWake() = %s, want nothing armed", got)
	}
}

func TestLinearLevelsFireInOrder(t *testing.T) {
	m := NewMachine(program(path(
		level("l1", "10m", user("u1")),
		level("l2", "20m", user("u2")),
		level("l3", "30m", user("u3")),
	), nil))

	effects := m.Start(monday)
	expectTrace(t, effects, "level_entered:l1")
	if effects[0].LevelOrdinal != 1 || effects[0].LevelCount != 3 || effects[0].Urgency != routing.UrgencyStandard {
		t.Fatalf("first level effect: %+v", effects[0])
	}
	if !reflect.DeepEqual(effects[0].Targets, []routing.Target{user("u1")}) {
		t.Fatalf("first level targets: %+v", effects[0].Targets)
	}
	expectWake(t, m, monday.Add(10*time.Minute))

	// Ticking early does nothing.
	expectTrace(t, m.Tick(monday.Add(9*time.Minute)))

	effects = m.Tick(monday.Add(10 * time.Minute))
	expectTrace(t, effects, "level_entered:l2")
	if effects[0].LevelOrdinal != 2 {
		t.Fatalf("second level ordinal: %d", effects[0].LevelOrdinal)
	}
	expectWake(t, m, monday.Add(30*time.Minute))

	expectTrace(t, m.Tick(monday.Add(30*time.Minute)), "level_entered:l3")
	expectWake(t, m, monday.Add(60*time.Minute))
	if m.Done() {
		t.Fatal("machine reported done with a level armed")
	}

	expectTrace(t, m.Tick(monday.Add(60*time.Minute)), "exhausted")
	expectNoWake(t, m)
	if !m.Done() {
		t.Fatal("machine did not report done after exhausting")
	}
}

func TestIfElseConditions(t *testing.T) {
	tests := []struct {
		name      string
		now       time.Time
		nodes     []routing.Node
		facts     map[string]bool
		wantTrace []string
		wantTicks []string
	}{
		{
			name: "working hours open takes then",
			now:  monday,
			nodes: []routing.Node{ifElse("if", []routing.Condition{workingHours("office", true)},
				[]routing.Node{level("day", "10m", user("u1"))}, []routing.Node{level("night", "10m", user("u2"))})},
			wantTrace: []string{"level_entered:day"},
		},
		{
			name: "working hours closed on friday evening takes else",
			now:  fridayEvening,
			nodes: []routing.Node{ifElse("if", []routing.Condition{workingHours("office", true)},
				[]routing.Node{level("day", "10m", user("u1"))}, []routing.Node{level("night", "10m", user("u2"))})},
			wantTrace: []string{"level_entered:night"},
		},
		{
			name: "working hours closed over the weekend takes else",
			now:  utc(2026, time.September, 13, 10, 0),
			nodes: []routing.Node{ifElse("if", []routing.Condition{workingHours("office", true)},
				[]routing.Node{level("day", "10m", user("u1"))}, []routing.Node{level("night", "10m", user("u2"))})},
			wantTrace: []string{"level_entered:night"},
		},
		{
			name: "inactive condition inverts the window",
			now:  fridayEvening,
			nodes: []routing.Node{ifElse("if", []routing.Condition{workingHours("office", false)},
				[]routing.Node{level("closed", "10m", user("u1"))}, []routing.Node{level("open", "10m", user("u2"))})},
			wantTrace: []string{"level_entered:closed"},
		},
		{
			name: "urgency condition before raise takes else",
			now:  monday,
			nodes: []routing.Node{ifElse("if", []routing.Condition{urgencyIs(routing.UrgencyInterrupt)},
				[]routing.Node{level("loud", "10m", user("u1"))}, []routing.Node{level("quiet", "10m", user("u2"))})},
			wantTrace: []string{"level_entered:quiet"},
		},
		{
			name: "request fact true takes then",
			now:  monday,
			nodes: []routing.Node{ifElse("if", []routing.Condition{requestIs("c1")},
				[]routing.Node{level("matched", "10m", user("u1"))}, []routing.Node{level("other", "10m", user("u2"))})},
			facts:     map[string]bool{"/c1": true},
			wantTrace: []string{"level_entered:matched"},
		},
		{
			name: "missing request fact is false",
			now:  monday,
			nodes: []routing.Node{ifElse("if", []routing.Condition{requestIs("c1")},
				[]routing.Node{level("matched", "10m", user("u1"))}, []routing.Node{level("other", "10m", user("u2"))})},
			wantTrace: []string{"level_entered:other"},
		},
		{
			name: "every condition must hold",
			now:  monday,
			nodes: []routing.Node{ifElse("if", []routing.Condition{workingHours("office", true), requestIs("c1")},
				[]routing.Node{level("both", "10m", user("u1"))}, []routing.Node{level("not-both", "10m", user("u2"))})},
			facts:     map[string]bool{"/c1": false},
			wantTrace: []string{"level_entered:not-both"},
		},
		{
			name: "empty then branch falls through",
			now:  monday,
			nodes: []routing.Node{
				ifElse("if", []routing.Condition{workingHours("office", true)}, nil, []routing.Node{level("night", "10m", user("u2"))}),
				level("after", "10m", user("u3")),
			},
			wantTrace: []string{"level_entered:after"},
		},
		{
			name: "empty else branch falls through",
			now:  fridayEvening,
			nodes: []routing.Node{
				ifElse("if", []routing.Condition{workingHours("office", true)}, []routing.Node{level("day", "10m", user("u1"))}, nil),
				level("after", "10m", user("u3")),
			},
			wantTrace: []string{"level_entered:after"},
		},
		{
			name: "nested branches walk in document order",
			now:  monday,
			nodes: []routing.Node{
				ifElse("outer", []routing.Condition{workingHours("office", true)}, []routing.Node{
					level("n1", "1m", user("u1")),
					ifElse("inner", []routing.Condition{urgencyIs(routing.UrgencyStandard)}, []routing.Node{level("n2", "1m", user("u2"))}, nil),
					level("n3", "1m", user("u3")),
				}, nil),
				level("after", "10m", user("u3")),
			},
			wantTrace: []string{"level_entered:n1"},
			wantTicks: []string{"level_entered:n2", "level_entered:n3", "level_entered:after"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program := program(path(test.nodes...), nil)
			if test.facts != nil {
				program.Facts = test.facts
			}
			m := NewMachine(program)
			expectTrace(t, m.Start(test.now), test.wantTrace...)
			for index, want := range test.wantTicks {
				expectTrace(t, m.Tick(test.now.Add(time.Duration(index+1)*time.Minute)), want)
			}
		})
	}
}

func TestUrgencyConditionAfterRaise(t *testing.T) {
	m := NewMachine(program(path(
		raisingLevel("l1", "10m", user("u1")),
		ifElse("if", []routing.Condition{urgencyIs(routing.UrgencyInterrupt)},
			[]routing.Node{level("loud", "10m", user("u2"))}, []routing.Node{level("quiet", "10m", user("u3"))}),
	), nil))

	effects := m.Start(monday)
	expectTrace(t, effects, "urgency_raised:l1", "level_entered:l1")
	if effects[0].Urgency != routing.UrgencyInterrupt || effects[1].Urgency != routing.UrgencyInterrupt {
		t.Fatalf("raised urgency not carried on effects: %+v", effects)
	}
	if m.Urgency() != routing.UrgencyInterrupt {
		t.Fatalf("Urgency() = %s", m.Urgency())
	}
	expectTrace(t, m.Tick(monday.Add(10*time.Minute)), "level_entered:loud")
}

func TestRaiseUrgencyIsMonotonic(t *testing.T) {
	m := NewMachine(program(path(
		raisingLevel("l1", "10m", user("u1")),
		raisingLevel("l2", "10m", user("u2")),
	), nil))
	expectTrace(t, m.Start(monday), "urgency_raised:l1", "level_entered:l1")
	// A second raise on an already-interrupt request is silent.
	expectTrace(t, m.Tick(monday.Add(10*time.Minute)), "level_entered:l2")

	// Starting at interrupt never raises.
	document := path(raisingLevel("l1", "10m", user("u1")))
	interrupt := NewMachine(ProgramFromDocuments(document, nil, routing.UrgencyInterrupt))
	expectTrace(t, interrupt.Start(monday), "level_entered:l1")
}

func TestDeferNode(t *testing.T) {
	everyDay := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	londonAllWeek := routing.PathDocument{
		WorkingHours: []routing.WorkingHoursSet{londonOfficeHours(everyDay...)},
		Nodes:        []routing.Node{deferNode("d", "office", false), level("l1", "10m", user("u1"))},
	}
	tests := []struct {
		name      string
		program   Program
		now       time.Time
		wantTrace []string
		wantUntil time.Time
	}{
		{
			name:      "window open is a no-op",
			program:   program(path(deferNode("d", "office", false), level("l1", "10m", user("u1"))), nil),
			now:       monday,
			wantTrace: []string{"level_entered:l1"},
		},
		{
			name:      "friday evening parks until monday morning",
			program:   program(path(deferNode("d", "office", false), level("l1", "10m", user("u1"))), nil),
			now:       fridayEvening,
			wantTrace: []string{"deferred:d"},
			wantUntil: mondayOpen,
		},
		{
			name:      "parks across the london spring change",
			program:   program(londonAllWeek, nil),
			now:       utc(2026, time.March, 28, 20, 0),
			wantTrace: []string{"deferred:d"},
			wantUntil: utc(2026, time.March, 29, 8, 0),
		},
		{
			name:      "standard only defer is skipped at interrupt",
			program:   ProgramFromDocuments(path(deferNode("d", "office", true), level("l1", "10m", user("u1"))), nil, routing.UrgencyInterrupt),
			now:       fridayEvening,
			wantTrace: []string{"level_entered:l1"},
		},
		{
			name:      "standard only defer still parks a standard request",
			program:   program(path(deferNode("d", "office", true), level("l1", "10m", user("u1"))), nil),
			now:       fridayEvening,
			wantTrace: []string{"deferred:d"},
			wantUntil: mondayOpen,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewMachine(test.program)
			effects := m.Start(test.now)
			expectTrace(t, effects, test.wantTrace...)
			if test.wantUntil.IsZero() {
				return
			}
			if !effects[0].Until.Equal(test.wantUntil) {
				t.Fatalf("deferred until %s, want %s", effects[0].Until.UTC(), test.wantUntil)
			}
			expectWake(t, m, test.wantUntil)
			progress := m.Progress(1, test.now)
			if progress.Organization.Phase != application.EscalationPhaseDeferred || progress.Organization.CurrentNodeID != "d" {
				t.Fatalf("progress while deferred: %+v", progress.Organization)
			}
			// The window opening continues into the level.
			effects = m.Tick(test.wantUntil)
			expectTrace(t, effects, "level_entered:l1")
			expectWake(t, m, test.wantUntil.Add(10*time.Minute))
		})
	}
}

func TestRepeatRerunsThenExhausts(t *testing.T) {
	document := path(raisingLevel("l1", "10m", user("u1")), level("l2", "10m", user("u2")))
	document.Repeat = &routing.Repeat{Times: 2, After: "5m"}
	m := NewMachine(program(document, nil))

	now := monday
	expectTrace(t, m.Start(now), "urgency_raised:l1", "level_entered:l1")
	now = now.Add(10 * time.Minute)
	expectTrace(t, m.Tick(now), "level_entered:l2")
	now = now.Add(10 * time.Minute)
	effects := m.Tick(now)
	expectTrace(t, effects, "repeat:1")
	if effects[0].After != 5*time.Minute {
		t.Fatalf("repeat after = %s", effects[0].After)
	}
	expectWake(t, m, now.Add(5*time.Minute))
	progress := m.Progress(1, now)
	if progress.Organization.Phase != application.EscalationPhaseRepeatWait || progress.Organization.Repeat != 1 || progress.Organization.CurrentNodeID != "l2" {
		t.Fatalf("progress while waiting to repeat: %+v", progress.Organization)
	}

	// The second pass keeps interrupt urgency and does not raise again.
	now = now.Add(5 * time.Minute)
	effects = m.Tick(now)
	expectTrace(t, effects, "level_entered:l1")
	if effects[0].Urgency != routing.UrgencyInterrupt || effects[0].Repeat != 1 {
		t.Fatalf("repeated level: %+v", effects[0])
	}
	now = now.Add(10 * time.Minute)
	expectTrace(t, m.Tick(now), "level_entered:l2")
	now = now.Add(10 * time.Minute)
	expectTrace(t, m.Tick(now), "repeat:2")
	now = now.Add(5 * time.Minute)
	expectTrace(t, m.Tick(now), "level_entered:l1")
	now = now.Add(10 * time.Minute)
	expectTrace(t, m.Tick(now), "level_entered:l2")
	now = now.Add(10 * time.Minute)
	expectTrace(t, m.Tick(now), "exhausted")
	if !m.Done() {
		t.Fatal("not done after the last repeat")
	}
}

func TestRepeatReevaluatesWorkingHours(t *testing.T) {
	document := path(
		ifElse("if", []routing.Condition{workingHours("office", true)},
			[]routing.Node{level("day", "10m", user("u1"))}, []routing.Node{level("night", "10m", user("u2"))}),
	)
	document.Repeat = &routing.Repeat{Times: 1, After: "1m"}
	m := NewMachine(program(document, nil))

	// 16:55 BST: open on the first pass, closed when the repeat fires at 17:01.
	now := utc(2026, time.September, 14, 15, 55)
	expectTrace(t, m.Start(now), "level_entered:day")
	now = now.Add(10 * time.Minute)
	expectTrace(t, m.Tick(now), "repeat:1")
	now = now.Add(time.Minute)
	expectTrace(t, m.Tick(now), "level_entered:night")
}

func TestTeamWithPolicyStartsChildInTheSameTick(t *testing.T) {
	teams := map[domain.ID]routing.TeamPolicyDocument{
		teamAlpha: policy(level("a1", "5m", user("a-user")), level("a2", "5m", broadcast())),
	}
	m := NewMachine(program(path(
		level("l1", "30m", user("u1"), team(teamAlpha), team(teamBeta)),
		level("l2", "30m", user("u2")),
	), teams))

	effects := m.Start(monday)
	expectTrace(t, effects, "level_entered:l1", "level_entered[team-alpha]:a1")
	// The policy team leaves the org level; the policy-less team stays.
	if !reflect.DeepEqual(effects[0].Targets, []routing.Target{user("u1"), team(teamBeta)}) {
		t.Fatalf("org level targets: %+v", effects[0].Targets)
	}
	if !reflect.DeepEqual(effects[1].Targets, []routing.Target{user("a-user")}) || effects[1].LevelOrdinal != 1 || effects[1].LevelCount != 2 {
		t.Fatalf("team level effect: %+v", effects[1])
	}
	// NextWake is the minimum across walkers: the child's 5m beats the org's 30m.
	expectWake(t, m, monday.Add(5*time.Minute))

	// The child's timer fires on its own; the org walker is untouched.
	effects = m.Tick(monday.Add(5 * time.Minute))
	expectTrace(t, effects, "level_entered[team-alpha]:a2")
	if !reflect.DeepEqual(effects[0].Targets, []routing.Target{broadcast()}) {
		t.Fatalf("broadcast target did not pass through: %+v", effects[0].Targets)
	}
	expectWake(t, m, monday.Add(10*time.Minute))
	expectTrace(t, m.Tick(monday.Add(10*time.Minute)), "exhausted[team-alpha]")
	expectWake(t, m, monday.Add(30*time.Minute))
	if m.Done() {
		t.Fatal("machine done while the org walker still runs")
	}

	expectTrace(t, m.Tick(monday.Add(30*time.Minute)), "level_entered:l2")
	expectTrace(t, m.Tick(monday.Add(60*time.Minute)), "exhausted")
	if !m.Done() {
		t.Fatal("machine not done after every walker exhausted")
	}
}

func TestOrgTimerLeavesChildrenRunning(t *testing.T) {
	teams := map[domain.ID]routing.TeamPolicyDocument{
		teamAlpha: policy(level("a1", "60m", user("a-user")), level("a2", "60m", broadcast())),
	}
	m := NewMachine(program(path(
		level("l1", "10m", team(teamAlpha)),
		level("l2", "10m", user("u2")),
	), teams))

	expectTrace(t, m.Start(monday), "level_entered:l1", "level_entered[team-alpha]:a1")
	expectTrace(t, m.Tick(monday.Add(10*time.Minute)), "level_entered:l2")
	expectTrace(t, m.Tick(monday.Add(20*time.Minute)), "exhausted")
	// The org walker is finished but the child still owns its timer.
	if m.Done() {
		t.Fatal("machine done while the child still runs")
	}
	expectWake(t, m, monday.Add(60*time.Minute))
	expectTrace(t, m.Tick(monday.Add(60*time.Minute)), "level_entered[team-alpha]:a2")
	expectTrace(t, m.Tick(monday.Add(120*time.Minute)), "exhausted[team-alpha]")
	if !m.Done() {
		t.Fatal("machine not done after the child exhausted")
	}
}

func TestRunningChildIsNotRestartedButFinishedOneIs(t *testing.T) {
	teams := map[domain.ID]routing.TeamPolicyDocument{
		teamAlpha: policy(level("a1", "15m", user("a-user"))),
	}
	m := NewMachine(program(path(
		level("l1", "10m", team(teamAlpha)),
		level("l2", "10m", team(teamAlpha)),
		level("l3", "10m", team(teamAlpha)),
	), teams))

	expectTrace(t, m.Start(monday), "level_entered:l1", "level_entered[team-alpha]:a1")
	// l2 names the team while its walker is still on a1: no restart.
	expectTrace(t, m.Tick(monday.Add(10*time.Minute)), "level_entered:l2")
	// The child finishes at 15m on its own timer.
	expectTrace(t, m.Tick(monday.Add(15*time.Minute)), "exhausted[team-alpha]")
	// l3 names the finished team: it starts again, in the same tick.
	effects := m.Tick(monday.Add(20 * time.Minute))
	expectTrace(t, effects, "level_entered:l3", "level_entered[team-alpha]:a1")
	progress := m.Progress(1, monday.Add(20*time.Minute))
	if len(progress.Teams) != 1 || progress.Teams[0].Phase != application.EscalationPhaseLevel {
		t.Fatalf("restarted team progress: %+v", progress.Teams)
	}
	expectWake(t, m, monday.Add(30*time.Minute))
}

func TestTeamTargetInsideTeamPolicyIsSkipped(t *testing.T) {
	teams := map[domain.ID]routing.TeamPolicyDocument{
		teamAlpha: policy(level("a1", "5m", team(teamBeta), user("a-user"))),
		teamBeta:  policy(level("b1", "5m", user("b-user"))),
	}
	m := NewMachine(program(path(level("l1", "30m", team(teamAlpha))), teams))
	effects := m.Start(monday)
	expectTrace(t, effects, "level_entered:l1", "level_entered[team-alpha]:a1")
	if !reflect.DeepEqual(effects[1].Targets, []routing.Target{user("a-user")}) {
		t.Fatalf("team target leaked through a team level: %+v", effects[1].Targets)
	}
}

func TestPauseFreezesLevelAndRepeatButNotDefer(t *testing.T) {
	t.Run("level remainder survives the pause", func(t *testing.T) {
		teams := map[domain.ID]routing.TeamPolicyDocument{
			teamAlpha: policy(level("a1", "20m", user("a-user")), level("a2", "20m", broadcast())),
		}
		m := NewMachine(program(path(level("l1", "10m", team(teamAlpha)), level("l2", "10m", user("u2"))), teams))
		expectTrace(t, m.Start(monday), "level_entered:l1", "level_entered[team-alpha]:a1")

		m.Pause(monday.Add(4 * time.Minute))
		if !m.Paused() {
			t.Fatal("Paused() false after Pause")
		}
		expectNoWake(t, m)
		progress := m.Progress(1, monday.Add(4*time.Minute))
		if progress.Organization.Phase != application.EscalationPhasePaused || !progress.Organization.NextAt.IsZero() {
			t.Fatalf("org progress while paused: %+v", progress.Organization)
		}
		if progress.Teams[0].Phase != application.EscalationPhasePaused {
			t.Fatalf("team progress while paused: %+v", progress.Teams[0])
		}
		// Nothing fires while paused, however late the tick.
		expectTrace(t, m.Tick(monday.Add(time.Hour)))

		// Resume an hour later: 6 minutes remained on the org, 16 on the team.
		resumed := monday.Add(64 * time.Minute)
		expectTrace(t, m.Resume(resumed))
		if m.Paused() {
			t.Fatal("Paused() true after Resume")
		}
		expectWake(t, m, resumed.Add(6*time.Minute))
		expectTrace(t, m.Tick(resumed.Add(6*time.Minute)), "level_entered:l2")
		// l2's 10 minutes and the team's 16 land together; the org fires first.
		expectTrace(t, m.Tick(resumed.Add(16*time.Minute)), "exhausted", "level_entered[team-alpha]:a2")
	})

	t.Run("negative remainder becomes zero and fires on the next tick", func(t *testing.T) {
		m := NewMachine(program(path(level("l1", "10m", user("u1")), level("l2", "10m", user("u2"))), nil))
		expectTrace(t, m.Start(monday), "level_entered:l1")
		m.Pause(monday.Add(15 * time.Minute))
		resumed := monday.Add(20 * time.Minute)
		expectTrace(t, m.Resume(resumed))
		expectWake(t, m, resumed)
		expectTrace(t, m.Tick(resumed), "level_entered:l2")
	})

	t.Run("repeat timer freezes", func(t *testing.T) {
		document := path(level("l1", "10m", user("u1")))
		document.Repeat = &routing.Repeat{Times: 1, After: "10m"}
		m := NewMachine(program(document, nil))
		expectTrace(t, m.Start(monday), "level_entered:l1")
		expectTrace(t, m.Tick(monday.Add(10*time.Minute)), "repeat:1")
		m.Pause(monday.Add(13 * time.Minute))
		expectNoWake(t, m)
		if phase := m.Progress(1, monday).Organization.Phase; phase != application.EscalationPhasePaused {
			t.Fatalf("phase while repeat frozen: %s", phase)
		}
		resumed := monday.Add(30 * time.Minute)
		m.Resume(resumed)
		expectWake(t, m, resumed.Add(7*time.Minute))
		expectTrace(t, m.Tick(resumed.Add(7*time.Minute)), "level_entered:l1")
	})

	t.Run("defer timer keeps running", func(t *testing.T) {
		m := NewMachine(program(path(deferNode("d", "office", false), level("l1", "10m", user("u1"))), nil))
		expectTrace(t, m.Start(fridayEvening), "deferred:d")
		m.Pause(fridayEvening.Add(time.Hour))
		// The wall-clock window is still armed and still reports deferred.
		expectWake(t, m, mondayOpen)
		if phase := m.Progress(1, fridayEvening).Organization.Phase; phase != application.EscalationPhaseDeferred {
			t.Fatalf("phase for a defer during a pause: %s", phase)
		}
		// The window opening fires even while paused; the level it reaches
		// arms a timer that is frozen at once because the claim still holds.
		expectTrace(t, m.Tick(mondayOpen), "level_entered:l1")
		expectNoWake(t, m)
		if phase := m.Progress(1, mondayOpen).Organization.Phase; phase != application.EscalationPhasePaused {
			t.Fatalf("phase for a level entered during a pause: %s", phase)
		}
		resumed := mondayOpen.Add(time.Hour)
		m.Resume(resumed)
		expectWake(t, m, resumed.Add(10*time.Minute))
	})
}

func TestProgressPhases(t *testing.T) {
	teams := map[domain.ID]routing.TeamPolicyDocument{
		teamAlpha: policy(level("a1", "5m", user("a-user"))),
	}
	m := NewMachine(program(path(
		level("l1", "10m", user("u1"), team(teamAlpha)),
		level("l2", "10m", user("u2")),
	), teams))
	m.Start(monday)

	progress := m.Progress(3, monday)
	want := application.EscalationProgress{
		Sequence: 3,
		Urgency:  routing.UrgencyStandard,
		Organization: application.EscalationWalkerProgress{
			Phase: application.EscalationPhaseLevel, CurrentNodeID: "l1", LevelOrdinal: 1, LevelCount: 2, NextAt: monday.Add(10 * time.Minute),
		},
		Teams: []application.EscalationWalkerProgress{{
			TeamID: teamAlpha, Phase: application.EscalationPhaseLevel, CurrentNodeID: "a1", LevelOrdinal: 1, LevelCount: 1, NextAt: monday.Add(5 * time.Minute),
		}},
		UpdatedAt: monday,
	}
	if !reflect.DeepEqual(progress, want) {
		t.Fatalf("progress\n got %+v\nwant %+v", progress, want)
	}

	m.Tick(monday.Add(5 * time.Minute))
	progress = m.Progress(4, monday.Add(5*time.Minute))
	if progress.Teams[0].Phase != application.EscalationPhaseExhausted || !progress.Teams[0].NextAt.IsZero() {
		t.Fatalf("exhausted team progress: %+v", progress.Teams[0])
	}
	m.Tick(monday.Add(10 * time.Minute))
	m.Tick(monday.Add(20 * time.Minute))
	progress = m.Progress(5, monday.Add(20*time.Minute))
	if progress.Organization.Phase != application.EscalationPhaseExhausted || progress.Organization.CurrentNodeID != "l2" || progress.Organization.LevelOrdinal != 2 {
		t.Fatalf("exhausted org progress: %+v", progress.Organization)
	}
}

func TestReplayRebuildsTheSameMachine(t *testing.T) {
	teams := map[domain.ID]routing.TeamPolicyDocument{
		teamAlpha: policy(level("a1", "5m", user("a-user")), level("a2", "5m", broadcast())),
	}
	document := path(
		level("l1", "10m", team(teamAlpha)),
		deferNode("d", "office", false),
		raisingLevel("l2", "10m", user("u2")),
	)
	document.Repeat = &routing.Repeat{Times: 1, After: "2m"}
	build := func() (*Machine, [][]string) {
		m := NewMachine(program(document, teams))
		var traces [][]string
		traces = append(traces, trace(m.Start(monday)))
		traces = append(traces, trace(m.Tick(monday.Add(5*time.Minute))))
		m.Pause(monday.Add(7 * time.Minute))
		m.Resume(monday.Add(9 * time.Minute))
		traces = append(traces, trace(m.Tick(monday.Add(12*time.Minute))))
		traces = append(traces, trace(m.Tick(monday.Add(22*time.Minute))))
		traces = append(traces, trace(m.Tick(monday.Add(24*time.Minute))))
		return m, traces
	}
	first, firstTraces := build()
	second, secondTraces := build()
	if !reflect.DeepEqual(firstTraces, secondTraces) {
		t.Fatalf("replay diverged:\n%v\n%v", firstTraces, secondTraces)
	}
	if !reflect.DeepEqual(first.Progress(1, monday), second.Progress(1, monday)) {
		t.Fatal("replayed progress differs")
	}
	firstWake, _ := first.NextWake()
	secondWake, _ := second.NextWake()
	if !firstWake.Equal(secondWake) {
		t.Fatalf("replayed wake differs: %s vs %s", firstWake, secondWake)
	}
}

func TestRequestConditionsAndFactKeys(t *testing.T) {
	teams := map[domain.ID]routing.TeamPolicyDocument{
		teamBeta:  policy(ifElse("b-if", []routing.Condition{requestIs("b1")}, []routing.Node{level("b", "5m", user("x"))}, nil)),
		teamAlpha: policy(ifElse("a-if", []routing.Condition{requestIs("a1")}, []routing.Node{level("a", "5m", user("x"))}, nil)),
	}
	document := path(
		ifElse("if", []routing.Condition{requestIs("p1"), workingHours("office", true)},
			[]routing.Node{ifElse("inner", []routing.Condition{requestIs("p2")}, nil, nil)},
			[]routing.Node{ifElse("else", []routing.Condition{requestIs("p3")}, nil, nil)}),
	)
	program := ProgramFromDocuments(document, teams, routing.UrgencyStandard)
	if program.Facts == nil {
		t.Fatal("ProgramFromDocuments left Facts nil")
	}

	var got []string
	for _, scoped := range program.RequestConditions() {
		got = append(got, FactKey(scoped.Scope, scoped.Condition.ID))
	}
	want := []string{"/p1", "/p2", "/p3", "team-alpha/a1", "team-beta/b1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request condition keys = %v, want %v", got, want)
	}

	// A team walker reads its own scope's facts, not the path's.
	program.Facts[FactKey(teamAlpha, "a1")] = true
	m := NewMachine(Program{
		Path:    path(level("l1", "30m", team(teamAlpha), team(teamBeta))),
		Teams:   teams,
		Urgency: routing.UrgencyStandard,
		Facts:   program.Facts,
	})
	expectTrace(t, m.Start(monday), "level_entered:l1", "level_entered[team-alpha]:a", "exhausted[team-beta]")
}

func TestProgramReachableTargetsReadsPathScopeFactsOnly(t *testing.T) {
	program := ProgramFromDocuments(path(
		ifElse("if", []routing.Condition{requestIs("p1")},
			[]routing.Node{level("then", "5m", user("u1"))},
			[]routing.Node{level("else", "5m", user("u2"))}),
	), nil, routing.UrgencyStandard)

	// A team policy's fact under the same id never opens a path branch.
	program.Facts[FactKey(teamAlpha, "p1")] = true
	if got := program.ReachableTargets(); len(got) != 1 || got[0].ID != "u2" {
		t.Fatalf("ReachableTargets() with only team-scope facts = %v, want u2", got)
	}
	program.Facts[FactKey("", "p1")] = true
	if got := program.ReachableTargets(); len(got) != 1 || got[0].ID != "u1" {
		t.Fatalf("ReachableTargets() with the path fact = %v, want u1", got)
	}
}
