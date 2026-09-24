package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvals"
)

// approvalReviewResponse is the product-only standing of one request:
// where it was routed, who was added since, and how the reader relates
// to it. AAP never sees it. An edition that keeps an audit log tells the
// rest of the request's story from its own events.
type approvalReviewResponse struct {
	Routing        *approvalRoutingResponse   `json:"routing,omitempty"`
	AddedReviewers []approvalReviewerResponse `json:"added_reviewers"`
	Viewer         reviewViewerResponse       `json:"viewer"`
}

// reviewViewerResponse is the reader's own standing: whether routing
// names them and whether a decision from them would pass the routing
// rule outright, only through break glass, or not at all.
type reviewViewerResponse struct {
	Targeted  bool   `json:"targeted"`
	Decidable string `json:"decidable"`
	Reason    string `json:"reason,omitempty"`
}

type approvalReviewerResponse struct {
	MembershipID        domain.ID `json:"membership_id"`
	DisplayName         string    `json:"display_name"`
	AddedByMembershipID domain.ID `json:"added_by_membership_id"`
	AddedByDisplayName  string    `json:"added_by_display_name"`
	CreatedAt           time.Time `json:"created_at"`
}

func newApprovalReviewerResponses(reviewers []domain.ApprovalReviewer) []approvalReviewerResponse {
	responses := make([]approvalReviewerResponse, 0, len(reviewers))
	for _, reviewer := range reviewers {
		responses = append(responses, approvalReviewerResponse{
			MembershipID: reviewer.MembershipID, DisplayName: reviewer.DisplayName,
			AddedByMembershipID: reviewer.AddedByMembershipID, AddedByDisplayName: reviewer.AddedByDisplayName,
			CreatedAt: reviewer.CreatedAt,
		})
	}
	return responses
}

func newApprovalReviewResponse(review approvals.Review, routing *application.ApprovalRoutingView) approvalReviewResponse {
	response := approvalReviewResponse{
		AddedReviewers: newApprovalReviewerResponses(review.AddedReviewers),
		Viewer:         reviewViewerResponse{Targeted: review.Viewer.Targeted, Decidable: review.Viewer.Decidable, Reason: review.Viewer.Reason},
	}
	if routing != nil {
		snapshot := newApprovalRoutingResponse(*routing, review.AddedReviewers)
		response.Routing = &snapshot
	}
	return response
}

// getApprovalReview is the reader's standing on one request: its
// routing, the reviewers added since, and whether the reader may decide,
// gated by request.read alone. Requests the pipeline decided
// automatically have no routing snapshot, which is simply absent.
func (a *API) getApprovalReview(c *gin.Context) {
	a.writeApprovalReview(c, domain.ID(c.Param("id")))
}

// writeApprovalReview assembles and writes the review for one request,
// shared by the review read and the reviewer widening, which answers
// with the review it changed.
func (a *API) writeApprovalReview(c *gin.Context, requestID domain.ID) {
	review, err := a.approvals.Review(c.Request.Context(), currentPrincipal(c), requestID)
	if err != nil {
		a.writeError(c, err)
		return
	}
	var routing *application.ApprovalRoutingView
	if a.routingSnapshots != nil {
		snapshot, err := a.routingSnapshots.GetApprovalRouting(c.Request.Context(), application.GetApprovalRoutingInput{
			OrganizationID: currentPrincipal(c).OrganizationID,
			RequestID:      requestID,
		})
		switch {
		case err == nil:
			routing = &snapshot
		case errors.Is(err, domain.ErrNotFound):
		default:
			a.writeError(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, newApprovalReviewResponse(review, routing))
}

type addApprovalReviewerRequest struct {
	MembershipID domain.ID `json:"membership_id" binding:"required"`
}

// addApprovalReviewer is the handoff: a targeted reviewer (or a break-glass
// holder) pulls a colleague into the request's routing. The request's
// review is the answer, so the page re-renders from one round trip.
type reviewerCandidateResponse struct {
	MembershipID domain.ID `json:"membership_id"`
	DisplayName  string    `json:"display_name"`
	Email        string    `json:"email"`
}

func (a *API) listReviewerCandidates(c *gin.Context) {
	requestID := domain.ID(c.Param("id"))
	setApprovalRequest(c, requestID)
	candidates, err := a.approvals.ReviewerCandidates(c.Request.Context(), currentPrincipal(c), requestID)
	if err != nil {
		a.writeError(c, err)
		return
	}
	responses := make([]reviewerCandidateResponse, 0, len(candidates))
	for _, candidate := range candidates {
		responses = append(responses, reviewerCandidateResponse{MembershipID: candidate.MembershipID, DisplayName: candidate.DisplayName, Email: candidate.Email})
	}
	c.JSON(http.StatusOK, gin.H{"candidates": responses})
}

func (a *API) addApprovalReviewer(c *gin.Context) {
	requestID := domain.ID(c.Param("id"))
	setApprovalRequest(c, requestID)
	var request addApprovalReviewerRequest
	if !bindStrict(c, &request) {
		return
	}
	if _, err := a.approvals.AddReviewer(c.Request.Context(), currentPrincipal(c), requestID, request.MembershipID); err != nil {
		a.writeError(c, err)
		return
	}
	a.writeApprovalReview(c, requestID)
}
