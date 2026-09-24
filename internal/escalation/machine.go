// Package escalation is the pure executor for escalation paths and team
// policies. It walks a pinned program node by node, arms timers, and
// reports what the runtime must do as effects. It never reads a clock,
// touches storage, or resolves people: every call takes the caller's now,
// and replaying the same calls with the same instants rebuilds the same
// machine. That determinism is what lets a durable workflow replay it.
package escalation

import (
	"sort"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/routing"
)

// Scope names the document a walker runs: empty for the organization
// path, else the id of the team whose policy it is.
type Scope = domain.ID

// Program is everything the machine needs, resolved before it starts: the
// pinned path, the pinned policy of every team that has one, the urgency
// the request arrived with, and the request-condition results. Request
// conditions are evaluated once, when the request is routed, because the
// request never changes while it is pending; the facts are pinned with
// the routing snapshot and the machine only reads them.
type Program struct {
	Path routing.PathDocument
	// Teams is keyed by team id. A team absent here has no policy, so a
	// level targeting it pages every member at once.
	Teams   map[domain.ID]routing.TeamPolicyDocument
	Urgency routing.Urgency
	// Facts holds one entry per request condition, keyed by FactKey. A
	// missing entry reads as false so an unevaluated branch never fires.
	Facts map[string]bool
}

// ProgramFromDocuments assembles a program with no request facts; the
// loader fills Facts from the routing snapshot.
func ProgramFromDocuments(path routing.PathDocument, teams map[domain.ID]routing.TeamPolicyDocument, urgency routing.Urgency) Program {
	return Program{Path: path, Teams: teams, Urgency: urgency, Facts: make(map[string]bool)}
}

// FactKey is where a request condition's result lives in Facts. Condition
// ids are unique within one document but not across documents, so the
// scope disambiguates them.
func FactKey(scope Scope, conditionID string) string {
	return string(scope) + "/" + conditionID
}

// ReachableTargets is the path's reachable target set under the pinned
// facts: the same set decide authorization admits.
func (program Program) ReachableTargets() []routing.Target {
	return program.Path.ReachableTargets(func(conditionID string) bool {
		return program.Facts[FactKey("", conditionID)]
	})
}

// ScopedCondition is one request condition and the document it sits in.
type ScopedCondition struct {
	Scope     Scope
	Condition approvalblock.Condition
}

// RequestConditions lists every request condition in the program, path
// first and then teams in id order.
func (program Program) RequestConditions() []ScopedCondition {
	conditions := CollectRequestConditions("", program.Path.Nodes)
	teamIDs := make([]string, 0, len(program.Teams))
	for teamID := range program.Teams {
		teamIDs = append(teamIDs, string(teamID))
	}
	sort.Strings(teamIDs)
	for _, teamID := range teamIDs {
		conditions = append(conditions, CollectRequestConditions(Scope(teamID), program.Teams[domain.ID(teamID)].Nodes)...)
	}
	return conditions
}

// CollectRequestConditions lists every request condition in one
// document's node tree, in walk order, under the given scope. The router
// evaluates these when it pins a request.
func CollectRequestConditions(scope Scope, nodes []routing.Node) []ScopedCondition {
	return collectRequestConditions(scope, nodes, make([]ScopedCondition, 0))
}

func collectRequestConditions(scope Scope, nodes []routing.Node, into []ScopedCondition) []ScopedCondition {
	for _, node := range nodes {
		for _, condition := range node.Conditions {
			if condition.Type == routing.ConditionRequest && condition.When != nil {
				into = append(into, ScopedCondition{Scope: scope, Condition: *condition.When})
			}
		}
		into = collectRequestConditions(scope, node.Then, into)
		into = collectRequestConditions(scope, node.Else, into)
	}
	return into
}

// EffectKind is what the runtime must do in response to a step.
type EffectKind string

const (
	// EffectLevelEntered asks the runtime to page the level's targets.
	EffectLevelEntered EffectKind = "level_entered"
	// EffectUrgencyRaised reports the request moved to interrupt urgency.
	EffectUrgencyRaised EffectKind = "urgency_raised"
	// EffectDeferred reports a walker parked until a window opens.
	EffectDeferred EffectKind = "deferred"
	// EffectRepeat reports a walker will re-run its document after a wait.
	EffectRepeat EffectKind = "repeat"
	// EffectExhausted reports a walker ran out of nodes and repeats.
	EffectExhausted EffectKind = "exhausted"
)

