package reviewrouting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type pathReaderStub struct {
	revisions map[string]domain.EscalationPathRevision
	err       error
	// policies are the active team policy revisions keyed by team;
	// policyErr fails the lookup instead.
	policies  map[domain.ID]domain.TeamEscalationPolicyRevision
	policyErr error
	// askedTeams records which teams the service asked about.
	askedTeams *[]domain.ID
	// owners are the agent owners' standings keyed by membership;
	// ownerErr fails the lookup instead.
	owners   map[domain.ID]application.AgentOwnerStanding
	ownerErr error
}

func (stub pathReaderStub) GetAgentOwnerStanding(_ context.Context, input application.GetAgentOwnerStandingInput) (application.AgentOwnerStanding, error) {
	if stub.ownerErr != nil {
		return application.AgentOwnerStanding{}, stub.ownerErr
	}
	return stub.owners[input.MembershipID], nil
}

func (stub pathReaderStub) GetActiveTeamEscalationPolicies(_ context.Context, input application.GetActiveTeamEscalationPoliciesInput) ([]domain.TeamEscalationPolicyRevision, error) {
	if stub.askedTeams != nil {
		*stub.askedTeams = append(*stub.askedTeams, input.TeamIDs...)
	}
	if stub.policyErr != nil {
		return nil, stub.policyErr
	}
	revisions := make([]domain.TeamEscalationPolicyRevision, 0)
	for _, teamID := range input.TeamIDs {
		if revision, exists := stub.policies[teamID]; exists {
			revisions = append(revisions, revision)
		}
	}
	return revisions, nil
}

func (stub pathReaderStub) GetActiveEscalationPathRevision(_ context.Context, input application.GetActiveEscalationPathRevisionInput) (domain.EscalationPathRevision, error) {
	if stub.err != nil {
		return domain.EscalationPathRevision{}, stub.err
	}
	revision, exists := stub.revisions[input.PathKey]
	if !exists {
		return domain.EscalationPathRevision{}, domain.ErrNotFound
	}
	return revision, nil
}

type evaluationClock struct{}

func (evaluationClock) Now() time.Time { return time.Unix(90_000, 0).UTC() }

func pendingInitialization(route *application.HumanReviewRoute) application.InitializeApprovalInput {
	request := domain.ApprovalRequest{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1",
		AgentID: domain.ID(uuid.NewString()), AgentSlug: "billing", AgentName: "billing-agent",
		Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount":1}`),
		Status: domain.ApprovalPending,
	}
	return application.InitializeApprovalInput{Request: request, HumanReview: route}
}

func evaluationService(paths pathReaderStub) *EvaluationService {
	return NewEvaluationService(paths, evaluationClock{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func auditData(t *testing.T, decorated application.InitializeApprovalInput) map[string]any {
	t.Helper()
	last := decorated.AuditEvents[len(decorated.AuditEvents)-1]
	if last.EventType != "routing.evaluated" || last.ActorType != domain.ActorSystem {
		t.Fatalf("audit event = %+v", last)
	}
	var data map[string]any
	if err := json.Unmarshal(last.Data, &data); err != nil {
		t.Fatalf("audit data: %v", err)
	}
	return data
}

func TestDecoratePinsTheBlocksPathAndItsActiveRevision(t *testing.T) {
	pipelineID, blockID := domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "payments", Revision: 4, IsActive: true,
	}
	service := evaluationService(pathReaderStub{revisions: map[string]domain.EscalationPathRevision{"payments": pathRevision}})

	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "payments", Reason: domain.RoutingReasonBlockEscalation, PipelineRevisionID: pipelineID, BlockID: blockID,
	}))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.Outcome != domain.RoutingEscalated ||
		snapshot.Reason != domain.RoutingReasonBlockEscalation ||
		snapshot.EscalationPathKey != "payments" || snapshot.EscalationPathRevisionID != pathRevision.ID ||
		snapshot.PipelineRevisionID != pipelineID || snapshot.BlockID != blockID ||
		snapshot.Urgency != "standard" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.RequestID != decorated.Request.ID || snapshot.CreatedAt.IsZero() {
		t.Fatalf("snapshot identity not stamped: %+v", snapshot)
	}
	data := auditData(t, decorated)
	if data["outcome"] != "escalated" || data["escalation_path_key"] != "payments" ||
		data["escalation_path_revision_id"] != string(pathRevision.ID) || data["block_id"] != string(blockID) {
		t.Fatalf("audit data = %v", data)
	}
}

