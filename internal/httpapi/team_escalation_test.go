package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/teamescalation"
	"github.com/withHumanAI/withHuman/internal/teams"
	"github.com/withHumanAI/withHuman/routing"
)

const (
	teamEscalationHTTPTeamID   = domain.ID("71111111-1111-4111-8111-111111111111")
	teamEscalationHTTPMember   = "51111111-1111-4111-8111-111111111111"
	teamEscalationHTTPOutsider = "54444444-4444-4444-8444-444444444444"
)

const teamEscalationHTTPDocument = `{"working_hours":[],"nodes":[{"id":"41111111-1111-4111-8111-111111111111","type":"level","targets":[{"type":"user","id":"` + teamEscalationHTTPMember + `"}],"escalate_after":"15m"},{"id":"42222222-2222-4222-8222-222222222222","type":"level","targets":[{"type":"broadcast"}],"escalate_after":"30m"}]}`

func TestTeamEscalationRoutesAreProductAPIs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/teams/t1/escalation-policy"},
		{method: http.MethodGet, path: "/teams/t1/escalation-policy/revisions"},
		{method: http.MethodPost, path: "/teams/t1/escalation-policy/revisions"},
		{method: http.MethodGet, path: "/teams/t1/escalation-policy/revisions/1"},
		{method: http.MethodPost, path: "/teams/t1/escalation-policy/revisions/1/activate"},
		{method: http.MethodDelete, path: "/teams/t1/escalation-policy/active"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, "/api/v1"+test.path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("product route: got status %d, want %d", response.Code, http.StatusUnauthorized)
			}
			response = httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, "/api/aap/v1"+test.path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("team policy control plane leaked into AAP: status %d", response.Code)
			}
		})
	}
}

type teamEscalationHTTPPolicies struct {
	application.TeamEscalationPolicyRepository
	revision  domain.TeamEscalationPolicyRevision
	created   *application.CreateTeamEscalationPolicyRevisionInput
	activated *application.ActivateTeamEscalationPolicyRevisionInput
}

func teamEscalationHTTPPoliciesWithRevision() *teamEscalationHTTPPolicies {
	return &teamEscalationHTTPPolicies{revision: domain.TeamEscalationPolicyRevision{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OrganizationID: "organization-1", TeamID: teamEscalationHTTPTeamID,
		Revision: 1, Document: json.RawMessage(teamEscalationHTTPDocument), DocumentVersion: routing.DocumentVersion,
		CreatedByActorID: "membership-1", CreatedAt: time.Unix(80_000, 0).UTC(),
	}}
}

func (r *teamEscalationHTTPPolicies) CreateTeamEscalationPolicyRevision(_ context.Context, input application.CreateTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error) {
	r.created = &input
	return domain.TeamEscalationPolicyRevision{
		ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", OrganizationID: input.OrganizationID, TeamID: input.TeamID,
		Revision: 2, Document: input.Document, DocumentVersion: input.DocumentVersion,
		CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
	}, nil
}

func (r *teamEscalationHTTPPolicies) GetTeamEscalationPolicyRevision(_ context.Context, input application.GetTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error) {
	if input.TeamID != r.revision.TeamID || input.Revision != r.revision.Revision {
		return domain.TeamEscalationPolicyRevision{}, domain.ErrNotFound
	}
	return r.revision, nil
}

func (r *teamEscalationHTTPPolicies) GetActiveTeamEscalationPolicyRevision(_ context.Context, input application.GetActiveTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error) {
	if input.TeamID != r.revision.TeamID || !r.revision.IsActive {
		return domain.TeamEscalationPolicyRevision{}, domain.ErrNotFound
	}
	return r.revision, nil
}

func (r *teamEscalationHTTPPolicies) ActivateTeamEscalationPolicyRevision(_ context.Context, input application.ActivateTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error) {
	r.activated = &input
	r.revision.IsActive = true
	return r.revision, nil
}

func (r *teamEscalationHTTPPolicies) DeactivateTeamEscalationPolicy(_ context.Context, _ application.DeactivateTeamEscalationPolicyInput) (domain.TeamEscalationPolicyRevision, error) {
	r.revision.IsActive = false
	return r.revision, nil
}

