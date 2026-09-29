package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/reviewrouting"
	"github.com/withHumanAI/withHuman/routing"
)

// routingSnapshotReader reads the routing snapshot pinned when a request
// entered human review.
type routingSnapshotReader interface {
	GetApprovalRouting(context.Context, application.GetApprovalRoutingInput) (application.ApprovalRoutingView, error)
}

func (a *API) installRoutingRoutes(router *gin.RouterGroup) {
	read, write, activate := string(domain.PermissionEscalationPathRead), string(domain.PermissionEscalationPathWrite), string(domain.PermissionEscalationPathActivate)
	a.handle(router, http.MethodGet, "/escalation-paths", read, a.listEscalationPaths)
	a.handle(router, http.MethodPost, "/escalation-paths", write, a.createEscalationPath)
	a.handle(router, http.MethodGet, "/escalation-paths/:path_key", read, a.getActiveEscalationPath)
	a.handle(router, http.MethodDelete, "/escalation-paths/:path_key", activate, a.archiveEscalationPath)
	a.handle(router, http.MethodPost, "/escalation-paths/:path_key/revisions", write, a.createEscalationPathRevision)
	a.handle(router, http.MethodGet, "/escalation-paths/:path_key/revisions", read, a.listEscalationPathRevisions)
	a.handle(router, http.MethodGet, "/escalation-paths/:path_key/revisions/:revision", read, a.getEscalationPathRevision)
	a.handle(router, http.MethodPost, "/escalation-paths/:path_key/revisions/:revision/activate", activate, a.activateEscalationPathRevision)
	a.handle(router, http.MethodGet, "/escalation-paths/:path_key/uses", read, a.listEscalationPathUses)
}

type escalationPathDocumentRequest struct {
	Name         string                    `json:"name"`
	WorkingHours []routing.WorkingHoursSet `json:"working_hours"`
	Nodes        []routing.Node            `json:"nodes"`
	Repeat       *routing.Repeat           `json:"repeat,omitempty"`
}

// escalationPathCreateRequest is a document plus the key it creates.
type escalationPathCreateRequest struct {
	PathKey string `json:"path_key"`
	escalationPathDocumentRequest
}

func (request escalationPathDocumentRequest) document() routing.PathDocument {
	return routing.PathDocument{
		WorkingHours: request.WorkingHours,
		Nodes:        request.Nodes,
		Repeat:       request.Repeat,
	}
}

type escalationPathSummaryResponse struct {
	PathKey         string     `json:"path_key"`
	Name            string     `json:"name"`
	ActiveRevision  *int64     `json:"active_revision,omitempty"`
	LatestRevision  int64      `json:"latest_revision"`
	RevisionCount   int64      `json:"revision_count"`
	LatestCreatedAt time.Time  `json:"latest_created_at"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
}

type escalationPathRevisionSummaryResponse struct {
	ID               domain.ID  `json:"id"`
	PathKey          string     `json:"path_key"`
	Revision         int64      `json:"revision"`
	IsActive         bool       `json:"is_active"`
	Name             string     `json:"name"`
	CreatedByActorID domain.ID  `json:"created_by_actor_id"`
	CreatedAt        time.Time  `json:"created_at"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty"`
}

type escalationPathRevisionResponse struct {
	escalationPathRevisionSummaryResponse
	WorkingHours    []routing.WorkingHoursSet       `json:"working_hours"`
	Nodes           []routing.Node                  `json:"nodes"`
	Repeat          *routing.Repeat                 `json:"repeat,omitempty"`
	DocumentVersion int                             `json:"document_version"`
	Warnings        []escalationPathWarningResponse `json:"warnings"`
}

// escalationPathWarningResponse is one advisory finding about the
// document: members of a targeted team who cannot decide requests, or a
// branch layout some request walks without meeting a level. The team
// fields are present only for findings about a team.
type escalationPathWarningResponse struct {
	Code        string    `json:"code"`
	TeamID      domain.ID `json:"team_id,omitempty"`
	TeamName    string    `json:"team_name,omitempty"`
	Count       int       `json:"count,omitempty"`
	MemberCount int       `json:"member_count,omitempty"`
}

func newEscalationPathWarningResponses(warnings []reviewrouting.PathWarning) []escalationPathWarningResponse {
	responses := make([]escalationPathWarningResponse, len(warnings))
	for index, warning := range warnings {
		responses[index] = escalationPathWarningResponse{
			Code: warning.Code, TeamID: warning.TeamID, TeamName: warning.TeamName,
			Count: warning.Count, MemberCount: warning.MemberCount,
		}
	}
	return responses
}