func TestDecoratePinsThePipelineDefaultWithoutABlock(t *testing.T) {
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "org-default", Revision: 1, IsActive: true,
	}
	service := evaluationService(pathReaderStub{revisions: map[string]domain.EscalationPathRevision{"org-default": pathRevision}})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "org-default", Reason: domain.RoutingReasonPipelineDefault, PipelineRevisionID: domain.ID(uuid.NewString()),
	}))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.Outcome != domain.RoutingEscalated ||
		snapshot.Reason != domain.RoutingReasonPipelineDefault ||
		snapshot.EscalationPathRevisionID != pathRevision.ID || snapshot.BlockID != "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestDecorateLeavesDecidedRequestsAlone(t *testing.T) {
	service := evaluationService(pathReaderStub{})
	initialization := pendingInitialization(nil)
	initialization.Request.Status = domain.ApprovalApproved
	decorated := service.Decorate(context.Background(), initialization)
	if decorated.Routing != nil || len(decorated.AuditEvents) != 0 {
		t.Fatalf("decided request was decorated: %+v", decorated)
	}
}

func TestDecorateFallsToTheQueueWithoutAPath(t *testing.T) {
	service := evaluationService(pathReaderStub{})
	for name, route := range map[string]*application.HumanReviewRoute{
		"no route": nil,
		"no path":  {Reason: domain.RoutingReasonNoEscalation, PipelineRevisionID: domain.ID(uuid.NewString())},
	} {
		t.Run(name, func(t *testing.T) {
			decorated := service.Decorate(context.Background(), pendingInitialization(route))
			snapshot := decorated.Routing
			if snapshot == nil || snapshot.Outcome != domain.RoutingDefaultQueue ||
				snapshot.Reason != domain.RoutingReasonNoEscalation || snapshot.Urgency != "standard" ||
				snapshot.EscalationPathKey != "" || snapshot.EscalationPathRevisionID != "" {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			if data := auditData(t, decorated); data["outcome"] != "default_queue" {
				t.Fatalf("audit data = %v", data)
			}
		})
	}
}

func TestDecorateFallsToTheQueueWhenThePathIsInactive(t *testing.T) {
	service := evaluationService(pathReaderStub{})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "payments", Reason: domain.RoutingReasonBlockEscalation, BlockID: domain.ID(uuid.NewString()),
	}))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.Outcome != domain.RoutingDefaultQueue ||
		snapshot.Reason != domain.RoutingReasonPathInactive || snapshot.EscalationPathKey != "" || snapshot.BlockID != "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if data := auditData(t, decorated); data["escalation_path_key"] != "payments" {
		t.Fatalf("audit data must still name the path: %v", data)
	}
}