// teamEscalationHTTPTeams is a one-team roster whose member replacement
// refuses when the policy still names someone being removed.
type teamEscalationHTTPTeams struct {
	application.TeamRepository
	policyNames []domain.ID
	set         *application.SetTeamManualMembersInput
}

func (t *teamEscalationHTTPTeams) GetTeam(_ context.Context, input application.GetTeamInput) (domain.Team, []application.TeamMemberRecord, error) {
	if input.TeamID != teamEscalationHTTPTeamID {
		return domain.Team{}, nil, domain.ErrNotFound
	}
	return domain.Team{ID: input.TeamID, OrganizationID: input.OrganizationID, Name: "Payments"}, []application.TeamMemberRecord{
		{MembershipID: teamEscalationHTTPMember, DisplayName: "Ada", Status: domain.MembershipActive},
	}, nil
}

func (t *teamEscalationHTTPTeams) CountTeamMembersLacking(context.Context, application.CountTeamMembersLackingInput) (application.TeamMemberPermissionCount, error) {
	return application.TeamMemberPermissionCount{MemberCount: 1}, nil
}

func (t *teamEscalationHTTPTeams) SetTeamManualMembers(_ context.Context, input application.SetTeamManualMembersInput) ([]application.TeamMemberRecord, error) {
	t.set = &input
	if len(t.policyNames) > 0 && !input.Force {
		return nil, domain.TeamMemberInEscalationPolicyError{TeamID: input.TeamID, Revision: 1, MembershipIDs: t.policyNames}
	}
	return []application.TeamMemberRecord{}, nil
}

func teamEscalationTestRouter(policies *teamEscalationHTTPPolicies, roster *teamEscalationHTTPTeams, principal domain.Principal) *gin.Engine {
	api := &API{
		teamEscalation: teamescalation.NewService(policies, roster, routingHTTPClock{}),
		teams:          teams.NewService(roster, routingHTTPClock{}),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	router := gin.New()
	router.Use(api.observability())
	group := router.Group("/api/v1", func(c *gin.Context) {
		setPrincipal(c, principal)
		c.Next()
	})
	api.installTeamRoutes(group)
	api.installTeamEscalationRoutes(group)
	return router
}

func teamEscalationHTTPPrincipal() domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "organization-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{
		domain.PermissionTeamRead, domain.PermissionTeamMemberWrite,
		domain.PermissionTeamEscalationWrite, domain.PermissionTeamEscalationActivate,
	}))
}

func teamEscalationPolicyPath(suffix string) string {
	return "/api/v1/teams/" + string(teamEscalationHTTPTeamID) + "/escalation-policy" + suffix
}

func TestTeamEscalationPolicyRevisionCreationReturnsCanonicalDocument(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policies := teamEscalationHTTPPoliciesWithRevision()
	router := teamEscalationTestRouter(policies, &teamEscalationHTTPTeams{}, teamEscalationHTTPPrincipal())

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, teamEscalationPolicyPath("/revisions"), strings.NewReader(teamEscalationHTTPDocument))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	var created teamEscalationPolicyRevisionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.TeamID != teamEscalationHTTPTeamID || created.Revision != 2 || len(created.Nodes) != 2 ||
		created.Nodes[1].Targets[0].Type != routing.TargetBroadcast || created.DocumentVersion != routing.DocumentVersion {
		t.Fatalf("unexpected creation response: %s", response.Body.String())
	}
	if created.Warnings == nil || len(created.Warnings) != 0 {
		t.Fatalf("a roster that can decide carries an empty warnings array: %s", response.Body.String())
	}
	if policies.created == nil || policies.created.OrganizationID != "organization-1" || policies.created.TeamID != teamEscalationHTTPTeamID {
		t.Fatalf("repository create input = %+v", policies.created)
	}
	var stored map[string]any
	if err := json.Unmarshal(policies.created.Document, &stored); err != nil {
		t.Fatalf("stored document: %v", err)
	}
	if _, named := stored["name"]; named {
		t.Fatal("a team policy document carries no name")
	}

	// A name is not part of the grammar: the strict decoder refuses it.
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, teamEscalationPolicyPath("/revisions"), strings.NewReader(`{"name":"Payments",`+teamEscalationHTTPDocument[1:]))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("named document = %d, want 400", response.Code)
	}

	outsider := strings.Replace(teamEscalationHTTPDocument, teamEscalationHTTPMember, teamEscalationHTTPOutsider, 1)
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, teamEscalationPolicyPath("/revisions"), strings.NewReader(outsider))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("non-member target = %d, want 400", response.Code)
	}
}