func newEscalationPathSummaryResponses(summaries []domain.EscalationPathSummary) []escalationPathSummaryResponse {
	responses := make([]escalationPathSummaryResponse, len(summaries))
	for index, summary := range summaries {
		responses[index] = escalationPathSummaryResponse{
			PathKey: summary.PathKey, Name: summary.Name, ActiveRevision: summary.ActiveRevision,
			LatestRevision: summary.LatestRevision, RevisionCount: summary.RevisionCount,
			LatestCreatedAt: summary.LatestCreatedAt, ArchivedAt: summary.ArchivedAt,
		}
	}
	return responses
}

func newEscalationPathRevisionSummaryResponse(revision domain.EscalationPathRevision) escalationPathRevisionSummaryResponse {
	return escalationPathRevisionSummaryResponse{
		ID: revision.ID, PathKey: revision.PathKey, Revision: revision.Revision,
		IsActive: revision.IsActive, Name: revision.Name,
		CreatedByActorID: revision.CreatedByActorID, CreatedAt: revision.CreatedAt,
		ArchivedAt: revision.ArchivedAt,
	}
}

func newEscalationPathRevisionSummaryResponses(revisions []domain.EscalationPathRevision) []escalationPathRevisionSummaryResponse {
	responses := make([]escalationPathRevisionSummaryResponse, len(revisions))
	for index, revision := range revisions {
		responses[index] = newEscalationPathRevisionSummaryResponse(revision)
	}
	return responses
}

func newEscalationPathRevisionResponse(revision reviewrouting.PathRevision) escalationPathRevisionResponse {
	return escalationPathRevisionResponse{
		escalationPathRevisionSummaryResponse: newEscalationPathRevisionSummaryResponse(revision.Revision),
		WorkingHours:                          revision.Document.WorkingHours,
		Nodes:                                 revision.Document.Nodes,
		Repeat:                                revision.Document.Repeat,
		DocumentVersion:                       revision.Revision.DocumentVersion,
		Warnings:                              newEscalationPathWarningResponses(revision.Warnings),
	}
}

type approvalRoutingResponse struct {
	Outcome                  domain.RoutingOutcome `json:"outcome"`
	Reason                   string                `json:"reason"`
	Urgency                  string                `json:"urgency"`
	EscalationPathKey        string                `json:"escalation_path_key,omitempty"`
	EscalationPathName       string                `json:"escalation_path_name,omitempty"`
	EscalationPathRevisionID domain.ID             `json:"escalation_path_revision_id,omitempty"`
	PipelineRevisionID       domain.ID             `json:"pipeline_revision_id,omitempty"`
	BlockID                  domain.ID             `json:"block_id,omitempty"`
	// AgentOwner is the person an agent_owner snapshot sent the request
	// to; AgentOwnerFallback says why an agent-owner route went to its
	// fallback at routing time, and AgentOwnerHandedOffAt when the owner
	// was deactivated and the fallback took over.
	AgentOwner            *approvalRoutingTargetResponse `json:"agent_owner,omitempty"`
	AgentOwnerFallback    string                         `json:"agent_owner_fallback,omitempty"`
	AgentOwnerHandedOffAt *time.Time                     `json:"agent_owner_handed_off_at,omitempty"`
	// Targets is who current routing admits, in path order: the agent's
	// owner until a handoff, otherwise who the pinned path reaches. Only
	// they and the added reviewers may claim or decide.
	// Empty for a default queue snapshot. Empty for an escalated one
	// means the path reaches nobody and only a break-glass reviewer may
	// decide.
	Targets        []approvalRoutingTargetResponse `json:"targets"`
	AddedReviewers []approvalReviewerResponse      `json:"added_reviewers"`
	// TeamPolicies pins the team policy revisions the request runs; empty
	// for a default queue snapshot or a path without policy-bearing teams.
	TeamPolicies []pinnedTeamPolicyResponse `json:"team_policies"`
	// Escalation is the executor's position, null before its first tick
	// and for a default queue snapshot.
	Escalation *escalationProgressResponse `json:"escalation"`
	CreatedAt  time.Time                   `json:"created_at"`
}

// approvalRoutingTargetResponse is one person (a membership) or team the
// pinned path reaches for the request.
type approvalRoutingTargetResponse struct {
	Type string    `json:"type"`
	ID   domain.ID `json:"id"`
	Name string    `json:"name"`
}

func newApprovalRoutingTargetResponses(targets []application.ApprovalRoutingTarget) []approvalRoutingTargetResponse {
	responses := make([]approvalRoutingTargetResponse, 0, len(targets))
	for _, target := range targets {
		responses = append(responses, approvalRoutingTargetResponse{Type: target.Type, ID: target.ID, Name: target.Name})
	}
	return responses
}

