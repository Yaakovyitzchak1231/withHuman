package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

func (a *API) installTeamRoutes(router *gin.RouterGroup) {
	read, write := string(domain.PermissionTeamRead), string(domain.PermissionTeamWrite)
	a.handle(router, http.MethodGet, "/teams", read, a.listTeams)
	a.handle(router, http.MethodPost, "/teams", write, a.createTeam)
	a.handle(router, http.MethodGet, "/teams/:id", read, a.getTeam)
	a.handle(router, http.MethodPatch, "/teams/:id", write, a.renameTeam)
	a.handle(router, http.MethodDelete, "/teams/:id", write, a.archiveTeam)
	a.handle(router, http.MethodPut, "/teams/:id/members", string(domain.PermissionTeamMemberWrite), a.setTeamMembers)
}

type teamView struct {
	ArchivedAt       *time.Time              `json:"archived_at"`
	ID               domain.ID               `json:"id"`
	Name             string                  `json:"name"`
	DirectoryManaged bool                    `json:"directory_managed"`
	MemberCount      int                     `json:"member_count"`
	Assignments      []domain.RoleAssignment `json:"assignments"`
	// EscalationPolicy is the team's policy lineage; both revisions are
	// null when the team has never had one.
	EscalationPolicy domain.TeamEscalationPolicyStatus `json:"escalation_policy"`
	CreatedAt        string                            `json:"created_at"`
}

type teamMemberView struct {
	MembershipID domain.ID `json:"membership_id"`
	DisplayName  string    `json:"display_name"`
	Email        string    `json:"email"`
	Status       string    `json:"status"`
	Sources      []string  `json:"sources"`
}

func toTeamView(team domain.Team) teamView {
	return teamView{
		ID: team.ID, Name: team.Name, ArchivedAt: team.ArchivedAt, DirectoryManaged: team.DirectoryManaged,
		MemberCount: team.MemberCount, Assignments: assignmentsOrEmpty(team.Assignments),
		EscalationPolicy: team.EscalationPolicy,
		CreatedAt:        team.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func assignmentsOrEmpty(assignments []domain.RoleAssignment) []domain.RoleAssignment {
	if assignments == nil {
		return []domain.RoleAssignment{}
	}
	return assignments
}

func toTeamMemberViews(members []application.TeamMemberRecord) []teamMemberView {
	views := make([]teamMemberView, 0, len(members))
	for _, member := range members {
		views = append(views, teamMemberView{
			MembershipID: member.MembershipID, DisplayName: member.DisplayName,
			Email: member.Email, Status: string(member.Status), Sources: member.Sources,
		})
	}
	return views
}

func (a *API) listTeams(c *gin.Context) {
	teams, err := a.teams.ListTeams(c.Request.Context(), currentPrincipal(c), queryInt(c, "limit", 200), application.TeamListStatus(c.Query("status")))
	if err != nil {
		a.writeError(c, err)
		return
	}
	views := make([]teamView, 0, len(teams))
	for _, team := range teams {
		views = append(views, toTeamView(team))
	}
	c.JSON(http.StatusOK, gin.H{"teams": views})
}

func (a *API) createTeam(c *gin.Context) {
	var request struct {
		Name string `json:"name" binding:"required"`
	}
	if !bind(c, &request) {
		return
	}
	team, err := a.teams.CreateTeam(c.Request.Context(), currentPrincipal(c), request.Name)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"team": toTeamView(team)})
}

func (a *API) getTeam(c *gin.Context) {
	team, members, err := a.teams.GetTeam(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"team": toTeamView(team), "members": toTeamMemberViews(members)})
}

func (a *API) renameTeam(c *gin.Context) {
	var request struct {
		Name string `json:"name" binding:"required"`
	}
	if !bind(c, &request) {
		return
	}
	team, err := a.teams.RenameTeam(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")), request.Name)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"team": toTeamView(team)})
}

func (a *API) archiveTeam(c *gin.Context) {
	if err := a.teams.ArchiveTeam(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id"))); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// setTeamMembers replaces the manual member set. Force accepts removing
// a member the team's active escalation policy still names; without it
// the refusal lists them so the page can ask.
func (a *API) setTeamMembers(c *gin.Context) {
	var request struct {
		MembershipIDs []domain.ID `json:"membership_ids"`
		Force         bool        `json:"force"`
	}
	if !bind(c, &request) {
		return
	}
	if request.MembershipIDs == nil {
		request.MembershipIDs = []domain.ID{}
	}
	members, err := a.teams.SetTeamMembers(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")), request.MembershipIDs, request.Force)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": toTeamMemberViews(members)})
}
