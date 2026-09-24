package orchestration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/escalation"
	"github.com/withHumanAI/withHuman/routing"
	"go.temporal.io/sdk/testsuite"
)

type notificationLedger struct {
	mu     sync.Mutex
	inputs []application.RecordApprovalNotificationsInput
}

func (l *notificationLedger) RecordApprovalNotifications(_ context.Context, input application.RecordApprovalNotificationsInput) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inputs = append(l.inputs, input)
	return nil
}

func membershipIDs(inputs []application.RecordApprovalNotificationsInput) []domain.ID {
	ids := make([]domain.ID, 0)
	for _, input := range inputs {
		ids = append(ids, input.MembershipIDs...)
	}
	return ids
}

// The default queue plan records everyone it addressed, a person with no
// channel included: the queue is where they find the request.
func TestPlanNotificationsRecordsWhoWasAddressed(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	resolver := &stubRecipientResolver{recipients: application.ApprovalRecipients{
		Outcome: domain.RoutingDefaultQueue, Urgency: domain.NotificationStandard,
		Recipients: []application.NotificationRecipient{
			{MembershipID: "membership-1", UserID: "user-1", DisplayName: "Ana", Methods: []application.RecipientMethod{
				{Kind: domain.ContactEmail, Address: "ana@example.com"},
			}},
			{MembershipID: "membership-2", UserID: "user-2", DisplayName: "Ben"},
		},
	}}
	ledger := &notificationLedger{}
	activities := NewActivities(&workflowRepository{request: request}, resolver, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt}, WithNotificationRecords(ledger))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestActivityEnvironment()
	environment.RegisterActivity(activities.PlanNotifications)

	if _, err := environment.ExecuteActivity(activities.PlanNotifications, request); err != nil {
		t.Fatal(err)
	}
	if len(ledger.inputs) != 1 || ledger.inputs[0].RequestID != request.ID || ledger.inputs[0].OrganizationID != request.OrganizationID {
		t.Fatalf("records = %#v", ledger.inputs)
	}
	if ids := membershipIDs(ledger.inputs); len(ids) != 2 || ids[0] != "membership-1" || ids[1] != "membership-2" {
		t.Fatalf("recorded memberships = %v", ids)
	}
	if !ledger.inputs[0].Now.Equal(request.CreatedAt) {
		t.Fatalf("recorded at %s, want the clock's %s", ledger.inputs[0].Now, request.CreatedAt)
	}
}

// An executor tick records the people the levels it entered resolve to,
// and nothing when the tick was stale.
func TestApplyEscalationEffectsRecordsTheLevelsPeople(t *testing.T) {
	request := workflowRequest(10 * time.Minute)
	programs := &stubEscalationRepository{record: pinnedProgram(t, twoLevelPath(), nil)}
	ledger := &notificationLedger{}
	activities := NewActivities(&workflowRepository{request: request}, &levelResolver{}, &stubRuleRepository{}, &recordingNotifier{}, workflowClock{now: request.CreatedAt}, WithEscalation(programs), WithNotificationRecords(ledger))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestActivityEnvironment()
	environment.RegisterActivity(activities.ApplyEscalationEffects)
	input := ApplyEscalationEffectsInput{
		Request: request, Sequence: 1,
		Effects: []escalation.Effect{
			{Kind: escalation.EffectLevelEntered, NodeID: "org-1", Targets: []routing.Target{userTarget("u1"), {Type: routing.TargetTeam, ID: "team-plain"}}, Urgency: routing.UrgencyStandard, LevelOrdinal: 1, LevelCount: 2},
		},
		Progress: application.EscalationProgress{Sequence: 1, Urgency: routing.UrgencyStandard, Organization: application.EscalationWalkerProgress{Phase: application.EscalationPhaseLevel, CurrentNodeID: "org-1", LevelOrdinal: 1, LevelCount: 2}, UpdatedAt: request.CreatedAt},
	}
	if _, err := environment.ExecuteActivity(activities.ApplyEscalationEffects, input); err != nil {
		t.Fatal(err)
	}
	if ids := membershipIDs(ledger.inputs); len(ids) != 2 || ids[0] != "membership-u1" || ids[1] != "membership-team-plain" {
		t.Fatalf("recorded memberships = %v", ids)
	}

	programs.stale = true
	input.Sequence = 2
	if _, err := environment.ExecuteActivity(activities.ApplyEscalationEffects, input); err != nil {
		t.Fatal(err)
	}
	if len(ledger.inputs) != 1 {
		t.Fatalf("a stale tick recorded notifications: %#v", ledger.inputs)
	}
}
