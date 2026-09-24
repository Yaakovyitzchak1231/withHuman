package authorization

import "github.com/withHumanAI/withHuman/domain"

type ClaimInput struct {
	MembershipStatus     domain.MembershipStatus
	Grants               []domain.Grant
	RequestID            domain.ID
	ReviewerMembershipID domain.ID
	// HasRoutingTargets is true when the request carries a matched routing
	// snapshot: only the routed targets and added reviewers may then
	// claim, exactly as for deciding. RoutedUserIDs and RoutedTeamIDs are
	// the pinned document's target union split by type; ReviewerTeamIDs is
	// the reviewer's team membership at claim time.
	HasRoutingTargets bool
	RoutedUserIDs     []domain.ID
	RoutedTeamIDs     []domain.ID
	AddedReviewerIDs  []domain.ID
	ReviewerTeamIDs   []domain.ID
	UnroutedRequests  domain.UnroutedRequestsPolicy
}

// EvaluateClaim is the policy kernel for claiming a pending request. Who may
// claim is who may decide, minus the decision-only checks: claiming is not a
// decision, so self-approval policy, authentication freshness or MFA
// assurance, and the break-glass acknowledgement do not apply to saying
// "I'm looking at it".
func EvaluateClaim(input ClaimInput) *Rejection {
	if input.MembershipStatus != domain.MembershipActive {
		return &Rejection{Code: "membership_inactive", Err: domain.ErrMembershipInactive}
	}
	resource := domain.RequestResource(input.RequestID)
	if !holds(input.Grants, domain.PermissionRequestClaim, resource) {
		return &Rejection{Code: "forbidden", Err: domain.ErrForbidden}
	}
	_, rejection := routingVerdict(routingInput{
		Grants: input.Grants, Resource: resource, ReviewerMembershipID: input.ReviewerMembershipID,
		HasRoutingTargets: input.HasRoutingTargets, RoutedUserIDs: input.RoutedUserIDs, RoutedTeamIDs: input.RoutedTeamIDs,
		AddedReviewerIDs: input.AddedReviewerIDs, ReviewerTeamIDs: input.ReviewerTeamIDs,
		UnroutedRequests: input.UnroutedRequests,
	})
	return rejection
}