type pinnedTeamPolicyResponse struct {
	TeamID           domain.ID `json:"team_id"`
	TeamName         string    `json:"team_name"`
	PolicyRevisionID domain.ID `json:"policy_revision_id"`
	Revision         int64     `json:"revision"`
}

// escalationProgressResponse is where the escalation machine stands: the
// organization path's walker flattened onto the object, and one entry
// per team policy walker running inline.
type escalationProgressResponse struct {
	Sequence         int64                              `json:"sequence"`
	Phase            application.EscalationPhase        `json:"phase"`
	Urgency          routing.Urgency                    `json:"urgency"`
	CurrentNodeID    string                             `json:"current_node_id,omitempty"`
	LevelOrdinal     int                                `json:"level_ordinal"`
	LevelCount       int                                `json:"level_count"`
	Repeat           int                                `json:"repeat"`
	NextEscalationAt *time.Time                         `json:"next_escalation_at,omitempty"`
	UpdatedAt        time.Time                          `json:"updated_at"`
	Teams            []escalationWalkerProgressResponse `json:"teams"`
}

type escalationWalkerProgressResponse struct {
	TeamID           domain.ID                   `json:"team_id"`
	Phase            application.EscalationPhase `json:"phase"`
	CurrentNodeID    string                      `json:"current_node_id,omitempty"`
	LevelOrdinal     int                         `json:"level_ordinal"`
	LevelCount       int                         `json:"level_count"`
	Repeat           int                         `json:"repeat"`
	NextEscalationAt *time.Time                  `json:"next_escalation_at,omitempty"`
}

func newPinnedTeamPolicyResponses(policies []application.PinnedTeamPolicy) []pinnedTeamPolicyResponse {
	responses := make([]pinnedTeamPolicyResponse, 0, len(policies))
	for _, policy := range policies {
		responses = append(responses, pinnedTeamPolicyResponse{
			TeamID: policy.TeamID, TeamName: policy.TeamName,
			PolicyRevisionID: policy.PolicyRevisionID, Revision: policy.Revision,
		})
	}
	return responses
}

// optionalTime turns the walker's zero "no timer armed" value into an
// absent field rather than the zero instant.
func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func newEscalationProgressResponse(progress *application.EscalationProgress) *escalationProgressResponse {
	if progress == nil {
		return nil
	}
	teams := make([]escalationWalkerProgressResponse, 0, len(progress.Teams))
	for _, team := range progress.Teams {
		teams = append(teams, escalationWalkerProgressResponse{
			TeamID: team.TeamID, Phase: team.Phase, CurrentNodeID: team.CurrentNodeID,
			LevelOrdinal: team.LevelOrdinal, LevelCount: team.LevelCount, Repeat: team.Repeat,
			NextEscalationAt: optionalTime(team.NextAt),
		})
	}
	organization := progress.Organization
	return &escalationProgressResponse{
		Sequence: progress.Sequence, Phase: organization.Phase, Urgency: progress.Urgency,
		CurrentNodeID: organization.CurrentNodeID, LevelOrdinal: organization.LevelOrdinal,
		LevelCount: organization.LevelCount, Repeat: organization.Repeat,
		NextEscalationAt: optionalTime(organization.NextAt), UpdatedAt: progress.UpdatedAt, Teams: teams,
	}
}

func newApprovalRoutingResponse(snapshot application.ApprovalRoutingView, added []domain.ApprovalReviewer) approvalRoutingResponse {
	var owner *approvalRoutingTargetResponse
	if snapshot.AgentOwnerMembershipID != "" {
		owner = &approvalRoutingTargetResponse{Type: "user", ID: snapshot.AgentOwnerMembershipID, Name: snapshot.AgentOwnerName}
	}
	return approvalRoutingResponse{
		AgentOwner: owner, AgentOwnerFallback: snapshot.AgentOwnerFallback, AgentOwnerHandedOffAt: snapshot.AgentOwnerHandedOffAt,
		Outcome: snapshot.Outcome, Reason: snapshot.Reason, Urgency: snapshot.Urgency,
		EscalationPathKey: snapshot.EscalationPathKey, EscalationPathName: snapshot.EscalationPathName,
		EscalationPathRevisionID: snapshot.EscalationPathRevisionID,
		PipelineRevisionID:       snapshot.PipelineRevisionID, BlockID: snapshot.BlockID,
		Targets:        newApprovalRoutingTargetResponses(snapshot.Targets),
		AddedReviewers: newApprovalReviewerResponses(added),
		TeamPolicies:   newPinnedTeamPolicyResponses(snapshot.PinnedTeamPolicies),
		Escalation:     newEscalationProgressResponse(snapshot.Escalation),
		CreatedAt:      snapshot.CreatedAt,
	}
}