func TestDecorateFallsToTheQueueWhenPathLookupFails(t *testing.T) {
	service := evaluationService(pathReaderStub{err: errors.New("database down")})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "payments", Reason: domain.RoutingReasonBlockEscalation,
	}))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.Outcome != domain.RoutingDefaultQueue ||
		snapshot.Reason != domain.RoutingReasonEvaluationError {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestDecorateRejectsUnusableRoute(t *testing.T) {
	service := evaluationService(pathReaderStub{})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "Not A Key", Reason: domain.RoutingReasonBlockEscalation,
	}))
	if snapshot := decorated.Routing; snapshot == nil || snapshot.Outcome != domain.RoutingDefaultQueue ||
		snapshot.Reason != domain.RoutingReasonEvaluationError {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

// teamTargetPathDocument is a path whose single level targets the given
// teams and one user.
func teamTargetPathDocument(userID domain.ID, teamIDs ...domain.ID) json.RawMessage {
	targets := []map[string]string{{"type": "user", "id": string(userID)}}
	for _, teamID := range teamIDs {
		targets = append(targets, map[string]string{"type": "team", "id": string(teamID)})
	}
	document, _ := json.Marshal(map[string]any{
		"working_hours": []any{},
		"nodes": []map[string]any{{
			"id": uuid.NewString(), "type": "level", "targets": targets, "escalate_after": "15m",
		}},
	})
	return document
}

func TestDecoratePinsActiveTeamPoliciesOfTargetedTeams(t *testing.T) {
	withPolicy, withoutPolicy := domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "payments", Revision: 1, IsActive: true,
		Document: teamTargetPathDocument(domain.ID(uuid.NewString()), withPolicy, withoutPolicy), DocumentVersion: 2,
	}
	policy := domain.TeamEscalationPolicyRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", TeamID: withPolicy, Revision: 3, IsActive: true,
	}
	var asked []domain.ID
	service := evaluationService(pathReaderStub{
		revisions:  map[string]domain.EscalationPathRevision{"payments": pathRevision},
		policies:   map[domain.ID]domain.TeamEscalationPolicyRevision{withPolicy: policy},
		askedTeams: &asked,
	})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "payments", Reason: domain.RoutingReasonPipelineDefault, PipelineRevisionID: domain.ID(uuid.NewString()),
	}))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.EscalationPathRevisionID != pathRevision.ID {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(asked) != 2 || asked[0] != withPolicy || asked[1] != withoutPolicy {
		t.Fatalf("asked about teams %v, want both targeted teams", asked)
	}
	if len(snapshot.TeamPolicies) != 1 || snapshot.TeamPolicies[0].TeamID != withPolicy ||
		snapshot.TeamPolicies[0].PolicyRevisionID != policy.ID || snapshot.TeamPolicies[0].Revision != 3 {
		t.Fatalf("pinned team policies = %+v, want the one active policy", snapshot.TeamPolicies)
	}
	data := auditData(t, decorated)
	listed, _ := data["team_policies"].([]any)
	if len(listed) != 1 {
		t.Fatalf("audit team_policies = %v", data["team_policies"])
	}
	entry, _ := listed[0].(map[string]any)
	if entry["team_id"] != string(withPolicy) || entry["policy_revision_id"] != string(policy.ID) || entry["revision"] != float64(3) {
		t.Fatalf("audit team policy entry = %v", entry)
	}
}

func TestDecoratePinsNoTeamPoliciesWithoutActiveOnes(t *testing.T) {
	teamID := domain.ID(uuid.NewString())
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "payments", Revision: 1, IsActive: true,
		Document: teamTargetPathDocument(domain.ID(uuid.NewString()), teamID), DocumentVersion: 2,
	}
	service := evaluationService(pathReaderStub{revisions: map[string]domain.EscalationPathRevision{"payments": pathRevision}})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "payments", Reason: domain.RoutingReasonPipelineDefault,
	}))
	if snapshot := decorated.Routing; snapshot == nil || len(snapshot.TeamPolicies) != 0 {
		t.Fatalf("snapshot = %+v, want no pinned team policies", snapshot)
	}
	if listed, _ := auditData(t, decorated)["team_policies"].([]any); len(listed) != 0 {
		t.Fatalf("audit team_policies = %v, want empty", listed)
	}
}

func TestDecorateKeepsThePathPinWhenTeamPolicyLookupFails(t *testing.T) {
	teamID := domain.ID(uuid.NewString())
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "payments", Revision: 1, IsActive: true,
		Document: teamTargetPathDocument(domain.ID(uuid.NewString()), teamID), DocumentVersion: 2,
	}
	service := evaluationService(pathReaderStub{
		revisions: map[string]domain.EscalationPathRevision{"payments": pathRevision},
		policyErr: errors.New("database down"),
	})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "payments", Reason: domain.RoutingReasonPipelineDefault,
	}))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.Outcome != domain.RoutingEscalated || snapshot.EscalationPathRevisionID != pathRevision.ID {
		t.Fatalf("snapshot = %+v, want the path still pinned", snapshot)
	}
	if len(snapshot.TeamPolicies) != 0 {
		t.Fatalf("pinned team policies = %+v, want none after a lookup failure", snapshot.TeamPolicies)
	}
}

func TestDecoratePinsNoTeamPoliciesForAnUnreadablePathDocument(t *testing.T) {
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "payments", Revision: 1, IsActive: true,
		Document: json.RawMessage(`{"nodes":[],"surprise":true}`), DocumentVersion: 2,
	}
	var asked []domain.ID
	service := evaluationService(pathReaderStub{
		revisions: map[string]domain.EscalationPathRevision{"payments": pathRevision}, askedTeams: &asked,
	})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "payments", Reason: domain.RoutingReasonPipelineDefault,
	}))
	if snapshot := decorated.Routing; snapshot == nil || snapshot.EscalationPathRevisionID != pathRevision.ID || len(snapshot.TeamPolicies) != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(asked) != 0 {
		t.Fatalf("service asked about teams %v from an unreadable document", asked)
	}
}

