package authorization

import (
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

func TestEvaluateDecision(t *testing.T) {
	now := time.Unix(10_000, 0)
	valid := DecisionInput{
		MembershipStatus:     domain.MembershipActive,
		Grants:               grants(domain.PermissionRequestDecide, domain.PermissionRequestClaim),
		RequestID:            "request-1",
		ReviewerMembershipID: "reviewer",
		Authentication:       domain.AuthContext{AuthenticatedAt: now.Add(-time.Minute), Assurance: "strong"},
		ApprovalReauthWindow: time.Hour,
		Now:                  now,
	}

	tests := []struct {
		name   string
		mutate func(*DecisionInput)
		code   string
		err    error
	}{
		{name: "active and authorized", mutate: func(*DecisionInput) {}},
		{name: "inactive membership", mutate: func(input *DecisionInput) { input.MembershipStatus = domain.MembershipSuspended }, code: "membership_inactive", err: domain.ErrMembershipInactive},
		{name: "missing permission", mutate: func(input *DecisionInput) { input.Grants = nil }, code: "forbidden", err: domain.ErrForbidden},
		{name: "self approval", mutate: func(input *DecisionInput) { input.RequesterMembershipID = input.ReviewerMembershipID }, code: "self_approval", err: domain.ErrSelfApproval},
		{name: "stale authentication", mutate: func(input *DecisionInput) { input.Authentication.AuthenticatedAt = now.Add(-2 * time.Hour) }, code: "authentication_stale", err: domain.ErrAuthenticationStale},
		{name: "missing authentication time", mutate: func(input *DecisionInput) { input.Authentication.AuthenticatedAt = time.Time{} }, code: "authentication_stale", err: domain.ErrAuthenticationStale},
		{name: "weak authentication", mutate: func(input *DecisionInput) {
			input.MFAMode = domain.MFAReviewersRequired
			input.Authentication.Assurance = "single_factor"
		}, code: "authentication_weak", err: domain.ErrAuthenticationWeak},
		{name: "targeted directly", mutate: func(input *DecisionInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"reviewer"}
		}},
		{name: "targeted through team", mutate: func(input *DecisionInput) {
			input.HasRoutingTargets = true
			input.RoutedTeamIDs = []domain.ID{"team-1"}
			input.ReviewerTeamIDs = []domain.ID{"team-2", "team-1"}
		}},
		{name: "not targeted", mutate: func(input *DecisionInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"someone-else"}
			input.RoutedTeamIDs = []domain.ID{"team-1"}
			input.ReviewerTeamIDs = []domain.ID{"team-2"}
		}, code: "not_targeted", err: domain.ErrNotTargeted},
		{name: "empty target set rejects everyone without break glass", mutate: func(input *DecisionInput) {
			input.HasRoutingTargets = true
		}, code: "not_targeted", err: domain.ErrNotTargeted},
		{name: "break glass needs the acknowledgement", mutate: func(input *DecisionInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"someone-else"}
			input.Grants = append(input.Grants, domain.Grant{Permission: domain.PermissionRequestDecideUnrouted, ScopeKind: domain.ScopeOrganization})
		}, code: "outside_routing_required", err: domain.ErrNotTargeted},
		{name: "break glass bypasses targeting when acknowledged", mutate: func(input *DecisionInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"someone-else"}
			input.OutsideRouting = true
			input.Grants = append(input.Grants, domain.Grant{Permission: domain.PermissionRequestDecideUnrouted, ScopeKind: domain.ScopeOrganization})
		}},
		{name: "added reviewer is targeted", mutate: func(input *DecisionInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"someone-else"}
			input.AddedReviewerIDs = []domain.ID{"reviewer"}
		}},
		{name: "unrouted request with any reviewer policy", mutate: func(input *DecisionInput) {
			input.UnroutedRequests = domain.UnroutedAnyReviewer
		}},
		{name: "unrouted request under break glass only refuses a plain reviewer", mutate: func(input *DecisionInput) {
			input.UnroutedRequests = domain.UnroutedBreakGlassOnly
		}, code: "break_glass_only", err: domain.ErrNotTargeted},
		{name: "unrouted request under break glass only admits an acknowledged holder", mutate: func(input *DecisionInput) {
			input.UnroutedRequests = domain.UnroutedBreakGlassOnly
			input.OutsideRouting = true
			input.Grants = append(input.Grants, domain.Grant{Permission: domain.PermissionRequestDecideUnrouted, ScopeKind: domain.ScopeOrganization})
		}},
		{name: "no routing snapshot stays unscoped", mutate: func(input *DecisionInput) {
			input.RoutedUserIDs = []domain.ID{"someone-else"}
		}},
		{name: "targeting rejection outranks stale authentication", mutate: func(input *DecisionInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"someone-else"}
			input.Authentication.AuthenticatedAt = now.Add(-2 * time.Hour)
		}, code: "not_targeted", err: domain.ErrNotTargeted},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			input.Grants = append([]domain.Grant(nil), valid.Grants...)
			test.mutate(&input)
			_, rejection := EvaluateDecision(input)
			if test.err == nil {
				if rejection != nil {
					t.Fatalf("unexpected rejection: %s: %v", rejection.Code, rejection.Err)
				}
				return
			}
			if rejection == nil || rejection.Code != test.code || !errors.Is(rejection.Err, test.err) {
				t.Fatalf("got %#v, want %s: %v", rejection, test.code, test.err)
			}
		})
	}
}

