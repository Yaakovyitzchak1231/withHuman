package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/escalation"
	"github.com/withHumanAI/withHuman/routing"
	"go.temporal.io/sdk/testsuite"
)

const (
	teamOncall domain.ID = "team-oncall"

	// Condition ids must be UUIDs; these two are fixed so fact keys are
	// stable across the tests.
	deployConditionID   = "3b6c6d5e-0d4c-4a3f-9d21-6f9b0a2c1e01"
	rollbackConditionID = "3b6c6d5e-0d4c-4a3f-9d21-6f9b0a2c1e02"
	brokenConditionID   = "3b6c6d5e-0d4c-4a3f-9d21-6f9b0a2c1e03"
)

// stubEscalationRepository serves one pinned program and records every
// progress write the executor makes, in order.
type stubEscalationRepository struct {
	mu       sync.Mutex
	record   application.EscalationProgramRecord
	stale    bool
	progress []application.RecordEscalationProgressInput
}

func (r *stubEscalationRepository) LoadEscalationProgram(context.Context, application.LoadEscalationProgramInput) (application.EscalationProgramRecord, error) {
	return r.record, nil
}

func (r *stubEscalationRepository) RecordEscalationProgress(_ context.Context, input application.RecordEscalationProgressInput) (application.EscalationProgressResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, input)
	return application.EscalationProgressResult{Applied: !r.stale}, nil
}

func (r *stubEscalationRepository) GetActiveTeamEscalationPolicies(context.Context, application.GetActiveTeamEscalationPoliciesInput) ([]domain.TeamEscalationPolicyRevision, error) {
	return nil, nil
}

// ticks returns the recorded progress writes under the lock.
func (r *stubEscalationRepository) ticks() []application.RecordEscalationProgressInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]application.RecordEscalationProgressInput(nil), r.progress...)
}