// branchingPathDocument routes on the request's tool: a request whose tool
// equals tool reaches thenTarget, any other reaches elseTarget. The
// condition's fact is pinned under "/" + conditionID.
func branchingPathDocument(conditionID, tool string, thenTarget, elseTarget map[string]string) json.RawMessage {
	document, _ := json.Marshal(map[string]any{
		"working_hours": []any{},
		"nodes": []map[string]any{{
			"id": uuid.NewString(), "type": "if_else",
			"conditions": []map[string]any{{"type": "request", "when": map[string]any{
				"id": conditionID, "field": "/request/tool", "operator": "equals", "value": tool,
			}}},
			"then": []map[string]any{{"id": uuid.NewString(), "type": "level", "targets": []map[string]string{thenTarget}, "escalate_after": "15m"}},
			"else": []map[string]any{{"id": uuid.NewString(), "type": "level", "targets": []map[string]string{elseTarget}, "escalate_after": "15m"}},
		}},
	})
	return document
}

func TestDecoratePinsRequestFactsAndOnlyReachableTeamPolicies(t *testing.T) {
	conditionID := uuid.NewString()
	thenTeam, elseTeam := domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "payments", Revision: 1, IsActive: true,
		Document: branchingPathDocument(conditionID, "stripe.refund",
			map[string]string{"type": "team", "id": string(thenTeam)}, map[string]string{"type": "team", "id": string(elseTeam)}),
		DocumentVersion: 2,
	}
	policyConditionID := uuid.NewString()
	policyDocument, _ := json.Marshal(map[string]any{
		"working_hours": []any{},
		"nodes": []map[string]any{{
			"id": uuid.NewString(), "type": "if_else",
			"conditions": []map[string]any{{"type": "request", "when": map[string]any{
				"id": policyConditionID, "field": "/request/tool", "operator": "equals", "value": "something.else",
			}}},
			"then": []map[string]any{{"id": uuid.NewString(), "type": "level", "targets": []map[string]string{{"type": "broadcast"}}, "escalate_after": "5m"}},
		}},
	})
	policies := map[domain.ID]domain.TeamEscalationPolicyRevision{
		thenTeam: {ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", TeamID: thenTeam, Revision: 2, IsActive: true, Document: policyDocument, DocumentVersion: 2},
		elseTeam: {ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", TeamID: elseTeam, Revision: 1, IsActive: true, Document: policyDocument, DocumentVersion: 2},
	}
	var asked []domain.ID
	service := evaluationService(pathReaderStub{
		revisions: map[string]domain.EscalationPathRevision{"payments": pathRevision},
		policies:  policies, askedTeams: &asked,
	})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "payments", Reason: domain.RoutingReasonPipelineDefault,
	}))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.Outcome != domain.RoutingEscalated {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	// The request's tool is stripe.refund, so the path condition holds and
	// the then team is the only reachable one.
	if got := snapshot.Facts["/"+conditionID]; !got {
		t.Fatalf("path fact = %v, want true; facts %v", got, snapshot.Facts)
	}
	if len(asked) != 1 || asked[0] != thenTeam {
		t.Fatalf("asked about teams %v, want only the reachable team", asked)
	}
	if len(snapshot.TeamPolicies) != 1 || snapshot.TeamPolicies[0].TeamID != thenTeam {
		t.Fatalf("pinned team policies = %+v, want the reachable team's policy", snapshot.TeamPolicies)
	}
	// The pinned policy's own request condition is evaluated under the
	// team's scope: the tool is not something.else, so it reads false.
	if got, present := snapshot.Facts[string(thenTeam)+"/"+policyConditionID]; !present || got {
		t.Fatalf("team fact = %v (present %v), want false", got, present)
	}
	if len(snapshot.Facts) != 2 {
		t.Fatalf("facts = %v, want exactly the path and team conditions", snapshot.Facts)
	}
	data := auditData(t, decorated)
	if data["fact_count"] != float64(2) {
		t.Fatalf("audit fact_count = %v", data["fact_count"])
	}
	reachable, _ := data["reachable_targets"].([]any)
	if len(reachable) != 1 || reachable[0] != "team:"+string(thenTeam) {
		t.Fatalf("audit reachable_targets = %v", data["reachable_targets"])
	}
}

