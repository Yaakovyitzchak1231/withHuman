package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/teamescalation"
	"github.com/withHumanAI/withHuman/routing"
)

// installTeamEscalationRoutes mounts the team escalation policy control
// plane under the team it belongs to. Reads ride on team.read; writes
// and activation carry their own team-scoped keys.
func (a *API) installTeamEscalationRoutes(router *gin.RouterGroup) {
	read, write, activate := string(domain.PermissionTeamRead), string(domain.PermissionTeamEscalationWrite), string(domain.PermissionTeamEscalationActivate)
	a.handle(router, http.MethodGet, "/teams/:id/escalation-policy", read, a.getActiveTeamEscalationPolicy)
	a.handle(router, http.MethodGet, "/teams/:id/escalation-policy/revisions", read, a.listTeamEscalationPolicyRevisions)
	a.handle(router, http.MethodPost, "/teams/:id/escalation-policy/revisions", write, a.createTeamEscalationPolicyRevision)
	a.handle(router, http.MethodGet, "/teams/:id/escalation-policy/revisions/:revision", read, a.getTeamEscalationPolicyRevision)
	a.handle(router, http.MethodPut, "/teams/:id/escalation-policy/revisions/:revision", write, a.updateTeamEscalationPolicyRevision)
	a.handle(router, http.MethodPost, "/teams/:id/escalation-policy/revisions/:revision/activate", activate, a.activateTeamEscalationPolicyRevision)
	a.handle(router, http.MethodDelete, "/teams/:id/escalation-policy/active", activate, a.archiveTeamEscalationPolicy)
}

// teamEscalationPolicyDocumentRequest is the policy body. Unlike a path
// it carries no name: the team is the name.
type teamEscalationPolicyDocumentRequest struct {
	WorkingHours []routing.WorkingHoursSet `json:"working_hours"`
	Nodes        []routing.Node            `json:"nodes"`
	Repeat       *routing.Repeat           `json:"repeat,omitempty"`
}

func (request teamEscalationPolicyDocumentRequest) document() routing.TeamPolicyDocument {
	return routing.TeamPolicyDocument{
		WorkingHours: request.WorkingHours,
		Nodes:        request.Nodes,
		Repeat:       request.Repeat,
	}
}

type teamEscalationPolicyRevisionSummaryResponse struct {
	ID               domain.ID  `json:"id"`
	TeamID           domain.ID  `json:"team_id"`
	Revision         int64      `json:"revision"`
	IsActive         bool       `json:"is_active"`
	CreatedByActorID domain.ID  `json:"created_by_actor_id"`
	CreatedAt        time.Time  `json:"created_at"`
	ActivatedAt      *time.Time `json:"activated_at,omitempty"`
}

type teamEscalationPolicyRevisionResponse struct {
	teamEscalationPolicyRevisionSummaryResponse
	WorkingHours    []routing.WorkingHoursSet             `json:"working_hours"`
	Nodes           []routing.Node                        `json:"nodes"`
	Repeat          *routing.Repeat                       `json:"repeat,omitempty"`
	DocumentVersion int                                   `json:"document_version"`
	Warnings        []teamEscalationPolicyWarningResponse `json:"warnings"`
}

// teamEscalationPolicyWarningResponse is one advisory finding: a target
// who left the team (membership_id, display_name when still known) or
// how many members cannot decide (count of member_count).
type teamEscalationPolicyWarningResponse struct {
	Code         string    `json:"code"`
	MembershipID domain.ID `json:"membership_id,omitempty"`
	DisplayName  string    `json:"display_name,omitempty"`
	Count        int       `json:"count"`
	MemberCount  int       `json:"member_count"`
}

// teamEscalationPolicyReferenceResponse is one active team policy that
// names a member, as carried by a refused suspension or removal.
type teamEscalationPolicyReferenceResponse struct {
	TeamID   domain.ID `json:"team_id"`
	TeamName string    `json:"team_name"`
	Revision int64     `json:"revision"`
}

func newTeamEscalationPolicyReferenceResponses(policies []domain.TeamEscalationPolicyReference) []teamEscalationPolicyReferenceResponse {
	responses := make([]teamEscalationPolicyReferenceResponse, 0, len(policies))
	for _, policy := range policies {
		responses = append(responses, teamEscalationPolicyReferenceResponse{
			TeamID: policy.TeamID, TeamName: policy.TeamName, Revision: policy.Revision,
		})
	}
	return responses
}

// idsOrEmpty keeps a list of ids an array on the wire when it is empty.
func idsOrEmpty(ids []domain.ID) []domain.ID {
	if ids == nil {
		return []domain.ID{}
	}
	return ids
}

func newTeamEscalationPolicyWarningResponses(warnings []teamescalation.Warning) []teamEscalationPolicyWarningResponse {
	responses := make([]teamEscalationPolicyWarningResponse, len(warnings))
	for index, warning := range warnings {
		responses[index] = teamEscalationPolicyWarningResponse{
			Code: warning.Code, MembershipID: warning.MembershipID, DisplayName: warning.DisplayName,
			Count: warning.Count, MemberCount: warning.MemberCount,
		}
	}
	return responses
}

