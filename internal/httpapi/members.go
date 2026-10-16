package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
	"github.com/withHumanAI/withHuman/internal/members"
)

// memberView is the management surface's member row.
type memberView struct {
	ID               domain.ID               `json:"id"`
	DisplayName      string                  `json:"display_name"`
	Email            string                  `json:"email"`
	Status           domain.MembershipStatus `json:"status"`
	Assignments      []domain.RoleAssignment `json:"assignments"`
	AdmissionSource  domain.AdmissionSource  `json:"admission_source"`
	DirectoryManaged bool                    `json:"directory_managed"`
	TeamIDs          []domain.ID             `json:"team_ids"`
	CreatedAt        time.Time               `json:"created_at"`
}

func toMemberView(record application.MemberRecord) memberView {
	assignments := record.Membership.Assignments
	if assignments == nil {
		assignments = []domain.RoleAssignment{}
	}
	teamIDs := record.Membership.TeamIDs
	if teamIDs == nil {
		teamIDs = []domain.ID{}
	}
	return memberView{
		ID: record.Membership.ID, DisplayName: record.DisplayName, Email: record.Email,
		Status: record.Membership.Status, Assignments: assignments,
		AdmissionSource:  record.Membership.AdmissionSource,
		DirectoryManaged: record.Membership.DirectoryManaged,
		TeamIDs:          teamIDs,
		CreatedAt:        record.Membership.CreatedAt,
	}
}

// roleSummary is a role as a view names it: enough to label a grant
// without the client fetching the catalog.
type roleSummary struct {
	ID          domain.ID           `json:"id"`
	Key         domain.Role         `json:"key"`
	Name        string              `json:"name"`
	Permissions []domain.Permission `json:"permissions"`
	BuiltIn     bool                `json:"built_in"`
}

// roleIndex loads the roles the organization can see once, so a list of
// invitations or domains resolves every grant's key and name from one
// query. Without a role repository (route-shape tests) the index is
// empty and grants render with ids alone.
func (a *API) roleIndex(ctx context.Context, organizationID domain.ID) (map[domain.ID]roleSummary, error) {
	index := make(map[domain.ID]roleSummary)
	if a.roles == nil || organizationID == "" {
		return index, nil
	}
	roles, err := a.roles.ListRoles(ctx, application.ListRolesInput{OrganizationID: organizationID})
	if err != nil {
		return nil, err
	}
	for _, role := range roles {
		index[role.ID] = roleSummary{ID: role.ID, Key: role.Key, Name: role.Name, Permissions: role.Permissions, BuiltIn: role.BuiltIn}
	}
	return index, nil
}

// grantView is an invitation grant with its role resolved for display:
// the predefined role's key and name when it grants one, and always the
// permissions it carries.
type grantView struct {
	Role        domain.Role         `json:"role,omitempty"`
	RoleName    string              `json:"role_name,omitempty"`
	Permissions []domain.Permission `json:"permissions"`
	ScopeKind   domain.ScopeKind    `json:"scope_kind"`
	ScopeID     string              `json:"scope_id,omitempty"`
}

func toGrantViews(specs []domain.AssignmentSpec, roles map[domain.ID]roleSummary) []grantView {
	views := make([]grantView, 0, len(specs))
	for _, spec := range specs {
		role := roles[spec.RoleID]
		permissions := role.Permissions
		if permissions == nil {
			permissions = []domain.Permission{}
		}
		view := grantView{Permissions: permissions, ScopeKind: spec.ScopeKind, ScopeID: spec.ScopeID}
		if def, ok := domain.BuiltInRoleByID(spec.RoleID); ok {
			view.Role, view.RoleName = def.Key, def.Name
		}
		views = append(views, view)
	}
	return views
}