func TestDecorateReadsABrokenConditionAsFalse(t *testing.T) {
	conditionID := uuid.NewString()
	thenUser, elseUser := uuid.NewString(), uuid.NewString()
	document, _ := json.Marshal(map[string]any{
		"working_hours": []any{},
		"nodes": []map[string]any{{
			"id": uuid.NewString(), "type": "if_else",
			"conditions": []map[string]any{{"type": "request", "when": map[string]any{
				"id": conditionID, "field": "/request/tool", "operator": "no_such_operator",
			}}},
			"then": []map[string]any{{"id": uuid.NewString(), "type": "level", "targets": []map[string]string{{"type": "user", "id": thenUser}}, "escalate_after": "15m"}},
			"else": []map[string]any{{"id": uuid.NewString(), "type": "level", "targets": []map[string]string{{"type": "user", "id": elseUser}}, "escalate_after": "15m"}},
		}},
	})
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "payments", Revision: 1, IsActive: true,
		Document: document, DocumentVersion: 2,
	}
	service := evaluationService(pathReaderStub{revisions: map[string]domain.EscalationPathRevision{"payments": pathRevision}})
	decorated := service.Decorate(context.Background(), pendingInitialization(&application.HumanReviewRoute{
		PathKey: "payments", Reason: domain.RoutingReasonPipelineDefault,
	}))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.Outcome != domain.RoutingEscalated {
		t.Fatalf("snapshot = %+v, want the path still pinned", snapshot)
	}
	if got, present := snapshot.Facts["/"+conditionID]; !present || got {
		t.Fatalf("broken condition fact = %v (present %v), want false", got, present)
	}
	reachable, _ := auditData(t, decorated)["reachable_targets"].([]any)
	if len(reachable) != 1 || reachable[0] != "user:"+elseUser {
		t.Fatalf("audit reachable_targets = %v, want the else branch", reachable)
	}
}

func ownerInitialization(ownerID domain.ID) application.InitializeApprovalInput {
	initialization := pendingInitialization(&application.HumanReviewRoute{
		PathKey: "platform", AskAgentOwner: true, Reason: domain.RoutingReasonPipelineDefault,
		PipelineRevisionID: domain.ID(uuid.NewString()),
	})
	initialization.Request.RequesterMembershipID = ownerID
	return initialization
}

func TestDecorateSendsAnOwnerRouteToTheOwnerWithTheFallbackPinned(t *testing.T) {
	ownerID := domain.ID(uuid.NewString())
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "platform", Revision: 2, IsActive: true,
	}
	service := evaluationService(pathReaderStub{
		revisions: map[string]domain.EscalationPathRevision{"platform": pathRevision},
		owners:    map[domain.ID]application.AgentOwnerStanding{ownerID: {Active: true, CanDecide: true}},
	})
	decorated := service.Decorate(context.Background(), ownerInitialization(ownerID))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.Outcome != domain.RoutingAgentOwner || snapshot.AgentOwnerMembershipID != ownerID ||
		snapshot.AgentOwnerFallback != "" || snapshot.Reason != domain.RoutingReasonPipelineDefault ||
		snapshot.EscalationPathKey != "platform" || snapshot.EscalationPathRevisionID != pathRevision.ID {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if data := auditData(t, decorated); data["outcome"] != "agent_owner" || data["agent_owner_membership_id"] != string(ownerID) {
		t.Fatalf("audit data = %v", data)
	}
}