func TestTeamEscalationPolicyActivationSetsETagAndArchiveNeedsIfMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policies := teamEscalationHTTPPoliciesWithRevision()
	router := teamEscalationTestRouter(policies, &teamEscalationHTTPTeams{}, teamEscalationHTTPPrincipal())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, teamEscalationPolicyPath(""), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("no active revision = %d, want 404", response.Code)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, teamEscalationPolicyPath("/revisions/1/activate"), nil))
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("activation without If-Match = %d, want 428", response.Code)
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, teamEscalationPolicyPath("/revisions/1/activate"), nil)
	request.Header.Set("If-Match", `"0"`)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("activation = %d, ETag %q: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if policies.activated == nil || policies.activated.ExpectedActiveRevision != nil {
		t.Fatalf(`If-Match "0" means no expected active revision: %+v`, policies.activated)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, teamEscalationPolicyPath(""), nil))
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("active read = %d, ETag %q", response.Code, response.Header().Get("ETag"))
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, teamEscalationPolicyPath("/active"), nil))
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("archive without If-Match = %d, want 428", response.Code)
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodDelete, teamEscalationPolicyPath("/active"), nil)
	request.Header.Set("If-Match", `"1"`)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("archive = %d: %s", response.Code, response.Body.String())
	}
}

func TestTeamEscalationRoutesRequirePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	principal := teamEscalationHTTPPrincipal().WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionTeamRead}))
	router := teamEscalationTestRouter(teamEscalationHTTPPoliciesWithRevision(), &teamEscalationHTTPTeams{}, principal)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, teamEscalationPolicyPath("/revisions"), strings.NewReader(teamEscalationHTTPDocument))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("without team.escalation.write = %d, want 403", response.Code)
	}
}

// Removing a member the active policy names answers 409 with the team,
// revision, and memberships, and force carries through to the store.
func TestSetTeamMembersRefusesPolicyTargetsUnlessForced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	roster := &teamEscalationHTTPTeams{policyNames: []domain.ID{teamEscalationHTTPMember}}
	router := teamEscalationTestRouter(teamEscalationHTTPPoliciesWithRevision(), roster, teamEscalationHTTPPrincipal())
	path := "/api/v1/teams/" + string(teamEscalationHTTPTeamID) + "/members"

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"membership_ids":[]}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("removal = %d: %s", response.Code, response.Body.String())
	}
	var refused struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				TeamID        string   `json:"team_id"`
				Revision      int64    `json:"revision"`
				MembershipIDs []string `json:"membership_ids"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Error.Code != "team_member_in_escalation_policy" || refused.Error.Details.TeamID != string(teamEscalationHTTPTeamID) ||
		refused.Error.Details.Revision != 1 || len(refused.Error.Details.MembershipIDs) != 1 || refused.Error.Details.MembershipIDs[0] != teamEscalationHTTPMember {
		t.Fatalf("refusal = %s", response.Body.String())
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"membership_ids":[],"force":true}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("forced removal = %d: %s", response.Code, response.Body.String())
	}
	if roster.set == nil || !roster.set.Force {
		t.Fatalf("force did not reach the repository: %+v", roster.set)
	}
}

// A refused member suspension names the team policies beside the paths.
func TestMembershipInEscalationPathDetailsCarryTeamPolicies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPatch, "/api/v1/members/m1", nil)
	WriteError(c, slog.New(slog.NewTextHandler(io.Discard, nil)), domain.MembershipInEscalationPathError{
		MembershipID: "m1",
		TeamPolicies: []domain.TeamEscalationPolicyReference{{TeamID: teamEscalationHTTPTeamID, TeamName: "Payments", Revision: 3}},
	})
	var refused struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Paths    []map[string]any `json:"escalation_paths"`
				Policies []map[string]any `json:"team_escalation_policies"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Error.Code != "membership_in_escalation_path" || refused.Error.Details.Paths == nil || len(refused.Error.Details.Policies) != 1 ||
		refused.Error.Details.Policies[0]["team_name"] != "Payments" || refused.Error.Details.Policies[0]["revision"] != float64(3) {
		t.Fatalf("refusal = %s", response.Body.String())
	}
}