func grants(keys ...domain.Permission) []domain.Grant {
	return domain.OrganizationGrants(keys)
}

func TestEvaluateDecisionRefusesScopedGrantOnRequests(t *testing.T) {
	now := time.Now()
	_, rejection := EvaluateDecision(DecisionInput{
		MembershipStatus:     domain.MembershipActive,
		Grants:               []domain.Grant{{Permission: domain.PermissionRequestDecide, ScopeKind: domain.ScopeAgent, ScopeID: "refund-bot"}},
		RequestID:            "request-1",
		ReviewerMembershipID: "reviewer",
		Authentication:       domain.AuthContext{AuthenticatedAt: now, Assurance: "strong"},
		ApprovalReauthWindow: time.Hour,
		Now:                  now,
	})
	if rejection == nil || rejection.Code != "forbidden" {
		t.Fatalf("a request is an organization resource; agent-type grants must not decide it: %#v", rejection)
	}
}

func TestEvaluateDecisionReportsOutsideRouting(t *testing.T) {
	now := time.Now()
	verdict, rejection := EvaluateDecision(DecisionInput{
		MembershipStatus:     domain.MembershipActive,
		Grants:               grants(domain.PermissionRequestDecide, domain.PermissionRequestDecideUnrouted),
		RequestID:            "request-1",
		ReviewerMembershipID: "reviewer",
		HasRoutingTargets:    true,
		RoutedUserIDs:        []domain.ID{"someone-else"},
		OutsideRouting:       true,
		Authentication:       domain.AuthContext{AuthenticatedAt: now, Assurance: "strong"},
		ApprovalReauthWindow: time.Hour,
		Now:                  now,
	})
	if rejection != nil || !verdict.OutsideRouting || verdict.Reason != "not_targeted" {
		t.Fatalf("expected an outside-routing verdict, got %#v / %#v", verdict, rejection)
	}
}

// A personal API key never signs in, so the re-authentication window is
// skipped for it; the MFA rule still applies through the assurance the
// key inherited at issue.
func TestRequireFreshSkipsTheWindowForAPIKeys(t *testing.T) {
	now := time.Unix(10_000, 0)
	stale := domain.AuthContext{Method: domain.AuthMethodAPIKey, Assurance: "strong", AuthenticatedAt: now.Add(-48 * time.Hour)}
	if rejection := RequireFresh(stale, time.Hour, domain.MFAReviewersRequired, now); rejection != nil {
		t.Fatalf("strong key refused: %v", rejection.Code)
	}
	weak := domain.AuthContext{Method: domain.AuthMethodAPIKey, Assurance: "single_factor", AuthenticatedAt: now}
	if rejection := RequireFresh(weak, time.Hour, domain.MFAReviewersRequired, now); rejection == nil || rejection.Code != "authentication_weak" {
		t.Fatalf("single-factor key under reviewers_required = %v, want authentication_weak", rejection)
	}
	if rejection := RequireFresh(weak, time.Hour, domain.MFAOptional, now); rejection != nil {
		t.Fatalf("single-factor key under optional refused: %v", rejection.Code)
	}
	session := domain.AuthContext{Method: "password", Assurance: "strong", AuthenticatedAt: now.Add(-48 * time.Hour)}
	if rejection := RequireFresh(session, time.Hour, domain.MFAOptional, now); rejection == nil || rejection.Code != "authentication_stale" {
		t.Fatalf("stale session = %v, want authentication_stale", rejection)
	}
}