func TestDecorateSendsAnOwnerRouteToItsFallbackWhenTheOwnerCannotTakeIt(t *testing.T) {
	ownerID := domain.ID(uuid.NewString())
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "platform", Revision: 1, IsActive: true,
	}
	for name, test := range map[string]struct {
		owner    domain.ID
		standing application.AgentOwnerStanding
		err      error
		reason   string
	}{
		"a service agent has no owner": {owner: "", reason: domain.AgentOwnerFallbackNoOwner},
		"a suspended owner":            {owner: ownerID, standing: application.AgentOwnerStanding{}, reason: domain.AgentOwnerFallbackInactive},
		"an owner who may not decide":  {owner: ownerID, standing: application.AgentOwnerStanding{Active: true}, reason: domain.AgentOwnerFallbackCannotDecide},
		"an owner lookup that failed":  {owner: ownerID, err: errors.New("database down"), reason: ""},
	} {
		t.Run(name, func(t *testing.T) {
			service := evaluationService(pathReaderStub{
				revisions: map[string]domain.EscalationPathRevision{"platform": pathRevision},
				owners:    map[domain.ID]application.AgentOwnerStanding{ownerID: test.standing},
				ownerErr:  test.err,
			})
			decorated := service.Decorate(context.Background(), ownerInitialization(test.owner))
			snapshot := decorated.Routing
			if snapshot == nil || snapshot.Outcome != domain.RoutingEscalated || snapshot.AgentOwnerMembershipID != "" ||
				snapshot.AgentOwnerFallback != test.reason || snapshot.EscalationPathRevisionID != pathRevision.ID {
				t.Fatalf("snapshot = %+v", snapshot)
			}
		})
	}
}

func TestDecorateKeepsTheOwnerWhenTheFallbackPathIsInactive(t *testing.T) {
	ownerID := domain.ID(uuid.NewString())
	service := evaluationService(pathReaderStub{
		owners: map[domain.ID]application.AgentOwnerStanding{ownerID: {Active: true, CanDecide: true}},
	})
	decorated := service.Decorate(context.Background(), ownerInitialization(ownerID))
	snapshot := decorated.Routing
	if snapshot == nil || snapshot.Outcome != domain.RoutingAgentOwner || snapshot.AgentOwnerMembershipID != ownerID ||
		snapshot.EscalationPathKey != "platform" || snapshot.EscalationPathRevisionID != "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	// Without an owner, the inactive fallback leaves the default queue,
	// still saying why the owner was not asked.
	decorated = service.Decorate(context.Background(), ownerInitialization(""))
	if snapshot := decorated.Routing; snapshot.Outcome != domain.RoutingDefaultQueue ||
		snapshot.Reason != domain.RoutingReasonPathInactive || snapshot.AgentOwnerFallback != domain.AgentOwnerFallbackNoOwner {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestDecorateIgnoresTheOwnerWithoutAnOwnerRoute(t *testing.T) {
	ownerID := domain.ID(uuid.NewString())
	pathRevision := domain.EscalationPathRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "organization-1", PathKey: "platform", Revision: 1, IsActive: true,
	}
	service := evaluationService(pathReaderStub{
		revisions: map[string]domain.EscalationPathRevision{"platform": pathRevision},
		owners:    map[domain.ID]application.AgentOwnerStanding{ownerID: {Active: true, CanDecide: true}},
	})
	initialization := ownerInitialization(ownerID)
	initialization.HumanReview.AskAgentOwner = false
	decorated := service.Decorate(context.Background(), initialization)
	if snapshot := decorated.Routing; snapshot.Outcome != domain.RoutingEscalated || snapshot.AgentOwnerMembershipID != "" || snapshot.AgentOwnerFallback != "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

// An owner route with no fallback path anywhere: the owner takes the
// request, and without one able to decide, any authorized reviewer does.
func TestDecorateAnOwnerRouteWithoutAPath(t *testing.T) {
	ownerID := domain.ID(uuid.NewString())
	service := evaluationService(pathReaderStub{
		owners: map[domain.ID]application.AgentOwnerStanding{ownerID: {Active: true, CanDecide: true}},
	})
	withoutPath := func(owner domain.ID) application.InitializeApprovalInput {
		initialization := ownerInitialization(owner)
		initialization.HumanReview.PathKey = ""
		return initialization
	}
	decorated := service.Decorate(context.Background(), withoutPath(ownerID))
	if snapshot := decorated.Routing; snapshot.Outcome != domain.RoutingAgentOwner || snapshot.AgentOwnerMembershipID != ownerID ||
		snapshot.EscalationPathKey != "" || snapshot.EscalationPathRevisionID != "" || snapshot.Reason != domain.RoutingReasonPipelineDefault {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	decorated = service.Decorate(context.Background(), withoutPath(""))
	if snapshot := decorated.Routing; snapshot.Outcome != domain.RoutingDefaultQueue || snapshot.Reason != domain.RoutingReasonNoEscalation ||
		snapshot.AgentOwnerFallback != domain.AgentOwnerFallbackNoOwner {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