// The routing snapshot always carries team_policies and escalation, so a
// page never has to probe for them.
func TestApprovalRoutingResponseCarriesTeamPoliciesAndProgress(t *testing.T) {
	now := time.Unix(80_000, 0).UTC()
	bare, err := json.Marshal(newApprovalRoutingResponse(application.ApprovalRoutingView{
		ApprovalRequestRouting: domain.ApprovalRequestRouting{Outcome: domain.RoutingDefaultQueue, Reason: domain.RoutingReasonNoEscalation, CreatedAt: now},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(bare, &decoded); err != nil {
		t.Fatal(err)
	}
	if policies, ok := decoded["team_policies"].([]any); !ok || len(policies) != 0 {
		t.Fatalf("team_policies = %v, want empty array", decoded["team_policies"])
	}
	if value, present := decoded["escalation"]; !present || value != nil {
		t.Fatalf("escalation = %v, want explicit null", decoded["escalation"])
	}

	fired := now.Add(15 * time.Minute)
	progressed, err := json.Marshal(newApprovalRoutingResponse(application.ApprovalRoutingView{
		ApprovalRequestRouting: domain.ApprovalRequestRouting{Outcome: domain.RoutingEscalated, Reason: domain.RoutingReasonBlockEscalation, CreatedAt: now},
		PinnedTeamPolicies:     []application.PinnedTeamPolicy{{TeamID: teamEscalationHTTPTeamID, TeamName: "Payments", PolicyRevisionID: "p1", Revision: 2}},
		Escalation: &application.EscalationProgress{
			Sequence: 3, Urgency: routing.UrgencyInterrupt, UpdatedAt: now,
			Organization: application.EscalationWalkerProgress{Phase: application.EscalationPhaseLevel, CurrentNodeID: "n1", LevelOrdinal: 1, LevelCount: 2, NextAt: fired},
			Teams:        []application.EscalationWalkerProgress{{TeamID: teamEscalationHTTPTeamID, Phase: application.EscalationPhaseExhausted, LevelOrdinal: 2, LevelCount: 2, Repeat: 1}},
		},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(progressed, &decoded); err != nil {
		t.Fatal(err)
	}
	escalation := decoded["escalation"].(map[string]any)
	teamsProgress := escalation["teams"].([]any)[0].(map[string]any)
	if escalation["phase"] != "level" || escalation["urgency"] != "interrupt" || escalation["current_node_id"] != "n1" ||
		escalation["next_escalation_at"] != fired.Format(time.RFC3339) || escalation["sequence"] != float64(3) {
		t.Fatalf("escalation = %v", escalation)
	}
	if _, armed := teamsProgress["next_escalation_at"]; armed || teamsProgress["phase"] != "exhausted" || teamsProgress["repeat"] != float64(1) {
		t.Fatalf("team progress = %v", teamsProgress)
	}
	if _, present := teamsProgress["current_node_id"]; present {
		t.Fatalf("an exhausted walker has no current node: %v", teamsProgress)
	}
	pinned := decoded["team_policies"].([]any)[0].(map[string]any)
	if pinned["team_name"] != "Payments" || pinned["policy_revision_id"] != "p1" || pinned["revision"] != float64(2) {
		t.Fatalf("pinned policy = %v", pinned)
	}
}
