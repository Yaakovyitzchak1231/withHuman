package authorization

import (
	"errors"
	"testing"

	"github.com/withHumanAI/withHuman/domain"
)

func TestEvaluateClaim(t *testing.T) {
	valid := ClaimInput{
		MembershipStatus:     domain.MembershipActive,
		Grants:               grants(domain.PermissionRequestDecide, domain.PermissionRequestClaim),
		RequestID:            "request-1",
		ReviewerMembershipID: "reviewer",
	}

	tests := []struct {
		name   string
		mutate func(*ClaimInput)
		code   string
		err    error
	}{
		{name: "active and authorized", mutate: func(*ClaimInput) {}},
		{name: "inactive membership", mutate: func(input *ClaimInput) { input.MembershipStatus = domain.MembershipSuspended }, code: "membership_inactive", err: domain.ErrMembershipInactive},
		{name: "missing permission", mutate: func(input *ClaimInput) { input.Grants = nil }, code: "forbidden", err: domain.ErrForbidden},
		{name: "targeted directly", mutate: func(input *ClaimInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"reviewer"}
		}},
		{name: "targeted through team", mutate: func(input *ClaimInput) {
			input.HasRoutingTargets = true
			input.RoutedTeamIDs = []domain.ID{"team-1"}
			input.ReviewerTeamIDs = []domain.ID{"team-2", "team-1"}
		}},
		{name: "not targeted", mutate: func(input *ClaimInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"someone-else"}
			input.RoutedTeamIDs = []domain.ID{"team-1"}
			input.ReviewerTeamIDs = []domain.ID{"team-2"}
		}, code: "not_targeted", err: domain.ErrNotTargeted},
		{name: "break glass bypasses targeting", mutate: func(input *ClaimInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"someone-else"}
			input.Grants = append(input.Grants, domain.Grant{Permission: domain.PermissionRequestDecideUnrouted, ScopeKind: domain.ScopeOrganization})
		}},
		{name: "no routing snapshot stays unscoped", mutate: func(input *ClaimInput) {
			input.RoutedUserIDs = []domain.ID{"someone-else"}
		}},
		{name: "added reviewer may claim", mutate: func(input *ClaimInput) {
			input.HasRoutingTargets = true
			input.RoutedUserIDs = []domain.ID{"someone-else"}
			input.AddedReviewerIDs = []domain.ID{"reviewer"}
		}},
		{name: "break glass only refuses a plain reviewer on an unrouted request", mutate: func(input *ClaimInput) {
			input.UnroutedRequests = domain.UnroutedBreakGlassOnly
		}, code: "break_glass_only", err: domain.ErrNotTargeted},
		{name: "break glass holder claims without acknowledgement", mutate: func(input *ClaimInput) {
			input.UnroutedRequests = domain.UnroutedBreakGlassOnly
			input.Grants = append(input.Grants, domain.Grant{Permission: domain.PermissionRequestDecideUnrouted, ScopeKind: domain.ScopeOrganization})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			input.Grants = append([]domain.Grant(nil), valid.Grants...)
			test.mutate(&input)
			rejection := EvaluateClaim(input)
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
