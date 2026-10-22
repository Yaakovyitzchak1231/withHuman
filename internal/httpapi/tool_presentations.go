package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/toolpresentations"
	"github.com/withHumanAI/withHuman/toolpresentation"
)

// Tool presentations are organization settings: the list reads with
// organization.read, like the policy, and every write needs
// organization.settings.write. The shipped catalog travels with the list
// so the page can show what an organization entry overrides.
func (a *API) installToolPresentationRoutes(router *gin.RouterGroup) {
	read, write := string(domain.PermissionOrganizationRead), string(domain.PermissionOrganizationSettingsWrite)
	a.handle(router, http.MethodGet, "/tool_presentations", read, a.listToolPresentations)
	a.handle(router, http.MethodPost, "/tool_presentations", write, a.createToolPresentation)
	a.handle(router, http.MethodPut, "/tool_presentations/:id", write, a.updateToolPresentation)
	a.handle(router, http.MethodDelete, "/tool_presentations/:id", write, a.deleteToolPresentation)
}

// presentationResponse is what a request carries: the resolved
// specification and its source, so a page can offer to edit the
// organization's entry or override the shipped one.
type presentationResponse struct {
	Source toolpresentations.Source `json:"source"`
	ID     domain.ID                `json:"id,omitempty"`
	Title  string                   `json:"title,omitempty"`
	Fields []toolpresentation.Field `json:"fields"`
}

func newPresentationResponse(presentation toolpresentations.Presentation) *presentationResponse {
	return &presentationResponse{Source: presentation.Source, ID: presentation.ID, Title: presentation.Spec.Title, Fields: presentation.Spec.Fields}
}

// present attaches presentations and the edition's summaries to product
// request responses. A nil service (tests that exercise other routes)
// presents nothing.
func (a *API) present(c *gin.Context, orgID domain.ID, responses []queueApprovalResponse, requests []domain.ApprovalRequest) []queueApprovalResponse {
	if a.toolPresentations != nil {
		presenter := a.toolPresentations.Presenter(c.Request.Context(), orgID)
		for index := range responses {
			if presentation, ok := presenter(requests[index]); ok {
				responses[index].Presentation = newPresentationResponse(presentation)
			}
		}
	}
	a.summarize(c, orgID, responses, requests)
	return responses
}

// summarize attaches the summaries the edition stored. A failed read
// leaves them out: the presentation title still names every request.
func (a *API) summarize(c *gin.Context, orgID domain.ID, responses []queueApprovalResponse, requests []domain.ApprovalRequest) {
	if a.requestSummaries == nil || len(requests) == 0 {
		return
	}
	ids := make([]domain.ID, len(requests))
	for index, request := range requests {
		ids[index] = request.ID
	}
	summaries, err := a.requestSummaries.RequestSummaries(c.Request.Context(), application.ReadRequestSummariesInput{OrganizationID: orgID, RequestIDs: ids})
	if err != nil {
		a.logger.WarnContext(c.Request.Context(), "request summaries unavailable", "error", err)
		return
	}
	for index, request := range requests {
		if summary, ok := summaries[request.ID]; ok {
			responses[index].Summary = &requestSummaryResponse{Text: summary.Text, GeneratedAt: summary.GeneratedAt}
		}
	}
}

func (a *API) presentOne(c *gin.Context, request domain.ApprovalRequest, now time.Time) queueApprovalResponse {
	responses := a.present(c, request.OrganizationID, []queueApprovalResponse{newQueueApprovalResponse(request, now)}, []domain.ApprovalRequest{request})
	return responses[0]
}

func (a *API) presentMany(c *gin.Context, requests []domain.ApprovalRequest, now time.Time) []queueApprovalResponse {
	responses := newQueueApprovalResponses(requests, now)
	if len(requests) == 0 {
		return responses
	}
	return a.present(c, currentPrincipal(c).OrganizationID, responses, requests)
}

type toolPresentationResponse struct {
	ID        domain.ID             `json:"id"`
	Runtime   string                `json:"runtime,omitempty"`
	Server    string                `json:"server,omitempty"`
	Tool      string                `json:"tool"`
	Spec      toolpresentation.Spec `json:"spec"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

func newToolPresentationResponse(record domain.ToolPresentation) toolPresentationResponse {
	return toolPresentationResponse{
		ID: record.ID, Runtime: record.Entry.Runtime, Server: record.Entry.Server, Tool: record.Entry.Tool,
		Spec: record.Entry.Spec.Normalize(), CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

type toolPresentationRequest struct {
	Runtime string                `json:"runtime"`
	Server  string                `json:"server"`
	Tool    string                `json:"tool"`
	Spec    toolpresentation.Spec `json:"spec"`
}

func (request toolPresentationRequest) entry() toolpresentation.Entry {
	return toolpresentation.Entry{Runtime: request.Runtime, Server: request.Server, Tool: request.Tool, Spec: request.Spec}
}

func (a *API) listToolPresentations(c *gin.Context) {
	records, err := a.toolPresentations.List(c.Request.Context(), currentPrincipal(c))
	if err != nil {
		a.writeError(c, err)
		return
	}
	responses := make([]toolPresentationResponse, len(records))
	for index, record := range records {
		responses[index] = newToolPresentationResponse(record)
	}
	builtin := toolpresentation.Builtin()
	for index := range builtin {
		builtin[index].Spec = builtin[index].Spec.Normalize()
	}
	c.JSON(http.StatusOK, gin.H{"presentations": responses, "built_in": builtin, "kinds": toolpresentation.Kinds})
}

func (a *API) createToolPresentation(c *gin.Context) {
	var request toolPresentationRequest
	if !bindStrict(c, &request) {
		return
	}
	record, err := a.toolPresentations.Create(c.Request.Context(), currentPrincipal(c), request.entry())
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newToolPresentationResponse(record))
}

func (a *API) updateToolPresentation(c *gin.Context) {
	var request toolPresentationRequest
	if !bindStrict(c, &request) {
		return
	}
	record, err := a.toolPresentations.Update(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")), request.entry())
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, newToolPresentationResponse(record))
}

func (a *API) deleteToolPresentation(c *gin.Context) {
	if err := a.toolPresentations.Delete(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id"))); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