// getApprovalRouting is a product read only: AAP never sees routing.
// approvals.RoutingState performs the read authorization and organization
// scoping before the snapshot lookup, and carries the reviewers added
// after the snapshot was pinned.
func (a *API) getApprovalRouting(c *gin.Context) {
	principal := currentPrincipal(c)
	requestID := domain.ID(c.Param("id"))
	state, err := a.approvals.RoutingState(c.Request.Context(), principal, requestID)
	if err != nil {
		a.writeError(c, err)
		return
	}
	if a.routingSnapshots == nil {
		a.writeError(c, domain.ErrNotFound)
		return
	}
	snapshot, err := a.routingSnapshots.GetApprovalRouting(c.Request.Context(), application.GetApprovalRoutingInput{
		OrganizationID: principal.OrganizationID,
		RequestID:      requestID,
	})
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, newApprovalRoutingResponse(snapshot, state.AddedReviewers))
}

func (a *API) listEscalationPaths(c *gin.Context) {
	status := application.EscalationPathStatus(c.DefaultQuery("status", string(application.EscalationPathStatusLive)))
	summaries, err := a.routing.ListPaths(c.Request.Context(), currentPrincipal(c), status, queryInt(c, "limit", 50))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"escalation_paths": newEscalationPathSummaryResponses(summaries)})
}

func (a *API) getActiveEscalationPath(c *gin.Context) {
	revision, err := a.routing.GetActivePath(c.Request.Context(), currentPrincipal(c), c.Param("path_key"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	setActiveRevisionETag(c, revision.Revision.Revision)
	c.JSON(http.StatusOK, newEscalationPathRevisionResponse(revision))
}

func (a *API) createEscalationPath(c *gin.Context) {
	var request escalationPathCreateRequest
	if !bindStrict(c, &request) {
		return
	}
	if request.Nodes == nil {
		a.writeError(c, fmt.Errorf("nodes are required: %w", domain.ErrInvalid))
		return
	}
	revision, err := a.routing.CreatePath(c.Request.Context(), currentPrincipal(c),
		request.PathKey, request.Name, request.document())
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newEscalationPathRevisionResponse(revision))
}

func (a *API) createEscalationPathRevision(c *gin.Context) {
	var request escalationPathDocumentRequest
	if !bindStrict(c, &request) {
		return
	}
	if request.Nodes == nil {
		a.writeError(c, fmt.Errorf("nodes are required: %w", domain.ErrInvalid))
		return
	}
	revision, err := a.routing.CreatePathRevision(c.Request.Context(), currentPrincipal(c),
		c.Param("path_key"), request.Name, request.document())
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newEscalationPathRevisionResponse(revision))
}

// archiveEscalationPath archives the whole path; the page's Archive
// button and scripts share it.
func (a *API) archiveEscalationPath(c *gin.Context) {
	if err := a.routing.ArchivePath(c.Request.Context(), currentPrincipal(c), c.Param("path_key")); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) listEscalationPathRevisions(c *gin.Context) {
	page, err := a.routing.ListPathRevisions(c.Request.Context(), currentPrincipal(c),
		c.Param("path_key"), queryInt(c, "limit", 50), c.Query("cursor"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, revisionPageResponse[escalationPathRevisionSummaryResponse]{
		Revisions: newEscalationPathRevisionSummaryResponses(page.Revisions), NextCursor: page.NextCursor, TotalCount: page.TotalCount,
	})
}

func (a *API) getEscalationPathRevision(c *gin.Context) {
	revisionNumber, err := parsePipelineRevision(c.Param("revision"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	revision, err := a.routing.GetPathRevision(c.Request.Context(), currentPrincipal(c),
		c.Param("path_key"), revisionNumber)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, newEscalationPathRevisionResponse(revision))
}

func (a *API) activateEscalationPathRevision(c *gin.Context) {
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
	revision, err := a.routing.ActivatePathRevision(c.Request.Context(), currentPrincipal(c),
		c.Param("path_key"), revisionNumber, expected)
	if err != nil {
		a.writeError(c, err)
		return
	}
	setActiveRevisionETag(c, revision.Revision.Revision)
	c.JSON(http.StatusOK, newEscalationPathRevisionResponse(revision))
}

// listEscalationPathUses answers the same set that refuses an archive,
// so the page can explain the refusal before the click.
func (a *API) listEscalationPathUses(c *gin.Context) {
	uses, err := a.routing.ListPathUses(c.Request.Context(), currentPrincipal(c), c.Param("path_key"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"uses": newPipelineUseResponses(uses)})
}