// invitationView is the invitation as the members page lists it: the
// row plus its grants with role keys and names resolved.
type invitationView struct {
	ID                    domain.ID               `json:"id"`
	OrganizationID        domain.ID               `json:"organization_id"`
	Email                 string                  `json:"email"`
	Grants                []grantView             `json:"grants"`
	InvitedByMembershipID domain.ID               `json:"invited_by_membership_id"`
	Status                domain.InvitationStatus `json:"status"`
	ExpiresAt             time.Time               `json:"expires_at"`
	AcceptedMembershipID  domain.ID               `json:"accepted_membership_id,omitempty"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
}

func toInvitationView(invitation domain.Invitation, roles map[domain.ID]roleSummary) invitationView {
	return invitationView{
		ID: invitation.ID, OrganizationID: invitation.OrganizationID, Email: invitation.Email,
		Grants:                toGrantViews(invitation.Grants, roles),
		InvitedByMembershipID: invitation.InvitedByMembershipID, Status: invitation.Status,
		ExpiresAt: invitation.ExpiresAt, AcceptedMembershipID: invitation.AcceptedMembershipID,
		CreatedAt: invitation.CreatedAt, UpdatedAt: invitation.UpdatedAt,
	}
}

// writeInvitation answers one invitation with its one-time URL.
func (a *API) writeInvitation(c *gin.Context, status int, result members.InvitationWithURL) {
	roles, err := a.roleIndex(c.Request.Context(), result.Invitation.OrganizationID)
	if err != nil {
		a.writeError(c, err)
		return
	}
	body := gin.H{"invitation": toInvitationView(result.Invitation, roles)}
	if result.InviteURL != "" {
		body["invite_url"] = result.InviteURL
	}
	c.JSON(status, body)
}

func (a *API) enrichedMe(c *gin.Context) {
	principal := currentPrincipal(c)
	response := gin.H{"principal": principal}
	if user, err := a.identities.GetUser(c.Request.Context(), application.GetUserInput{UserID: principal.ActorID}); err == nil {
		// has_password tells the account page whether a password form
		// makes sense: an account founded through a social provider has
		// no local credential and can neither change nor reset one.
		_, credentialErr := a.identities.GetLocalCredential(c.Request.Context(), application.GetLocalCredentialInput{Email: user.PrimaryEmail})
		response["user"] = gin.H{
			"display_name": user.DisplayName,
			"email":        user.PrimaryEmail,
			"has_password": credentialErr == nil,
		}
	}
	if organization, err := a.identities.GetOrganization(c.Request.Context(), application.GetOrganizationInput{
		OrganizationID: principal.OrganizationID,
	}); err == nil {
		response["organization"] = gin.H{"name": organization.Name, "slug": organization.Slug}
	}
	// The membership's own assignments plus the ones inherited through
	// its teams, so the account page can say where each grant comes from.
	if principal.MembershipID != "" && a.roles != nil {
		if assignments, err := a.roles.ListAssignments(c.Request.Context(), application.ListAssignmentsInput{
			OrganizationID: principal.OrganizationID, PrincipalKind: domain.PrincipalMembership, PrincipalID: principal.MembershipID,
		}); err == nil {
			if assignments == nil {
				assignments = []domain.RoleAssignment{}
			}
			response["assignments"] = assignments
		}
	}
	// Onboarding is required while nobody has finished or skipped it and
	// this person can run it; the frontend opens /welcome on that signal.
	// A skipped setup nobody has finished since is offered again.
	if a.onboarding != nil {
		if status, err := a.onboarding.Status(c.Request.Context(), principal); err == nil {
			response["onboarding"] = gin.H{"required": status.Required, "skipped": status.Skipped, "completed_at": status.CompletedAt}
		}
	}
	c.JSON(http.StatusOK, response)
}

func (a *API) listMembers(c *gin.Context) {
	records, err := a.members.ListMembers(c.Request.Context(), currentPrincipal(c), queryInt(c, "limit", 200))
	if err != nil {
		a.writeError(c, err)
		return
	}
	views := make([]memberView, 0, len(records))
	for _, record := range records {
		views = append(views, toMemberView(record))
	}
	c.JSON(http.StatusOK, gin.H{"members": views})
}

func (a *API) updateMember(c *gin.Context) {
	var request struct {
		Status string `json:"status" binding:"required"`
		// Force suspends a member an active escalation path still
		// targets; without it that suspension is refused with
		// membership_in_escalation_path.
		Force bool `json:"force"`
	}
	if !bind(c, &request) {
		return
	}
	if request.Status != string(domain.MembershipActive) && request.Status != string(domain.MembershipSuspended) {
		a.writeError(c, fmt.Errorf("status must be active or suspended: %w", domain.ErrInvalid))
		return
	}
	membership, err := a.members.SetMemberStatus(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")), domain.MembershipStatus(request.Status), request.Force)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"member": membership})
}

func (a *API) removeMember(c *gin.Context) {
	_, err := a.members.SetMemberStatus(c.Request.Context(), currentPrincipal(c),
		domain.ID(c.Param("id")), domain.MembershipDeprovisioned, queryBool(c, "force"))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// setMemberRole gives a member one predefined role in place of every
// grant they hold directly.
func (a *API) setMemberRole(c *gin.Context) {
	var request struct {
		Role domain.Role `json:"role" binding:"required"`
	}
	if !bindStrict(c, &request) {
		return
	}
	membership, err := a.members.SetMemberRole(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")), request.Role)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"member": membership})
}

// roleView is a predefined role as the pickers offer it.
type roleView struct {
	Key         domain.Role         `json:"key"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Permissions []domain.Permission `json:"permissions"`
	Dangerous   bool                `json:"dangerous"`
}

