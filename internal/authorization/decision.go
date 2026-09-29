package authorization

import (
	"slices"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

type DecisionInput struct {
	MembershipStatus domain.MembershipStatus
	// Grants is the reviewer's grant set as reloaded inside the decision
	// transaction; RequestID names the request the grants are checked
	// against.
	Grants               []domain.Grant
	RequestID            domain.ID
	ReviewerMembershipID domain.ID
	// HasRoutingTargets is true when the request carries a matched routing
	// snapshot: only the routed targets may then decide, unless the
	// reviewer holds request.decide.unrouted (break glass) and says so.
	// RoutedUserIDs and RoutedTeamIDs are the pinned document's target
	// union split by type; AddedReviewerIDs are the people added to the
	// request after routing; ReviewerTeamIDs is the reviewer's team
	// membership at decision time.
	HasRoutingTargets bool
	RoutedUserIDs     []domain.ID
	RoutedTeamIDs     []domain.ID
	AddedReviewerIDs  []domain.ID
	ReviewerTeamIDs   []domain.ID
	// UnroutedRequests is the organization's policy for requests no path
	// matched: any reviewer, or break-glass holders only.
	UnroutedRequests domain.UnroutedRequestsPolicy
	// OutsideRouting is the reviewer's explicit acknowledgement that the
	// decision is made outside routing. Without it a break-glass holder
	// who is not targeted is refused, so nothing breaks glass by accident.
	OutsideRouting       bool
	Authentication       domain.AuthContext
	ApprovalReauthWindow time.Duration
	MFAMode              domain.MFAMode
	Now                  time.Time
}

type Rejection struct {
	Code string
	Err  error
}

// Verdict says how an allowed decision was allowed, for the audit trail.
type Verdict struct {
	// OutsideRouting is true when the reviewer was not among the request's
	// targets and decided through request.decide.unrouted.
	OutsideRouting bool
	// Reason explains why routing did not cover the reviewer:
	// "not_targeted" or "break_glass_only".
	Reason string
}

// EvaluateDecision is the provider-neutral policy kernel used inside the
// serializable decision transaction. Keeping it pure makes every denial path
// cheap to test without weakening the database recheck at commit time.
func EvaluateDecision(input DecisionInput) (Verdict, *Rejection) {
	if input.MembershipStatus != domain.MembershipActive {
		return Verdict{}, &Rejection{Code: "membership_inactive", Err: domain.ErrMembershipInactive}
	}
	resource := domain.RequestResource(input.RequestID)
	if !holds(input.Grants, domain.PermissionRequestDecide, resource) {
		return Verdict{}, &Rejection{Code: "forbidden", Err: domain.ErrForbidden}
	}
	verdict, rejection := routingVerdict(routingInput{
		Grants: input.Grants, Resource: resource, ReviewerMembershipID: input.ReviewerMembershipID,
		HasRoutingTargets: input.HasRoutingTargets, RoutedUserIDs: input.RoutedUserIDs, RoutedTeamIDs: input.RoutedTeamIDs,
		AddedReviewerIDs: input.AddedReviewerIDs, ReviewerTeamIDs: input.ReviewerTeamIDs,
		UnroutedRequests: input.UnroutedRequests, OutsideRouting: input.OutsideRouting, RequireAcknowledgement: true,
	})
	if rejection != nil {
		return Verdict{}, rejection
	}
	if rejection := RequireFresh(input.Authentication, input.ApprovalReauthWindow, input.MFAMode, input.Now); rejection != nil {
		return Verdict{}, rejection
	}
	return verdict, nil
}

// RequireFresh is the authentication freshness and assurance check that
// decisions and every dangerous permission share: a session older than
// the organization's re-authentication window, or weaker than its MFA
// policy demands, is refused.
func RequireFresh(auth domain.AuthContext, window time.Duration, mfa domain.MFAMode, now time.Time) *Rejection {
	// An API key never signs in, so the re-authentication window does not
	// apply to it: its "sign in again" is expiry and revocation, checked
	// live on every request. The MFA rule still applies, through the
	// assurance the key inherited from the session that minted it.
	if auth.Method != domain.AuthMethodAPIKey && (auth.AuthenticatedAt.IsZero() || now.Sub(auth.AuthenticatedAt) > window) {
		return &Rejection{Code: "authentication_stale", Err: domain.ErrAuthenticationStale}
	}
	if (mfa == domain.MFAEveryoneRequired || mfa == domain.MFAReviewersRequired) && auth.Assurance != "strong" {
		return &Rejection{Code: "authentication_weak", Err: domain.ErrAuthenticationWeak}
	}
	return nil
}

type routingInput struct {
	Grants                 []domain.Grant
	Resource               domain.Resource
	ReviewerMembershipID   domain.ID
	HasRoutingTargets      bool
	RoutedUserIDs          []domain.ID
	RoutedTeamIDs          []domain.ID
	AddedReviewerIDs       []domain.ID
	ReviewerTeamIDs        []domain.ID
	UnroutedRequests       domain.UnroutedRequestsPolicy
	OutsideRouting         bool
	RequireAcknowledgement bool
}

// routingVerdict is the routing rule shared by deciding and claiming.
// Routing is the selector: a routed request admits its targets and the
// people added afterwards; an unrouted request admits any reviewer unless
// the organization sends those to break-glass holders only. A holder of
// request.decide.unrouted may act outside routing, for a decision only
// after acknowledging it.
func routingVerdict(input routingInput) (Verdict, *Rejection) {
	targeted := slices.Contains(input.AddedReviewerIDs, input.ReviewerMembershipID) ||
		routingTargetsReviewer(input.ReviewerMembershipID, input.RoutedUserIDs, input.RoutedTeamIDs, input.ReviewerTeamIDs)
	reason := ""
	switch {
	case input.HasRoutingTargets && !targeted:
		reason = "not_targeted"
	case !input.HasRoutingTargets && input.UnroutedRequests == domain.UnroutedBreakGlassOnly:
		reason = "break_glass_only"
	default:
		return Verdict{}, nil
	}
	if !holds(input.Grants, domain.PermissionRequestDecideUnrouted, input.Resource) {
		return Verdict{}, &Rejection{Code: reason, Err: domain.ErrNotTargeted}
	}
	if input.RequireAcknowledgement && !input.OutsideRouting {
		return Verdict{}, &Rejection{Code: "outside_routing_required", Err: domain.ErrNotTargeted}
	}
	return Verdict{OutsideRouting: true, Reason: reason}, nil
}

// routingTargetsReviewer reports whether the pinned routing snapshot
// targets the reviewer: directly by membership, or through a team the
// reviewer belongs to at evaluation time. Shared by the decision and claim
// kernels.
func routingTargetsReviewer(reviewerMembershipID domain.ID, routedUserIDs, routedTeamIDs, reviewerTeamIDs []domain.ID) bool {
	if slices.Contains(routedUserIDs, reviewerMembershipID) {
		return true
	}
	for _, teamID := range routedTeamIDs {
		if slices.Contains(reviewerTeamIDs, teamID) {
			return true
		}
	}
	return false
}
