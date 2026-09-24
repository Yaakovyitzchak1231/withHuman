package approvals

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
)

func TestReviewNeedsRequestReadAndAHuman(t *testing.T) {
	now := time.Unix(50_000, 0).UTC()
	repository := &approvalRepositoryStub{}
	repository.request = domain.ApprovalRequest{ID: "request-1", OrganizationID: "org-1", AgentID: "agent-1", Status: domain.ApprovalPending}
	service := NewService(repository, repository, defaultPolicy(), &evaluatorStub{}, nil, &lifecycleStub{}, entitlements.NewUnlimited(), approvalClock{now})

	noRead := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1"}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestReadUnrouted}))
	if _, err := service.Review(context.Background(), noRead, "request-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("review without request.read: %v, want forbidden", err)
	}
	agent := domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", OrganizationID: "org-1"}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead}))
	if _, err := service.Review(context.Background(), agent, "request-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("review by the requesting agent: %v, want forbidden", err)
	}
}

// The viewer mirrors the decision kernel's routing rule, so the detail
// page's decide affordance and the decision's answer never disagree.
func TestViewerMirrorsTheRoutingRule(t *testing.T) {
	pending := domain.ApprovalRequest{ID: "request-1", Status: domain.ApprovalPending}
	decided := domain.ApprovalRequest{ID: "request-1", Status: domain.ApprovalApproved}
	anyReviewer := domain.OrganizationPolicy{UnroutedRequests: domain.UnroutedAnyReviewer}
	breakGlassOnly := domain.OrganizationPolicy{UnroutedRequests: domain.UnroutedBreakGlassOnly}
	grants := func(permissions ...domain.Permission) domain.Principal {
		return domain.Principal{ActorType: domain.ActorHuman, OrganizationID: "org-1", MembershipID: "membership-1"}.WithGrants(domain.OrganizationGrants(permissions))
	}
	reader := grants(domain.PermissionRequestRead)
	reviewer := grants(domain.PermissionRequestRead, domain.PermissionRequestDecide)
	breakGlass := grants(domain.PermissionRequestRead, domain.PermissionRequestDecide, domain.PermissionRequestDecideUnrouted)
	targeted := application.ApprovalRoutingState{HasTargets: true, Targeted: true}
	elsewhere := application.ApprovalRoutingState{HasTargets: true}
	unrouted := application.ApprovalRoutingState{}

	for name, test := range map[string]struct {
		principal domain.Principal
		request   domain.ApprovalRequest
		state     application.ApprovalRoutingState
		policy    domain.OrganizationPolicy
		want      Viewer
	}{
		"decided request is not decidable":     {reviewer, decided, targeted, anyReviewer, Viewer{Targeted: true, Decidable: DecidableNo}},
		"reader lacks the permission":          {reader, pending, targeted, anyReviewer, Viewer{Targeted: true, Decidable: DecidableNo, Reason: ViewerReasonNoPermission}},
		"targeted reviewer decides":            {reviewer, pending, targeted, anyReviewer, Viewer{Targeted: true, Decidable: DecidableYes}},
		"routed elsewhere without break glass": {reviewer, pending, elsewhere, anyReviewer, Viewer{Decidable: DecidableNo, Reason: ViewerReasonNotTargeted}},
		"routed elsewhere with break glass":    {breakGlass, pending, elsewhere, anyReviewer, Viewer{Decidable: DecidableBreakGlass, Reason: ViewerReasonNotTargeted}},
		"unrouted under any_reviewer":          {reviewer, pending, unrouted, anyReviewer, Viewer{Decidable: DecidableYes}},
		"unrouted under break_glass_only":      {reviewer, pending, unrouted, breakGlassOnly, Viewer{Decidable: DecidableNo, Reason: ViewerReasonBreakGlassOnly}},
		"unrouted break glass holder":          {breakGlass, pending, unrouted, breakGlassOnly, Viewer{Decidable: DecidableBreakGlass, Reason: ViewerReasonBreakGlassOnly}},
		"targeted ignores the unrouted policy": {reviewer, pending, targeted, breakGlassOnly, Viewer{Targeted: true, Decidable: DecidableYes}},
		"added reviewer counts as targeted":    {reviewer, pending, application.ApprovalRoutingState{HasTargets: true, Targeted: true, AddedReviewers: []domain.ApprovalReviewer{{MembershipID: "membership-1"}}}, anyReviewer, Viewer{Targeted: true, Decidable: DecidableYes}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := viewerFor(test.principal, test.request, test.state, test.policy); got != test.want {
				t.Fatalf("viewer = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestReviewCarriesTheViewerAndAddedReviewers(t *testing.T) {
	now := time.Unix(50_000, 0).UTC()
	repository := &approvalRepositoryStub{}
	repository.request = domain.ApprovalRequest{ID: "request-1", OrganizationID: "org-1", AgentID: "agent-1", Status: domain.ApprovalPending}
	repository.routingState = application.ApprovalRoutingState{HasTargets: true, AddedReviewers: []domain.ApprovalReviewer{
		{RequestID: "request-1", MembershipID: "membership-2", DisplayName: "Ben", AddedByMembershipID: "membership-3", AddedByDisplayName: "Cy", CreatedAt: now},
	}}
	policy := defaultPolicy()
	service := NewService(repository, repository, policy, &evaluatorStub{}, nil, &lifecycleStub{}, entitlements.NewUnlimited(), approvalClock{now})
	reviewer := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead, domain.PermissionRequestDecide}))

	review, err := service.Review(context.Background(), reviewer, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if review.Viewer != (Viewer{Decidable: DecidableNo, Reason: ViewerReasonNotTargeted}) {
		t.Fatalf("viewer = %#v", review.Viewer)
	}
	if len(review.AddedReviewers) != 1 || review.AddedReviewers[0].DisplayName != "Ben" {
		t.Fatalf("added reviewers = %#v", review.AddedReviewers)
	}
	if policy.reads != 1 {
		t.Fatalf("policy read %d times, want once per call", policy.reads)
	}

	// A request without routing rows reads as unrouted with nobody added,
	// never as an error.
	repository.routingState = application.ApprovalRoutingState{}
	repository.routingErr = domain.ErrNotFound
	review, err = service.Review(context.Background(), reviewer, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if review.Viewer.Decidable != DecidableYes || review.AddedReviewers == nil || len(review.AddedReviewers) != 0 {
		t.Fatalf("unrouted review = %#v", review)
	}
}