// Effect is one thing the runtime must do. Only the fields for its Kind
// are set; Scope is always set so the runtime knows which document acted.
type Effect struct {
	Kind   EffectKind
	Scope  Scope
	NodeID string
	// Targets are the level's targets left for the runtime to resolve:
	// users, policy-less teams, and broadcasts. Teams with a policy are
	// removed because their walker pages them instead.
	Targets []routing.Target
	Urgency routing.Urgency
	// Until is when a deferred walker resumes.
	Until time.Time
	// Iteration counts repeats performed so far, After the wait before it.
	Iteration int
	After     time.Duration
	// LevelOrdinal and LevelCount say "level 2 of 3" within the walker's
	// own document; Repeat is the iteration the level was entered on.
	LevelOrdinal int
	LevelCount   int
	Repeat       int
}

type timerKind uint8

const (
	timerNone timerKind = iota
	timerLevel
	timerDefer
	timerRepeat
)

// frame is one list of nodes the walker is inside and how far along it
// is. Branch bodies push a frame; finishing one pops back to its parent,
// which is how an empty branch falls through to the node after the
// if_else.
type frame struct {
	nodes []routing.Node
	index int
}

// walker is the cursor over one document. The organization path and each
// started team policy have one; they share only the machine's urgency.
type walker struct {
	scope    Scope
	nodes    []routing.Node
	repeat   *routing.Repeat
	sets     map[string]routing.WorkingHoursSet
	ordinals routing.LevelOrdinals

	frames []frame
	timer  timerKind
	due    time.Time
	// frozen is set while a claim holds the walker's level or repeat timer;
	// remaining is the time it had left, re-armed on resume.
	frozen    bool
	remaining time.Duration
	repeats   int
	done      bool
	// currentNodeID is the node whose timer is armed or the last level
	// entered; lastLevelID keeps the ordinal stable through a defer.
	currentNodeID string
	lastLevelID   string
}

func newWalker(scope Scope, workingHours []routing.WorkingHoursSet, nodes []routing.Node, repeat *routing.Repeat) *walker {
	sets := make(map[string]routing.WorkingHoursSet, len(workingHours))
	for _, set := range workingHours {
		sets[set.ID] = set
	}
	return &walker{
		scope:    scope,
		nodes:    nodes,
		repeat:   repeat,
		sets:     sets,
		ordinals: levelOrdinalsOf(nodes),
		frames:   []frame{{nodes: nodes}},
	}
}

// levelOrdinalsOf numbers a node list the way the documents do, so a team
// walker's ordinals count only its own levels.
func levelOrdinalsOf(nodes []routing.Node) routing.LevelOrdinals {
	return routing.PathDocument{Nodes: nodes}.LevelOrdinals()
}

func (w *walker) armed() bool {
	return w.timer != timerNone && !w.frozen && !w.done
}

// freeze stops a level or repeat timer, remembering how long it had left.
// Defer timers stay armed: a working-hours window opens at the same
// wall-clock instant whether or not someone holds a claim.
func (w *walker) freeze(now time.Time) {
	if w.timer == timerNone || w.timer == timerDefer || w.frozen || w.done {
		return
	}
	w.remaining = w.due.Sub(now)
	if w.remaining < 0 {
		w.remaining = 0
	}
	w.due = time.Time{}
	w.frozen = true
}

func (w *walker) thaw(now time.Time) {
	if !w.frozen {
		return
	}
	w.due = now.Add(w.remaining)
	w.remaining = 0
	w.frozen = false
}

func (w *walker) progress() application.EscalationWalkerProgress {
	progress := application.EscalationWalkerProgress{
		TeamID:        w.scope,
		CurrentNodeID: w.currentNodeID,
		LevelOrdinal:  w.ordinals.Ordinal[w.lastLevelID],
		LevelCount:    w.ordinals.Count,
		Repeat:        w.repeats,
	}
	switch {
	case w.done:
		progress.Phase = application.EscalationPhaseExhausted
	case w.frozen:
		progress.Phase = application.EscalationPhasePaused
	case w.timer == timerDefer:
		progress.Phase = application.EscalationPhaseDeferred
		progress.NextAt = w.due
	case w.timer == timerRepeat:
		progress.Phase = application.EscalationPhaseRepeatWait
		progress.NextAt = w.due
	default:
		progress.Phase = application.EscalationPhaseLevel
		progress.NextAt = w.due
	}
	return progress
}

// Machine runs one request's escalation: the organization walker plus a
// walker per team policy the path has handed off to.
type Machine struct {
	program Program
	urgency routing.Urgency
	org     *walker
	// teams is in start order so ticks, progress, and effects come out in
	// the same order on every replay.
	teams  []*walker
	paused bool
}

func NewMachine(program Program) *Machine {
	if program.Facts == nil {
		program.Facts = make(map[string]bool)
	}
	return &Machine{
		program: program,
		urgency: program.Urgency,
		org:     newWalker("", program.Path.WorkingHours, program.Path.Nodes, program.Path.Repeat),
	}
}

