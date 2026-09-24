package approvals

import (
	"context"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

// Review is the reader's standing on one request: who has been pulled
// into its routing since it was pinned, and whether a decision from the
// reader would pass the routing rule. It is read with request.read
// alone. What the pipeline concluded and what happened to the request
// since is the audit log's story, which an edition that keeps one tells
// from its own events.
type Review struct {
	// AddedReviewers are the people pulled into the request's routing
	// after it was pinned, oldest first.
	AddedReviewers []domain.ApprovalReviewer
	// Viewer is the reading principal's own standing on the request: the
	// decide affordance the detail view shows.
	Viewer Viewer
}

// Viewer is how the request relates to the person reading it. Targeted
// says whether routing (or a later widening) names them; Decidable says
// whether a decision from them would pass the routing rule, and Reason
// says which rule stands in the way when it would not pass outright.
type Viewer struct {
	Targeted bool
	// Decidable is yes, break_glass (a decision passes only with the
	// outside-routing acknowledgement), or no.
	Decidable string
	// Reason is not_targeted, break_glass_only, or no_permission; empty
	// when Decidable is yes or the request is no longer pending.
	Reason string
}

const (
	DecidableYes        = "yes"
	DecidableBreakGlass = "break_glass"
	DecidableNo         = "no"

	ViewerReasonNotTargeted    = "not_targeted"
	ViewerReasonBreakGlassOnly = "break_glass_only"
	ViewerReasonNoPermission   = "no_permission"
)

// Review assembles the reader's standing on one request. The shared read
// performs the authorization, organization scoping, and visibility
// narrowing; the viewer is then computed from the routing state and the
// organization policy, the same inputs the decision kernel reads.
func (s *Service) Review(ctx context.Context, principal domain.Principal, requestID domain.ID) (Review, error) {
	if !domain.ManagementPrincipal(principal) {
		return Review{}, domain.ErrForbidden
	}
	policy, err := s.policyFor(ctx, principal)
	if err != nil {
		return Review{}, err
	}
	request, err := s.get(ctx, principal, requestID, visibleTo(principal, policy))
	if err != nil {
		return Review{}, err
	}
	state, err := s.routingState(ctx, principal, requestID)
	if err != nil {
		return Review{}, err
	}
	return Review{AddedReviewers: state.AddedReviewers, Viewer: viewerFor(principal, request, state, policy)}, nil
}

// viewerFor mirrors the routing rule of the decision kernel for display:
// the same inputs, the same order, so the affordance the page shows and
// the answer the decision gets never disagree. It stops short of the
// checks only the transaction can make (membership status, authentication
// freshness, self-approval), which the decision still applies.
func viewerFor(principal domain.Principal, request domain.ApprovalRequest, state application.ApprovalRoutingState, policy domain.OrganizationPolicy) Viewer {
	viewer := Viewer{Targeted: state.Targeted}
	if request.Status != domain.ApprovalPending {
		viewer.Decidable = DecidableNo
		return viewer
	}
	resource := domain.RequestResource(request.ID)
	if authorization.Check(principal, domain.PermissionRequestDecide, resource) != nil {
		viewer.Decidable, viewer.Reason = DecidableNo, ViewerReasonNoPermission
		return viewer
	}
	switch {
	case state.HasTargets && !state.Targeted:
		viewer.Reason = ViewerReasonNotTargeted
	case !state.HasTargets && policy.UnroutedRequests == domain.UnroutedBreakGlassOnly:
		viewer.Reason = ViewerReasonBreakGlassOnly
	default:
		viewer.Decidable = DecidableYes
		return viewer
	}
	if authorization.Check(principal, domain.PermissionRequestDecideUnrouted, resource) == nil {
		viewer.Decidable = DecidableBreakGlass
	} else {
		viewer.Decidable = DecidableNo
	}
	return viewer
}