// listRoles answers the predefined roles a person can hold. Every member
// may read them: they are the organization's vocabulary for access, the
// same in every organization.
func (a *API) listRoles(c *gin.Context) {
	definitions := domain.HumanRoles()
	views := make([]roleView, 0, len(definitions))
	for _, def := range definitions {
		dangerous := false
		for _, key := range def.Permissions {
			dangerous = dangerous || domain.PermissionIsDangerous(key)
		}
		views = append(views, roleView{Key: def.Key, Name: def.Name, Description: def.Description, Permissions: def.Permissions, Dangerous: dangerous})
	}
	c.JSON(http.StatusOK, gin.H{"roles": views})
}

func (a *API) listInvitations(c *gin.Context) {
	principal := currentPrincipal(c)
	invitations, err := a.members.ListInvitations(c.Request.Context(), principal, queryInt(c, "limit", 100))
	if err != nil {
		a.writeError(c, err)
		return
	}
	roles, err := a.roleIndex(c.Request.Context(), principal.OrganizationID)
	if err != nil {
		a.writeError(c, err)
		return
	}
	views := make([]invitationView, 0, len(invitations))
	for _, invitation := range invitations {
		views = append(views, toInvitationView(invitation, roles))
	}
	c.JSON(http.StatusOK, gin.H{"invitations": views})
}

// createInvitation invites a person with a predefined role, or, where
// the edition offers custom permissions, with explicit permission
// policies; exactly one of the two.
func (a *API) createInvitation(c *gin.Context) {
	var request struct {
		Email    string                        `json:"email" binding:"required"`
		Role     domain.Role                   `json:"role"`
		Policies []domain.PermissionPolicySpec `json:"permission_policies"`
	}
	if !bindStrict(c, &request) {
		return
	}
	principal := currentPrincipal(c)
	if err := authorization.Check(principal, domain.PermissionMembershipInvite, domain.OrganizationResource()); err != nil {
		a.writeError(c, err)
		return
	}
	var specs []domain.AssignmentSpec
	switch {
	case (request.Role == "") == (len(request.Policies) == 0):
		a.writeError(c, fmt.Errorf("choose a role or permission policies, not both: %w", domain.ErrInvalid))
		return
	case request.Role != "":
		grant, err := members.RoleGrant(request.Role)
		if err != nil {
			a.writeError(c, err)
			return
		}
		specs = []domain.AssignmentSpec{grant}
	case !a.config.CustomPermissions:
		a.writeError(c, domain.ErrCustomPermissionsUnavailable)
		return
	default:
		var err error
		if specs, err = a.members.BuildPolicyAssignments(c.Request.Context(), principal, request.Policies, false); err != nil {
			a.writeError(c, err)
			return
		}
	}
	result, err := a.members.CreateInvitation(c.Request.Context(), principal, request.Email, specs)
	if err != nil {
		a.writeError(c, err)
		return
	}
	a.writeInvitation(c, http.StatusCreated, result)
}

func (a *API) resendInvitation(c *gin.Context) {
	result, err := a.members.ResendInvitation(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")))
	if err != nil {
		a.writeError(c, err)
		return
	}
	a.writeInvitation(c, http.StatusOK, result)
}

func (a *API) revokeInvitation(c *gin.Context) {
	if err := a.members.RevokeInvitation(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id"))); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) previewInvitation(c *gin.Context) {
	var request struct {
		Token string `json:"token" binding:"required"`
	}
	if !bind(c, &request) {
		return
	}
	preview, err := a.auth.PreviewInvitation(c.Request.Context(), request.Token)
	if err != nil {
		a.writeError(c, err)
		return
	}
	roles, err := a.roleIndex(c.Request.Context(), preview.Invitation.OrganizationID)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"organization_name":    preview.OrganizationName,
		"organization_slug":    preview.OrganizationSlug,
		"inviter_display_name": preview.InviterDisplayName,
		"email":                preview.Invitation.Email,
		"grants":               toGrantViews(preview.Invitation.Grants, roles),
		"expires_at":           preview.Invitation.ExpiresAt,
		"sso_required":         preview.SSORequired,
		"existing_user":        preview.UserExists,
	})
}

func (a *API) acceptInvitation(c *gin.Context) {
	var request struct {
		Token       string `json:"token" binding:"required"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if !bind(c, &request) {
		return
	}
	input := authn.AcceptInvitationInput{
		Token: request.Token, DisplayName: request.DisplayName, Password: request.Password,
	}
	// A live session turns acceptance into one click when the emails match.
	if request.Password == "" {
		if token, err := c.Cookie(authn.SessionCookieName); err == nil {
			if principal, err := a.auth.ResolveSession(c.Request.Context(), token); err == nil {
				input.Session = &principal
			}
		}
	}
	result, err := a.auth.AcceptInvitation(c.Request.Context(), input)
	if err != nil {
		a.writeError(c, err)
		return
	}
	setPrincipal(c, result.Principal)
	a.setSessionCookie(c, result.Token, result.ExpiresAt)
	c.JSON(http.StatusOK, gin.H{"principal": result.Principal})
}