// Start walks the organization path up to its first timer.
func (m *Machine) Start(now time.Time) []Effect {
	return m.run(m.org, now)
}

// Tick fires every armed timer due at or before now, the organization
// first and then teams in start order, and walks each fired walker to its
// next timer. A walker whose new timer is also already due fires again in
// the same tick so a zero-length wait cannot strand it.
func (m *Machine) Tick(now time.Time) []Effect {
	effects := m.fire(m.org, now)
	// Index-based because the organization walker may append a team walker
	// mid-loop; its timers land after now so it does not fire here.
	for index := 0; index < len(m.teams); index++ {
		effects = append(effects, m.fire(m.teams[index], now)...)
	}
	return effects
}

func (m *Machine) fire(w *walker, now time.Time) []Effect {
	var effects []Effect
	for w.armed() && !w.due.After(now) {
		w.timer = timerNone
		w.due = time.Time{}
		effects = append(effects, m.run(w, now)...)
	}
	return effects
}

// Pause freezes every level and repeat timer while a claim is held.
// Defer timers keep running because they track wall-clock windows.
func (m *Machine) Pause(now time.Time) {
	if m.paused {
		return
	}
	m.paused = true
	for _, w := range m.walkers() {
		w.freeze(now)
	}
}

// Resume re-arms every frozen timer with the time it had left. A timer
// whose remainder was zero is due immediately; the caller Ticks to fire
// it, so resuming never emits effects itself.
func (m *Machine) Resume(now time.Time) []Effect {
	if !m.paused {
		return nil
	}
	m.paused = false
	for _, w := range m.walkers() {
		w.thaw(now)
	}
	return nil
}

// NextWake is the earliest armed timer across all walkers.
func (m *Machine) NextWake() (time.Time, bool) {
	var earliest time.Time
	found := false
	for _, w := range m.walkers() {
		if !w.armed() {
			continue
		}
		if !found || w.due.Before(earliest) {
			earliest = w.due
			found = true
		}
	}
	return earliest, found
}

// Done reports that every walker has exhausted its document.
func (m *Machine) Done() bool {
	for _, w := range m.walkers() {
		if !w.done {
			return false
		}
	}
	return true
}

func (m *Machine) Paused() bool {
	return m.paused
}

func (m *Machine) Urgency() routing.Urgency {
	return m.urgency
}

// Progress snapshots every walker for persistence.
func (m *Machine) Progress(sequence int64, now time.Time) application.EscalationProgress {
	progress := application.EscalationProgress{
		Sequence:     sequence,
		Urgency:      m.urgency,
		Organization: m.org.progress(),
		UpdatedAt:    now,
	}
	for _, w := range m.teams {
		progress.Teams = append(progress.Teams, w.progress())
	}
	return progress
}

func (m *Machine) walkers() []*walker {
	walkers := make([]*walker, 0, 1+len(m.teams))
	walkers = append(walkers, m.org)
	return append(walkers, m.teams...)
}

// run advances a walker from its cursor until it arms a timer or ends.
func (m *Machine) run(w *walker, now time.Time) []Effect {
	var effects []Effect
	for {
		if len(w.frames) == 0 {
			return append(effects, m.finish(w, now)...)
		}
		top := &w.frames[len(w.frames)-1]
		if top.index >= len(top.nodes) {
			w.frames = w.frames[:len(w.frames)-1]
			continue
		}
		node := top.nodes[top.index]
		top.index++
		switch node.Type {
		case routing.NodeLevel:
			return append(effects, m.enterLevel(w, node, now)...)
		case routing.NodeIfElse:
			branch := node.Else
			if m.conditionsHold(w, node.Conditions, now) {
				branch = node.Then
			}
			if len(branch) > 0 {
				w.frames = append(w.frames, frame{nodes: branch})
			}
		case routing.NodeDefer:
			if effect, parked := m.deferNode(w, node, now); parked {
				return append(effects, effect)
			}
		}
	}
}

// finish handles the end of the node list: another repeat iteration when
// the document allows one, otherwise exhaustion.
func (m *Machine) finish(w *walker, now time.Time) []Effect {
	if w.repeat != nil && w.repeats < w.repeat.Times {
		w.repeats++
		after := parseDuration(w.repeat.After)
		// Reset the cursor now so the repeat timer firing simply continues
		// the walk from the top; urgency is machine-wide and stays raised.
		w.frames = []frame{{nodes: w.nodes}}
		m.arm(w, timerRepeat, now.Add(after), now)
		return []Effect{{Kind: EffectRepeat, Scope: w.scope, Iteration: w.repeats, After: after}}
	}
	w.done = true
	w.timer = timerNone
	w.due = time.Time{}
	w.frozen = false
	return []Effect{{Kind: EffectExhausted, Scope: w.scope}}
}