func newTeamEscalationPolicyRevisionSummaryResponse(revision domain.TeamEscalationPolicyRevision) teamEscalationPolicyRevisionSummaryResponse {
	return teamEscalationPolicyRevisionSummaryResponse{
		ID: revision.ID, TeamID: revision.TeamID, Revision: revision.Revision, IsActive: revision.IsActive,
		CreatedByActorID: revision.CreatedByActorID, CreatedAt: revision.CreatedAt,
		ActivatedAt: revision.ActivatedAt,
	}
}

func newTeamEscalationPolicyRevisionSummaryResponses(revisions []domain.TeamEscalationPolicyRevision) []teamEscalationPolicyRevisionSummaryResponse {
	responses := make([]teamEscalationPolicyRevisionSummaryResponse, len(revisions))
	for index, revision := range revisions {
		responses[index] = newTeamEscalationPolicyRevisionSummaryResponse(revision)
	}
	return responses
}

func newTeamEscalationPolicyRevisionResponse(revision teamescalation.PolicyRevision) teamEscalationPolicyRevisionResponse {
	return teamEscalationPolicyRevisionResponse{
		teamEscalationPolicyRevisionSummaryResponse: newTeamEscalationPolicyRevisionSummaryResponse(revision.Revision),
		WorkingHours:    revision.Document.WorkingHours,
		Nodes:           revision.Document.Nodes,
		Repeat:          revision.Document.Repeat,
		DocumentVersion: revision.Revision.DocumentVersion,
		Warnings:        newTeamEscalationPolicyWarningResponses(revision.Warnings),
	}
}

func (a *API) getActiveTeamEscalationPolicy(c *gin.Context) {
	revision, err := a.teamEscalation.GetActivePolicy(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")))
	if err != nil {
		a.writeError(c, err)
		return
	}
	setActiveRevisionETag(c, revision.Revision.Revision)
	c.JSON(http.StatusOK, newTeamEscalationPolicyRevisionResponse(revision))
}

func (a *API) listTeamEscalationPolicyRevisions(c *gin.Context) {
	revisions, err := a.teamEscalation.ListRevisions(c.Request.Context(), currentPrincipal(c),
		domain.ID(c.Param("id")), queryInt(c, "limit", 50))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"revisions": newTeamEscalationPolicyRevisionSummaryResponses(revisions)})
}

func (a *API) createTeamEscalationPolicyRevision(c *gin.Context) {
	var request teamEscalationPolicyDocumentRequest
	if !bindStrict(c, &request) {
		return
	}
	if request.Nodes == nil {
		a.writeError(c, fmt.Errorf("nodes are required: %w", domain.ErrInvalid))
		return
	}
	revision, err := a.teamEscalation.CreateRevision(c.Request.Context(), currentPrincipal(c),
		domain.ID(c.Param("id")), request.document())
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newTeamEscalationPolicyRevisionResponse(revision))
}

// updateTeamEscalationPolicyRevision replaces a draft revision's document
// in place with the same body a new revision takes.
func (a *API) updateTeamEscalationPolicyRevision(c *gin.Context) {
	revisionNumber, err := parsePipelineRevision(c.Param("revision"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	var request teamEscalationPolicyDocumentRequest
	if !bindStrict(c, &request) {
		return
	}
	if request.Nodes == nil {
		a.writeError(c, fmt.Errorf("nodes are required: %w", domain.ErrInvalid))
		return
	}
	revision, err := a.teamEscalation.UpdateRevision(c.Request.Context(), currentPrincipal(c),
		domain.ID(c.Param("id")), revisionNumber, request.document())
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, newTeamEscalationPolicyRevisionResponse(revision))
}

func (a *API) getTeamEscalationPolicyRevision(c *gin.Context) {
	revisionNumber, err := parsePipelineRevision(c.Param("revision"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	revision, err := a.teamEscalation.GetRevision(c.Request.Context(), currentPrincipal(c),
		domain.ID(c.Param("id")), revisionNumber)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, newTeamEscalationPolicyRevisionResponse(revision))
}

func (a *API) activateTeamEscalationPolicyRevision(c *gin.Context) {
	revisionNumber, err := parsePipelineRevision(c.Param("revision"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	expected, err := parseActiveRevisionIfMatch(c.GetHeader("If-Match"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	revision, err := a.teamEscalation.ActivateRevision(c.Request.Context(), currentPrincipal(c),
		domain.ID(c.Param("id")), revisionNumber, expected)
	if err != nil {
		a.writeError(c, err)
		return
	}
	setActiveRevisionETag(c, revision.Revision.Revision)
	c.JSON(http.StatusOK, newTeamEscalationPolicyRevisionResponse(revision))
}

func (a *API) archiveTeamEscalationPolicy(c *gin.Context) {
	expected, err := parseRequiredActiveRevisionIfMatch(c.GetHeader("If-Match"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	if err := a.teamEscalation.Deactivate(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")), expected); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