// levelEnteredAt reports when the tick that entered the named level was
// recorded, as an offset from the request's creation.
func (r *stubEscalationRepository) levelEnteredAt(t *testing.T, request domain.ApprovalRequest, nodeID string) time.Duration {
	t.Helper()
	for _, tick := range r.ticks() {
		for _, event := range tick.Events {
			if event.EventType != EventLevelEntered {
				continue
			}
			var data struct {
				NodeID string `json:"node_id"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.NodeID == nodeID {
				return tick.Progress.UpdatedAt.Sub(request.CreatedAt)
			}
		}
	}
	t.Fatalf("level %s was never entered: %d ticks", nodeID, len(r.progress))
	return 0
}

// levelResolver answers each level's targets with one email-only
// recipient per target, named after the target, so a delivery's
// recipients say which level fired. Broadcasts resolve to the team.
type levelResolver struct {
	mu            sync.Mutex
	approvalCalls int
	inputs        []application.ResolveLevelRecipientsInput
}

func (r *levelResolver) ResolveApprovalRecipients(context.Context, application.ResolveApprovalRecipientsInput) (application.ApprovalRecipients, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approvalCalls++
	return emailResolver().recipients, nil
}

func (r *levelResolver) ResolveLevelRecipients(_ context.Context, input application.ResolveLevelRecipientsInput) (application.ApprovalRecipients, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, input)
	recipients := application.ApprovalRecipients{Outcome: domain.RoutingEscalated, Urgency: input.Urgency}
	for _, entry := range input.Entries {
		for _, target := range entry.Targets {
			id := target.ID
			if target.Type == routing.TargetBroadcast {
				id = "everyone-on-" + string(entry.TeamID)
			}
			recipients.Recipients = append(recipients.Recipients, application.NotificationRecipient{
				MembershipID: domain.ID("membership-" + id), UserID: domain.ID("user-" + id), DisplayName: id,
				Methods: []application.RecipientMethod{{Kind: domain.ContactEmail, Address: id + "@example.com"}},
			})
		}
	}
	return recipients, nil
}

func pinnedProgram(t *testing.T, path routing.PathDocument, teams map[domain.ID]routing.TeamPolicyDocument) application.EscalationProgramRecord {
	t.Helper()
	encodedPath, err := json.Marshal(path)
	if err != nil {
		t.Fatal(err)
	}
	record := application.EscalationProgramRecord{
		Escalated: true, Urgency: string(routing.UrgencyStandard),
		PathDocument: encodedPath, PathDocumentVersion: routing.DocumentVersion,
	}
	for teamID, document := range teams {
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		record.TeamPolicies = append(record.TeamPolicies, application.PinnedTeamPolicyDocument{
			TeamID: teamID, Document: encoded, DocumentVersion: routing.DocumentVersion,
		})
	}
	return record
}

func userTarget(id string) routing.Target { return routing.Target{Type: routing.TargetUser, ID: id} }

func levelNode(id, after string, targets ...routing.Target) routing.Node {
	return routing.Node{ID: id, Type: routing.NodeLevel, Targets: targets, EscalateAfter: after}
}

// twoLevelPath pages u1 first and u2 three minutes later.
func twoLevelPath() routing.PathDocument {
	return routing.PathDocument{Nodes: []routing.Node{
		levelNode("level-1", "3m", userTarget("u1")),
		levelNode("level-2", "3m", userTarget("u2")),
	}}
}

type escalationRun struct {
	request    domain.ApprovalRequest
	repository *workflowRepository
	programs   *stubEscalationRepository
	resolver   *levelResolver
	notifier   *recordingNotifier
	env        *testsuite.TestWorkflowEnvironment
}

// newEscalationRun wires a workflow test around one pinned program. The
// environment's clock starts at the request's creation so recorded
// instants are exact offsets from it.
func newEscalationRun(t *testing.T, timeout time.Duration, record application.EscalationProgramRecord) *escalationRun {
	t.Helper()
	request := workflowRequest(timeout)
	run := &escalationRun{
		request:    request,
		repository: &workflowRepository{request: request},
		programs:   &stubEscalationRepository{record: record},
		resolver:   &levelResolver{},
		notifier:   &recordingNotifier{},
	}
	activities := NewActivities(run.repository, run.resolver, &stubRuleRepository{}, run.notifier, workflowClock{now: request.CreatedAt}, WithEscalation(run.programs))
	var suite testsuite.WorkflowTestSuite
	run.env = suite.NewTestWorkflowEnvironment()
	run.env.SetStartTime(request.CreatedAt)
	run.env.RegisterActivity(activities)
	return run
}

func (run *escalationRun) execute(t *testing.T) {
	t.Helper()
	run.env.ExecuteWorkflow(ApprovalWorkflow, ApprovalWorkflowInput{OrganizationID: run.request.OrganizationID, RequestID: run.request.ID})
	if err := run.env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}

// deliveries lists each delivery's recipients by email, in send order.
func (run *escalationRun) deliveries() []string {
	var sent []string
	for _, input := range run.notifier.allInputs() {
		emails := make([]string, 0, len(input.Recipients))
		for _, recipient := range input.Recipients {
			emails = append(emails, strings.Join(recipient.Emails, ","))
		}
		sent = append(sent, strings.Join(emails, "+"))
	}
	return sent
}

func assertDeliveries(t *testing.T, run *escalationRun, want ...string) {
	t.Helper()
	got := run.deliveries()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("deliveries = %v, want %v", got, want)
	}
}

func TestEscalationPagesTheNextLevelWhenItsWaitRunsOut(t *testing.T) {
	run := newEscalationRun(t, 10*time.Minute, pinnedProgram(t, twoLevelPath(), nil))
	run.execute(t)

	assertDeliveries(t, run, "u1@example.com", "u2@example.com")
	if got := run.programs.levelEnteredAt(t, run.request, "level-2"); got != 3*time.Minute {
		t.Fatalf("level 2 entered at %s, want 3m", got)
	}
	ticks := run.programs.ticks()
	// Start, level 2 at 3m, exhaustion at 6m: one write per tick, numbered.
	if len(ticks) != 3 {
		t.Fatalf("got %d progress writes, want 3: %#v", len(ticks), ticks)
	}
	for index, tick := range ticks {
		if tick.Progress.Sequence != int64(index+1) {
			t.Fatalf("tick %d carries sequence %d", index, tick.Progress.Sequence)
		}
	}
	last := ticks[2]
	if last.Progress.Organization.Phase != application.EscalationPhaseExhausted || len(last.Events) != 1 || last.Events[0].EventType != EventEscalationExhausted {
		t.Fatalf("final tick did not record exhaustion: %#v", last)
	}
	if run.resolver.approvalCalls != 0 {
		t.Fatal("an escalated request must not also run the default queue plan")
	}
	if run.repository.expired != 1 {
		t.Fatal("request did not expire at its deadline")
	}
}

func TestEscalationDecisionLeavesLaterLevelsUnpaged(t *testing.T) {
	run := newEscalationRun(t, 10*time.Minute, pinnedProgram(t, twoLevelPath(), nil))
	run.env.RegisterDelayedCallback(func() {
		run.env.SignalWorkflow(DecisionSignal, decisionSignal{Command: domain.DecisionCommand{
			OperationID: "operation-1", RequestID: run.request.ID, OrganizationID: run.request.OrganizationID, Status: domain.ApprovalApproved,
		}})
	}, 2*time.Minute)
	run.execute(t)

	assertDeliveries(t, run, "u1@example.com")
	if run.repository.request.Status != domain.ApprovalApproved || run.repository.expired != 0 {
		t.Fatalf("decision did not end the request: %s expired=%d", run.repository.request.Status, run.repository.expired)
	}
}

func TestEscalationClaimHoldsTheClockUntilItLapses(t *testing.T) {
	run := newEscalationRun(t, 10*time.Minute, pinnedProgram(t, twoLevelPath(), nil))
	run.env.RegisterDelayedCallback(func() {
		run.env.SignalWorkflow(ClaimSignal, claimSignal{ClaimID: "claim-1", ExpiresAt: run.request.CreatedAt.Add(4 * time.Minute)})
	}, time.Minute)
	run.execute(t)

	// Level 2 had two minutes left when the claim arrived at 1m; the claim
	// held for three, so it pages at 6m instead of 3m.
	assertDeliveries(t, run, "u1@example.com", "u2@example.com")
	if got := run.programs.levelEnteredAt(t, run.request, "level-2"); got != 6*time.Minute {
		t.Fatalf("level 2 entered at %s, want 6m", got)
	}
	if len(run.repository.lapsed) != 1 || run.repository.lapsed[0] != "claim-1" {
		t.Fatalf("claim did not lapse: %v", run.repository.lapsed)
	}
	var paused bool
	for _, tick := range run.programs.ticks() {
		if tick.Progress.Organization.Phase == application.EscalationPhasePaused {
			paused = true
		}
	}
	if !paused {
		t.Fatal("the claim did not record a paused position")
	}
}

func TestEscalationEarlyReleaseResumesTheClock(t *testing.T) {
	run := newEscalationRun(t, 10*time.Minute, pinnedProgram(t, twoLevelPath(), nil))
	run.env.RegisterDelayedCallback(func() {
		run.env.SignalWorkflow(ClaimSignal, claimSignal{ClaimID: "claim-1", ExpiresAt: run.request.CreatedAt.Add(4 * time.Minute)})
	}, time.Minute)
	run.env.RegisterDelayedCallback(func() {
		// A release for some other claim changes nothing.
		run.env.SignalWorkflow(ClaimReleasedSignal, claimReleasedSignal{ClaimID: "claim-0"})
	}, 90*time.Second)
	run.env.RegisterDelayedCallback(func() {
		run.env.SignalWorkflow(ClaimReleasedSignal, claimReleasedSignal{ClaimID: "claim-1"})
	}, 2*time.Minute)
	run.execute(t)

	// Held from 1m to 2m: level 2 shifts by one minute, to 4m.
	assertDeliveries(t, run, "u1@example.com", "u2@example.com")
	if got := run.programs.levelEnteredAt(t, run.request, "level-2"); got != 4*time.Minute {
		t.Fatalf("level 2 entered at %s, want 4m", got)
	}
	if len(run.repository.lapsed) != 0 {
		t.Fatalf("a released claim must not lapse: %v", run.repository.lapsed)
	}
}

func TestEscalationDeadlineExpiresBeforeTheNextLevel(t *testing.T) {
	run := newEscalationRun(t, 2*time.Minute, pinnedProgram(t, twoLevelPath(), nil))
	run.execute(t)

	assertDeliveries(t, run, "u1@example.com")
	if run.repository.expired != 1 || run.repository.request.Status != domain.ApprovalExpired {
		t.Fatalf("request did not expire: %#v", run.repository.request)
	}
}

func TestEscalationRunsATeamPolicyInsideTheOrganizationPath(t *testing.T) {
	path := routing.PathDocument{Nodes: []routing.Node{
		levelNode("org-1", "5m", userTarget("u1"), routing.Target{Type: routing.TargetTeam, ID: string(teamOncall)}),
		levelNode("org-2", "3m", userTarget("u2")),
	}}
	policy := routing.TeamPolicyDocument{Nodes: []routing.Node{
		levelNode("team-1", "2m", userTarget("t1")),
		levelNode("team-2", "2m", routing.Target{Type: routing.TargetBroadcast}),
	}}
	run := newEscalationRun(t, 10*time.Minute, pinnedProgram(t, path, map[domain.ID]routing.TeamPolicyDocument{teamOncall: policy}))
	run.execute(t)

	// The organization level and the team's first level fire together at
	// 0; the team's second level follows its own two minute wait; the
	// organization's second level waits its full five.
	assertDeliveries(t, run, "u1@example.com+t1@example.com", "everyone-on-team-oncall@example.com", "u2@example.com")
	if got := run.programs.levelEnteredAt(t, run.request, "team-2"); got != 2*time.Minute {
		t.Fatalf("team level 2 entered at %s, want 2m", got)
	}
	if got := run.programs.levelEnteredAt(t, run.request, "org-2"); got != 5*time.Minute {
		t.Fatalf("organization level 2 entered at %s, want 5m", got)
	}
	first := run.resolver.inputs[0]
	if len(first.Entries) != 2 || first.Entries[0].TeamID != "" || first.Entries[1].TeamID != teamOncall {
		t.Fatalf("first tick did not resolve the organization level and the team level: %#v", first.Entries)
	}
	if len(first.Entries[0].Targets) != 1 || first.Entries[0].Targets[0].ID != "u1" {
		t.Fatalf("the policy team must be handed to its walker, not resolved as a target: %#v", first.Entries[0].Targets)
	}
}

func TestEscalationStaleTickSendsNothing(t *testing.T) {
	run := newEscalationRun(t, 10*time.Minute, pinnedProgram(t, twoLevelPath(), nil))
	run.programs.stale = true
	run.execute(t)

	if run.notifier.callCount() != 0 {
		t.Fatalf("a stale tick paged someone: %v", run.deliveries())
	}
	if len(run.resolver.inputs) != 0 {
		t.Fatal("a stale tick resolved recipients")
	}
	if run.repository.expired != 1 {
		t.Fatal("request did not expire")
	}
}

func TestEscalationDefaultQueueStillPlansOnce(t *testing.T) {
	run := newEscalationRun(t, 10*time.Minute, application.EscalationProgramRecord{Escalated: false})
	run.execute(t)

	if run.resolver.approvalCalls != 1 || len(run.resolver.inputs) != 0 {
		t.Fatalf("default queue must plan once through the routing resolver: approval=%d level=%d", run.resolver.approvalCalls, len(run.resolver.inputs))
	}
	if run.notifier.callCount() != 1 || len(run.programs.ticks()) != 0 {
		t.Fatalf("default queue must deliver once and record no escalation progress: deliveries=%d ticks=%d", run.notifier.callCount(), len(run.programs.ticks()))
	}
}

func TestLoadEscalationProgramReusesPinnedFacts(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	deploy := approvalblock.Field("/request/tool").Equals("deploy")
	deploy.ID = deployConditionID
	rollback := approvalblock.Field("/request/tool").Equals("rollback")
	rollback.ID = rollbackConditionID
	path := routing.PathDocument{Nodes: []routing.Node{
		{ID: "branch", Type: routing.NodeIfElse,
			Conditions: []routing.Condition{{Type: routing.ConditionRequest, When: &deploy}},
			Then:       []routing.Node{levelNode("deploys", "3m", userTarget("u1"))},
			Else:       []routing.Node{levelNode("others", "3m", userTarget("u2"))},
		},
	}}
	policy := routing.TeamPolicyDocument{Nodes: []routing.Node{
		{ID: "team-branch", Type: routing.NodeIfElse,
			Conditions: []routing.Condition{{Type: routing.ConditionRequest, When: &rollback}},
			Then:       []routing.Node{levelNode("team-rollbacks", "2m", userTarget("t1"))},
		},
	}}
	// The router pinned these when the request was routed; the executor
	// never re-evaluates the request, it reads what was pinned.
	record := pinnedProgram(t, path, map[domain.ID]routing.TeamPolicyDocument{teamOncall: policy})
	record.Facts = map[string]bool{
		escalation.FactKey("", deployConditionID):           true,
		escalation.FactKey(teamOncall, rollbackConditionID): false,
	}
	programs := &stubEscalationRepository{record: record}
	activities := NewActivities(&workflowRepository{request: request}, &levelResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt}, WithEscalation(programs))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestActivityEnvironment()
	environment.RegisterActivity(activities.LoadEscalationProgram)

	result, err := environment.ExecuteActivity(activities.LoadEscalationProgram, request)
	if err != nil {
		t.Fatal(err)
	}
	var output EscalationProgramOutput
	if err := result.Get(&output); err != nil {
		t.Fatal(err)
	}
	if !output.Escalated || output.Program.Urgency != routing.UrgencyStandard {
		t.Fatalf("program not loaded: %#v", output)
	}
	program := output.Program
	if len(program.Facts) != len(record.Facts) {
		t.Fatalf("facts = %#v, want %#v", program.Facts, record.Facts)
	}
	for key, value := range record.Facts {
		if got, present := program.Facts[key]; !present || got != value {
			t.Fatalf("fact %s = %v (present %v), want %v", key, got, present, value)
		}
	}
	// The pinned facts steer the machine: the request is a deploy, so the
	// then branch pages u1.
	effects := escalation.NewMachine(program).Start(request.CreatedAt)
	if len(effects) != 1 || effects[0].Kind != escalation.EffectLevelEntered || effects[0].NodeID != "deploys" {
		t.Fatalf("machine did not follow the facts: %#v", effects)
	}
}

func TestLoadEscalationProgramRefusesAnUnknownDocumentVersion(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	record := pinnedProgram(t, twoLevelPath(), nil)
	record.PathDocumentVersion = routing.DocumentVersion + 1
	activities := NewActivities(&workflowRepository{request: request}, &levelResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt}, WithEscalation(&stubEscalationRepository{record: record}))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestActivityEnvironment()
	environment.RegisterActivity(activities.LoadEscalationProgram)

	if _, err := environment.ExecuteActivity(activities.LoadEscalationProgram, request); err == nil {
		t.Fatal("an unknown document version was accepted")
	}
}

func TestApplyEscalationEffectsRecordsOneAuditEventPerEffect(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	programs := &stubEscalationRepository{record: pinnedProgram(t, twoLevelPath(), nil)}
	resolver := &levelResolver{}
	activities := NewActivities(&workflowRepository{request: request}, resolver, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt}, WithEscalation(programs))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestActivityEnvironment()
	environment.RegisterActivity(activities.ApplyEscalationEffects)
	until := request.CreatedAt.Add(time.Hour)
	input := ApplyEscalationEffectsInput{
		Request: request, Sequence: 4,
		Effects: []escalation.Effect{
			{Kind: escalation.EffectUrgencyRaised, NodeID: "org-2", Urgency: routing.UrgencyInterrupt},
			{Kind: escalation.EffectLevelEntered, NodeID: "org-2", Targets: []routing.Target{userTarget("u2"), {Type: routing.TargetTeam, ID: "team-plain"}}, Urgency: routing.UrgencyInterrupt, LevelOrdinal: 2, LevelCount: 2, Repeat: 1},
			{Kind: escalation.EffectDeferred, Scope: teamOncall, NodeID: "team-wait", Until: until},
			{Kind: escalation.EffectRepeat, Scope: teamOncall, Iteration: 1, After: 10 * time.Minute},
			{Kind: escalation.EffectExhausted, Scope: teamOncall},
		},
		Progress: application.EscalationProgress{
			Sequence: 4, Urgency: routing.UrgencyInterrupt,
			Organization: application.EscalationWalkerProgress{Phase: application.EscalationPhaseLevel},
			UpdatedAt:    request.CreatedAt,
		},
	}

	result, err := environment.ExecuteActivity(activities.ApplyEscalationEffects, input)
	if err != nil {
		t.Fatal(err)
	}
	var output ApplyEscalationEffectsOutput
	if err := result.Get(&output); err != nil {
		t.Fatal(err)
	}
	if output.Stale || len(output.Steps) != 1 || len(output.Steps[0].Recipients) != 2 {
		t.Fatalf("unexpected plan: %#v", output)
	}
	if len(resolver.inputs) != 1 || resolver.inputs[0].Urgency != domain.NotificationInterrupt || len(resolver.inputs[0].Entries) != 1 || len(resolver.inputs[0].Entries[0].Targets) != 2 {
		t.Fatalf("level targets were not resolved at the tick's urgency: %#v", resolver.inputs)
	}
	ticks := programs.ticks()
	if len(ticks) != 1 || ticks[0].Progress.Sequence != 4 {
		t.Fatalf("progress not recorded once with the tick's sequence: %#v", ticks)
	}
	events := ticks[0].Events
	wantTypes := []string{EventUrgencyRaised, EventLevelEntered, EventDeferred, EventRepeated, EventEscalationExhausted}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events, want %d: %#v", len(events), len(wantTypes), events)
	}
	wantKeys := [][]string{
		{"node_id", "from", "to"},
		{"node_id", "level_ordinal", "level_count", "urgency", "repeat", "targets"},
		{"node_id", "team_id", "until"},
		{"team_id", "iteration", "after"},
		{"team_id"},
	}
	for index, event := range events {
		if event.EventType != wantTypes[index] || event.ActorType != domain.ActorSystem || event.SubjectType != "approval_request" || event.SubjectID != request.ID {
			t.Fatalf("event %d = %#v, want %s by the system on the request", index, event, wantTypes[index])
		}
		var data map[string]any
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		if len(data) != len(wantKeys[index]) {
			t.Fatalf("event %s data = %v, want keys %v", event.EventType, data, wantKeys[index])
		}
		for _, key := range wantKeys[index] {
			if _, present := data[key]; !present {
				t.Fatalf("event %s is missing %s: %v", event.EventType, key, data)
			}
		}
	}
	var raised struct{ From, To string }
	if err := json.Unmarshal(events[0].Data, &raised); err != nil || raised.From != "standard" || raised.To != "interrupt" {
		t.Fatalf("urgency_raised = %s", events[0].Data)
	}
	var entered struct {
		Targets []map[string]string `json:"targets"`
		TeamID  string              `json:"team_id"`
	}
	if err := json.Unmarshal(events[1].Data, &entered); err != nil || len(entered.Targets) != 2 || entered.Targets[1]["type"] != "team" || entered.Targets[1]["id"] != "team-plain" {
		t.Fatalf("level_entered = %s", events[1].Data)
	}
}

func TestApplyEscalationEffectsReportsAStaleTick(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	programs := &stubEscalationRepository{record: pinnedProgram(t, twoLevelPath(), nil), stale: true}
	resolver := &levelResolver{}
	activities := NewActivities(&workflowRepository{request: request}, resolver, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt}, WithEscalation(programs))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestActivityEnvironment()
	environment.RegisterActivity(activities.ApplyEscalationEffects)

	result, err := environment.ExecuteActivity(activities.ApplyEscalationEffects, ApplyEscalationEffectsInput{
		Request: request, Sequence: 1,
		Effects:  []escalation.Effect{{Kind: escalation.EffectLevelEntered, NodeID: "level-1", Targets: []routing.Target{userTarget("u1")}, Urgency: routing.UrgencyStandard, LevelOrdinal: 1, LevelCount: 2}},
		Progress: application.EscalationProgress{Sequence: 1, Urgency: routing.UrgencyStandard, Organization: application.EscalationWalkerProgress{Phase: application.EscalationPhaseLevel}, UpdatedAt: request.CreatedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output ApplyEscalationEffectsOutput
	if err := result.Get(&output); err != nil {
		t.Fatal(err)
	}
	if !output.Stale || len(output.Steps) != 0 || len(resolver.inputs) != 0 {
		t.Fatalf("a refused sequence must resolve nobody: %#v resolved=%d", output, len(resolver.inputs))
	}
}