func (m *Machine) enterLevel(w *walker, node routing.Node, now time.Time) []Effect {
	var effects []Effect
	if node.RaiseUrgency && m.urgency == routing.UrgencyStandard {
		m.urgency = routing.UrgencyInterrupt
		effects = append(effects, Effect{Kind: EffectUrgencyRaised, Scope: w.scope, NodeID: node.ID, Urgency: m.urgency})
	}

	targets := make([]routing.Target, 0, len(node.Targets))
	var policyTeams []domain.ID
	for _, target := range node.Targets {
		if target.Type == routing.TargetTeam {
			// Team policies never target teams; one that slipped through
			// cannot be honored inside a team walker, so it is dropped.
			if w.scope != "" {
				continue
			}
			teamID := domain.ID(target.ID)
			if _, hasPolicy := m.program.Teams[teamID]; hasPolicy {
				policyTeams = append(policyTeams, teamID)
				continue
			}
		}
		targets = append(targets, target)
	}

	w.currentNodeID = node.ID
	w.lastLevelID = node.ID
	effects = append(effects, Effect{
		Kind:         EffectLevelEntered,
		Scope:        w.scope,
		NodeID:       node.ID,
		Targets:      targets,
		Urgency:      m.urgency,
		LevelOrdinal: w.ordinals.Ordinal[node.ID],
		LevelCount:   w.ordinals.Count,
		Repeat:       w.repeats,
	})
	m.arm(w, timerLevel, now.Add(parseDuration(node.EscalateAfter)), now)
	for _, teamID := range policyTeams {
		effects = append(effects, m.startTeam(teamID, now)...)
	}
	return effects
}

// startTeam hands a level to a team's policy. A walker still running for
// that team keeps going untouched: re-targeting a team mid-policy must not
// restart its ladder. A finished walker is replaced in place so start
// order, and with it effect order, stays stable.
func (m *Machine) startTeam(teamID domain.ID, now time.Time) []Effect {
	index := m.teamIndex(teamID)
	if index >= 0 && !m.teams[index].done {
		return nil
	}
	document := m.program.Teams[teamID]
	w := newWalker(Scope(teamID), document.WorkingHours, document.Nodes, document.Repeat)
	if index >= 0 {
		m.teams[index] = w
	} else {
		m.teams = append(m.teams, w)
	}
	return m.run(w, now)
}

func (m *Machine) teamIndex(teamID domain.ID) int {
	for index, w := range m.teams {
		if w.scope == teamID {
			return index
		}
	}
	return -1
}

// deferNode parks the walker until its window opens. It is a no-op when
// the window is already open or when the request is interrupt-urgent and
// the defer only applies to standard requests.
func (m *Machine) deferNode(w *walker, node routing.Node, now time.Time) (Effect, bool) {
	if node.StandardOnly && m.urgency == routing.UrgencyInterrupt {
		return Effect{}, false
	}
	set, known := w.sets[node.WorkingHoursID]
	if !known || windowOpen(set, now) {
		return Effect{}, false
	}
	until := nextOpen(set, now)
	w.currentNodeID = node.ID
	m.arm(w, timerDefer, until, now)
	return Effect{Kind: EffectDeferred, Scope: w.scope, NodeID: node.ID, Until: until}, true
}

func (m *Machine) conditionsHold(w *walker, conditions []routing.Condition, now time.Time) bool {
	for _, condition := range conditions {
		switch condition.Type {
		case routing.ConditionWorkingHours:
			set, known := w.sets[condition.WorkingHoursID]
			open := known && windowOpen(set, now)
			if open != condition.Active {
				return false
			}
		case routing.ConditionUrgency:
			if m.urgency != condition.Urgency {
				return false
			}
		case routing.ConditionRequest:
			if condition.When == nil || !m.program.Facts[FactKey(w.scope, condition.When.ID)] {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// arm sets a walker's timer. A timer armed while a claim is held is frozen
// at once so the claim covers it too; defer timers are exempt as always.
func (m *Machine) arm(w *walker, kind timerKind, due, now time.Time) {
	w.timer = kind
	w.due = due
	w.frozen = false
	w.remaining = 0
	if m.paused {
		w.freeze(now)
	}
}

// parseDuration reads a validated duration string. Documents are validated
// before pinning, so a parse failure means a corrupted revision; a zero
// wait makes the fault visible as an immediate escalation rather than a
// silent stall.
func parseDuration(raw string) time.Duration {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	return duration
}
