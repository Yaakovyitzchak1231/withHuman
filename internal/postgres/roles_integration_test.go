package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// reconcileBuiltInRoles runs the boot reconciler the way the server does,
// so a test sees the registry's permissions on the seeded rows.
func reconcileBuiltInRoles(t *testing.T, ctx context.Context, store *Store, now time.Time) application.ReconcileReport {
	t.Helper()
	report, err := store.ReconcileBuiltInRoles(ctx, application.ReconcileBuiltInRolesInput{
		Roles: domain.BuiltInRoles(), Permissions: domain.PermissionKeys(), Now: now,
	})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	return report
}

func organizationGrant(roleID domain.ID) domain.AssignmentSpec {
	return domain.AssignmentSpec{RoleID: roleID, ScopeKind: domain.ScopeOrganization}
}

func hasGrant(grants []domain.Grant, permission domain.Permission, kind domain.ScopeKind, scopeID string) bool {
	return slices.Contains(grants, domain.Grant{Permission: permission, ScopeKind: kind, ScopeID: scopeID})
}

func countRows(t *testing.T, ctx context.Context, store *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := store.Pool().QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return count
}

// Roles as rows against real PostgreSQL: the boot reconciler, the grant
// loader with team inheritance, assignment replacement with its guard
// rails, the writers that mint assignments (activation, invitations, JIT
// admission, directory groups), and the deletes that clean them up.
func TestRolesAgainstRealPostgres(t *testing.T) {
	server := embeddedpgtest.Start(t)
	ctx := context.Background()
	store, err := Open(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	recordAudit(t, store)
	now := time.Now().UTC().Truncate(time.Microsecond)

	// The migration seeded the five rows, so the first reconcile has
	// nothing to seed; a deleted row is seeded back; a second run is a
	// no-op.
	if report := reconcileBuiltInRoles(t, ctx, store, now); report.Seeded != 0 || len(report.StrippedKeys) != 0 {
		t.Fatalf("first reconcile = %#v, want nothing seeded or stripped", report)
	}
	if _, err := store.Pool().Exec(ctx, `DELETE FROM roles WHERE id = $1`, domain.RoleAuditorID); err != nil {
		t.Fatalf("delete auditor row: %v", err)
	}
	if report := reconcileBuiltInRoles(t, ctx, store, now); report.Seeded != 1 || report.Updated != 0 {
		t.Fatalf("reconcile after delete = %#v, want one seeded", report)
	}
	if report := reconcileBuiltInRoles(t, ctx, store, now); report.Seeded != 0 || report.Updated != 0 {
		t.Fatalf("idempotent reconcile = %#v, want no change", report)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM roles WHERE built_in`); count != 5 {
		t.Fatalf("built-in rows = %d, want 5", count)
	}

	activate := func(slug, email string) application.ActivationResult {
		t.Helper()
		result, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
			OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: slug,
			OrganizationName: "Org " + slug, UserID: domain.ID(uuid.NewString()),
			MembershipID: domain.ID(uuid.NewString()), DisplayName: "Founder " + slug,
			Email: email, PasswordHash: "argon2id$test",
			AdmissionSource: domain.AdmissionManual, Now: now,
		})
		if err != nil {
			t.Fatalf("ActivateOrganization(%s): %v", slug, err)
		}
		return result
	}
	org := activate("roles-one", "founder@one.example.com")
	orgID, ownerMembershipID := org.Organization.ID, org.Principal.MembershipID
	other := activate("roles-two", "founder@two.example.com")

	// The owner row holds every human key and the founder holds it.
	owner, err := store.GetRole(ctx, application.GetRoleInput{OrganizationID: orgID, RoleID: domain.RoleOwnerID})
	if err != nil || !slices.Equal(owner.Permissions, domain.HumanPermissions()) {
		t.Fatalf("owner role = %#v, %v; want every human permission", owner, err)
	}
	if !hasGrant(org.Principal.Grants, domain.PermissionOrganizationDelete, domain.ScopeOrganization, "") {
		t.Fatalf("founder principal lacks owner grants: %#v", org.Principal.Grants)
	}
	if owner.AssignmentCount != 1 {
		t.Fatalf("owner assignment count = %d, want 1", owner.AssignmentCount)
	}

	// Listing puts the built-ins first in their canonical order, then
	// custom rows by name; a custom row of another tenant never shows.
	custom, err := store.CreateRole(ctx, application.CreateRoleInput{
		OrganizationID: orgID, RoleID: domain.ID(uuid.NewString()), Key: "pipeline-editor", Name: "Pipeline editor",
		Description:        "Writes pipelines for one agent type.",
		Permissions:        []domain.Permission{domain.PermissionPipelineRead, domain.PermissionPipelineWrite},
		BindableScopeKinds: []domain.ScopeKind{domain.ScopeOrganization, domain.ScopeAgent},
		ActorMembershipID:  ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if _, err := store.CreateRole(ctx, application.CreateRoleInput{
		OrganizationID: orgID, RoleID: domain.ID(uuid.NewString()), Key: "pipeline-editor", Name: "Duplicate",
		Permissions: []domain.Permission{domain.PermissionPipelineRead}, BindableScopeKinds: []domain.ScopeKind{domain.ScopeOrganization},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate key = %v, want conflict", err)
	}
	roles, err := store.ListRoles(ctx, application.ListRolesInput{OrganizationID: orgID})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	keys := make([]domain.Role, len(roles))
	for index, role := range roles {
		keys[index] = role.Key
	}
	if !slices.Equal(keys, []domain.Role{"owner", "admin", "reviewer", "auditor", "agent", "pipeline-editor"}) {
		t.Fatalf("role order = %v", keys)
	}
	if _, err := store.GetRole(ctx, application.GetRoleInput{OrganizationID: other.Organization.ID, RoleID: custom.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("other tenant reads custom role = %v, want not found", err)
	}
	if _, err := store.UpdateRole(ctx, application.UpdateRoleInput{
		OrganizationID: orgID, RoleID: domain.RoleAdminID, Name: "Renamed",
		Permissions: []domain.Permission{domain.PermissionAgentRead}, BindableScopeKinds: []domain.ScopeKind{domain.ScopeOrganization},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrRoleBuiltIn) {
		t.Fatalf("update built-in = %v, want ErrRoleBuiltIn", err)
	}
	if err := store.DeleteRole(ctx, application.DeleteRoleInput{
		OrganizationID: orgID, RoleID: domain.RoleAdminID, ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrRoleBuiltIn) {
		t.Fatalf("delete built-in = %v, want ErrRoleBuiltIn", err)
	}

	// A key the registry no longer knows is stripped from the custom row
	// and the change is audited as the system's.
	if _, err := store.Pool().Exec(ctx, `UPDATE roles SET permissions = permissions || '{ghost.permission}' WHERE id = $1`, custom.ID); err != nil {
		t.Fatalf("plant stale key: %v", err)
	}
	report := reconcileBuiltInRoles(t, ctx, store, now)
	if report.Updated != 1 || !slices.Equal(report.StrippedKeys, []domain.Permission{"ghost.permission"}) {
		t.Fatalf("reconcile with stale key = %#v", report)
	}
	custom, err = store.GetRole(ctx, application.GetRoleInput{OrganizationID: orgID, RoleID: custom.ID})
	if err != nil || !slices.Equal(custom.Permissions, []domain.Permission{domain.PermissionPipelineRead, domain.PermissionPipelineWrite}) {
		t.Fatalf("custom role after strip = %#v, %v", custom, err)
	}
	stripAudit := auditEvents(t, ctx, store, `organization_id = $1 AND event_type = 'role.updated' AND actor_type = 'system'`, orgID)
	if len(stripAudit) != 1 {
		t.Fatalf("strip audit = %#v; want one system role.updated", stripAudit)
	}
	var stripData struct {
		Before []string `json:"before_permissions"`
		After  []string `json:"after_permissions"`
	}
	if err := json.Unmarshal(stripAudit[0].Data, &stripData); err != nil || len(stripData.Before) != 3 || len(stripData.After) != 2 {
		t.Fatalf("strip audit data = %s, %v", stripAudit[0].Data, err)
	}

	// Two more members: one by invitation carrying grants, one the
	// assignments are replaced on.
	invite := func(email string, grants []domain.AssignmentSpec) domain.Principal {
		t.Helper()
		invitationID := domain.ID(uuid.NewString())
		hash := tokenHashOf("invite-" + email)
		created, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
			OrganizationID: orgID, InvitationID: invitationID, Email: email, Grants: grants,
			InvitedByMembershipID: ownerMembershipID, TokenHash: hash, ExpiresAt: now.Add(time.Hour),
			SeatQuota: application.UnlimitedQuota(), Now: now,
		})
		if err != nil {
			t.Fatalf("CreateInvitation(%s): %v", email, err)
		}
		if !slices.Equal(created.Grants, grants) {
			t.Fatalf("created invitation grants = %#v, want %#v", created.Grants, grants)
		}
		preview, err := store.GetInvitationByToken(ctx, application.GetInvitationByTokenInput{
			InvitationID: invitationID, TokenHash: hash, Now: now,
		})
		if err != nil || !slices.Equal(preview.Invitation.Grants, grants) {
			t.Fatalf("preview grants = %#v, %v; want %#v", preview.Invitation.Grants, err, grants)
		}
		principal, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{
			InvitationID: invitationID, TokenHash: hash, AssertedEmail: email,
			NewUserID: domain.ID(uuid.NewString()), DisplayName: email, PasswordHash: "argon2id$test",
			ProviderKey: "local", Issuer: "withhuman:self-hosted", MembershipID: domain.ID(uuid.NewString()),
			SeatQuota: application.UnlimitedQuota(), Now: now,
		})
		if err != nil {
			t.Fatalf("AcceptInvitation(%s): %v", email, err)
		}
		return principal
	}
	reviewer := invite("reviewer@one.example.com", []domain.AssignmentSpec{organizationGrant(domain.RoleReviewerID)})
	if !hasGrant(reviewer.Grants, domain.PermissionRequestDecide, domain.ScopeOrganization, "") ||
		hasGrant(reviewer.Grants, domain.PermissionRequestReadUnrouted, domain.ScopeOrganization, "") {
		t.Fatalf("invited reviewer grants = %#v", reviewer.Grants)
	}
	reviewerMembership, err := store.GetMembership(ctx, application.GetMembershipInput{OrganizationID: orgID, MembershipID: reviewer.MembershipID})
	if err != nil || len(reviewerMembership.Assignments) != 1 || reviewerMembership.Assignments[0].RoleKey != domain.RoleReviewer ||
		reviewerMembership.Assignments[0].GrantedByActorID != ownerMembershipID {
		t.Fatalf("invited membership assignments = %#v, %v", reviewerMembership.Assignments, err)
	}
	// An invitation naming a role the organization cannot grant is
	// refused up front.
	if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
		OrganizationID: other.Organization.ID, InvitationID: domain.ID(uuid.NewString()), Email: "stranger@two.example.com",
		Grants: []domain.AssignmentSpec{organizationGrant(custom.ID)}, InvitedByMembershipID: other.Principal.MembershipID,
		TokenHash: tokenHashOf("stranger"), ExpiresAt: now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invitation with foreign role = %v, want invalid", err)
	}

	// Team inheritance: a team-scoped admin grant reaches its members.
	team, err := store.CreateTeam(ctx, application.CreateTeamInput{OrganizationID: orgID, Name: "Payments", ActorMembershipID: ownerMembershipID, Now: now})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{reviewer.MembershipID},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("SetTeamManualMembers: %v", err)
	}
	teamAssignments, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: team.ID,
		Assignments:       []domain.AssignmentSpec{{RoleID: domain.RoleAdminID, ScopeKind: domain.ScopeTeam, ScopeID: string(team.ID)}},
		ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil || len(teamAssignments) != 1 || teamAssignments[0].ScopeName != "Payments" || teamAssignments[0].ViaTeamID != "" {
		t.Fatalf("team assignments = %#v, %v", teamAssignments, err)
	}
	grants, err := store.LoadGrants(ctx, application.LoadGrantsInput{OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: reviewer.MembershipID})
	if err != nil {
		t.Fatalf("LoadGrants: %v", err)
	}
	if !hasGrant(grants, domain.PermissionRequestDecide, domain.ScopeOrganization, "") ||
		!hasGrant(grants, domain.PermissionTeamMemberWrite, domain.ScopeTeam, string(team.ID)) ||
		hasGrant(grants, domain.PermissionTeamMemberWrite, domain.ScopeOrganization, "") {
		t.Fatalf("inherited grants = %#v", grants)
	}
	inherited, err := store.ListAssignments(ctx, application.ListAssignmentsInput{OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: reviewer.MembershipID})
	if err != nil || len(inherited) != 2 {
		t.Fatalf("membership assignments = %#v, %v; want direct and inherited", inherited, err)
	}
	viaIndex := slices.IndexFunc(inherited, func(a domain.RoleAssignment) bool { return a.RoleKey == domain.RoleAdmin })
	if viaIndex < 0 || inherited[viaIndex].ViaTeamID != team.ID || inherited[viaIndex].ViaTeamName != "Payments" {
		t.Fatalf("inherited row = %#v", inherited)
	}
	members, err := store.ListMembers(ctx, application.ListMembersInput{OrganizationID: orgID})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	for _, member := range members {
		want := 1
		if member.Membership.ID == reviewer.MembershipID {
			want = 2
		}
		if len(member.Membership.Assignments) != want {
			t.Fatalf("member %s assignments = %#v, want %d", member.Email, member.Membership.Assignments, want)
		}
	}
	// The session principal carries the same grants.
	session, err := store.CreateSession(ctx, application.CreateSessionInput{
		UserID: reviewer.ActorID, OrganizationID: orgID, SecretHash: tokenHashOf("reviewer-session"),
		IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		Auth: domain.AuthContext{Method: "local_password", Assurance: "single_factor", AuthenticatedAt: now},
	})
	if err != nil || !hasGrant(session.Grants, domain.PermissionTeamMemberWrite, domain.ScopeTeam, string(team.ID)) {
		t.Fatalf("session grants = %#v, %v", session.Grants, err)
	}

	// Replacement inserts and deletes the difference, audits each row,
	// and a role from another tenant or an unbindable scope is invalid.
	replaced, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: reviewer.MembershipID,
		Assignments:       []domain.AssignmentSpec{organizationGrant(domain.RoleAuditorID), {RoleID: custom.ID, ScopeKind: domain.ScopeAgent, ScopeID: "deploy-bot"}},
		ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("ReplaceAssignments: %v", err)
	}
	direct := slices.DeleteFunc(slices.Clone(replaced), func(a domain.RoleAssignment) bool { return a.ViaTeamID != "" })
	if len(direct) != 2 || direct[0].RoleKey != domain.RoleAuditor || direct[1].RoleKey != "pipeline-editor" || direct[1].ScopeID != "deploy-bot" {
		t.Fatalf("replaced direct assignments = %#v", direct)
	}
	assignmentAudit := auditEvents(t, ctx, store, `organization_id = $1 AND event_type IN ('role_assignment.granted', 'role_assignment.revoked')
		AND subject_type = 'membership' AND subject_id = $2`, orgID, reviewer.MembershipID)
	if len(assignmentAudit) != 4 {
		t.Fatalf("assignment audit = %d events; want invite grant, revoke, two grants", len(assignmentAudit))
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: reviewer.MembershipID,
		Assignments:       []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeTeam, ScopeID: string(team.ID)}},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("reviewer at team scope = %v, want invalid", err)
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: other.Organization.ID, PrincipalKind: domain.PrincipalMembership, PrincipalID: other.Principal.MembershipID,
		Assignments:       []domain.AssignmentSpec{organizationGrant(domain.RoleOwnerID), organizationGrant(custom.ID)},
		ActorMembershipID: other.Principal.MembershipID, Now: now,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("foreign custom role = %v, want invalid", err)
	}
	// The last owner cannot lose the role or be suspended.
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: ownerMembershipID,
		Assignments:       []domain.AssignmentSpec{organizationGrant(domain.RoleAdminID)},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("last owner removal = %v, want conflict", err)
	}
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: ownerMembershipID, Status: domain.MembershipSuspended,
		ActorMembershipID: reviewer.MembershipID, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("last owner suspension = %v, want conflict", err)
	}
	if hasOwner, err := store.HasOwner(ctx); err != nil || !hasOwner {
		t.Fatalf("HasOwner = %v, %v", hasOwner, err)
	}

	// A role that is assigned or promised by a pending invitation cannot
	// be deleted; once both are gone it can.
	if err := store.DeleteRole(ctx, application.DeleteRoleInput{OrganizationID: orgID, RoleID: custom.ID, ActorMembershipID: ownerMembershipID, Now: now}); !errors.Is(err, domain.ErrRoleAssigned) {
		t.Fatalf("delete assigned role = %v, want ErrRoleAssigned", err)
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: reviewer.MembershipID,
		Assignments:       []domain.AssignmentSpec{organizationGrant(domain.RoleReviewerID)},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("restore reviewer: %v", err)
	}
	pendingInvitationID := domain.ID(uuid.NewString())
	if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
		OrganizationID: orgID, InvitationID: pendingInvitationID, Email: "pending@one.example.com",
		Grants: []domain.AssignmentSpec{organizationGrant(custom.ID)}, InvitedByMembershipID: ownerMembershipID,
		TokenHash: tokenHashOf("pending"), ExpiresAt: now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("CreateInvitation(pending): %v", err)
	}
	if err := store.DeleteRole(ctx, application.DeleteRoleInput{OrganizationID: orgID, RoleID: custom.ID, ActorMembershipID: ownerMembershipID, Now: now}); !errors.Is(err, domain.ErrRoleAssigned) {
		t.Fatalf("delete invited role = %v, want ErrRoleAssigned", err)
	}
	if err := store.RevokeInvitation(ctx, application.RevokeInvitationInput{OrganizationID: orgID, InvitationID: pendingInvitationID, Now: now}); err != nil {
		t.Fatalf("RevokeInvitation: %v", err)
	}
	if err := store.DeleteRole(ctx, application.DeleteRoleInput{OrganizationID: orgID, RoleID: custom.ID, ActorMembershipID: ownerMembershipID, Now: now}); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	// Unattended admission (the hosted just-in-time admission under a
	// verified domain) grants the default role the edition chose, as the
	// system, and binds the asserted identity.
	admitted, err := store.AdmitExternalMember(ctx, application.AdmitExternalMemberInput{
		OrganizationID: orgID, MembershipID: domain.ID(uuid.NewString()), NewUserID: domain.ID(uuid.NewString()),
		Email: "hire@one.example.com", DisplayName: "New Hire", RoleID: domain.RoleAuditorID, AdmissionSource: domain.AdmissionSSOJIT,
		ProviderKey: "polis", Issuer: "https://auth.example.com", Subject: "google-1", Now: now,
	})
	if err != nil || !hasGrant(admitted.Grants, domain.PermissionRequestReadUnrouted, domain.ScopeOrganization, "") ||
		hasGrant(admitted.Grants, domain.PermissionRequestDecide, domain.ScopeOrganization, "") {
		t.Fatalf("admitted grants = %#v, %v", admitted.Grants, err)
	}
	admittedRows, err := store.ListAssignments(ctx, application.ListAssignmentsInput{OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: admitted.MembershipID})
	if err != nil || len(admittedRows) != 1 || admittedRows[0].RoleKey != domain.RoleAuditor || admittedRows[0].GrantedByActorID != "" {
		t.Fatalf("admitted assignments = %#v, %v", admittedRows, err)
	}

	// A directory group maps to a grant its team holds; removing the
	// group clears it. A directory-managed membership refuses manual
	// replacement.
	directoryNow := now.Add(time.Minute)
	applyDirectory := func(eventID string, commands ...application.DirectoryCommandRecord) {
		t.Helper()
		if err := store.ApplyDirectoryCommands(ctx, application.ApplyDirectoryCommandsInput{
			OrganizationID: orgID, AdapterKey: "polis", ConnectionExternalID: "dir-1",
			Commands: commands, SeatQuota: application.UnlimitedQuota(), Now: directoryNow,
		}); err != nil {
			t.Fatalf("ApplyDirectoryCommands(%s): %v", eventID, err)
		}
	}
	groupData, _ := json.Marshal(map[string]any{"name": "Directory reviewers"})
	userData, _ := json.Marshal(map[string]any{"email": "scim@one.example.com", "display_name": "SCIM User", "active": true, "removed": false})
	applyDirectory("event-1",
		application.DirectoryCommandRecord{Type: "upsert_group", ExternalID: "grp-1", Data: groupData},
		application.DirectoryCommandRecord{Type: "upsert_user", ExternalID: "scim-1", Data: userData})
	if err := store.SetDirectoryGroupGrant(ctx, application.SetDirectoryGroupGrantInput{
		OrganizationID: orgID, ProviderObjectID: "grp-missing", Grant: &domain.AssignmentSpec{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("grant on unknown group = %v, want not found", err)
	}
	reviewerGrant := organizationGrant(domain.RoleReviewerID)
	if err := store.SetDirectoryGroupGrant(ctx, application.SetDirectoryGroupGrantInput{
		OrganizationID: orgID, ProviderObjectID: "grp-1", Grant: &reviewerGrant, ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("SetDirectoryGroupGrant: %v", err)
	}
	links, err := store.ListDirectoryGroupLinks(ctx, application.ListDirectoryGroupLinksInput{OrganizationID: orgID})
	if err != nil || len(links) != 1 || links[0].Grant == nil || *links[0].Grant != reviewerGrant ||
		links[0].RoleKey != domain.RoleReviewer || links[0].TeamName != "Directory reviewers" {
		t.Fatalf("group links = %#v, %v", links, err)
	}
	directoryTeamID := links[0].TeamID
	groupRows, err := store.ListAssignments(ctx, application.ListAssignmentsInput{OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: directoryTeamID})
	if err != nil || len(groupRows) != 1 || groupRows[0].Source != domain.AssignmentDirectory || groupRows[0].SourceKey != "grp-1" {
		t.Fatalf("directory team assignments = %#v, %v", groupRows, err)
	}
	// Manual replacement on the team leaves the directory row alone.
	teamRows, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: directoryTeamID,
		ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil || len(teamRows) != 1 || teamRows[0].Source != domain.AssignmentDirectory {
		t.Fatalf("directory row after empty manual replace = %#v, %v", teamRows, err)
	}
	var scimMembershipID domain.ID
	members, _ = store.ListMembers(ctx, application.ListMembersInput{OrganizationID: orgID})
	for _, member := range members {
		if member.Membership.DirectoryManaged {
			scimMembershipID = member.Membership.ID
		}
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: scimMembershipID,
		Assignments:       []domain.AssignmentSpec{reviewerGrant},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("replace on directory-managed membership = %v, want conflict", err)
	}
	if err := store.SetDirectoryGroupGrant(ctx, application.SetDirectoryGroupGrantInput{
		OrganizationID: orgID, ProviderObjectID: "grp-1", ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("clear group grant: %v", err)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM role_assignments WHERE source = 'directory' AND organization_id = $1`, orgID); count != 0 {
		t.Fatalf("directory rows after clear = %d", count)
	}
	if err := store.SetDirectoryGroupGrant(ctx, application.SetDirectoryGroupGrantInput{
		OrganizationID: orgID, ProviderObjectID: "grp-1", Grant: &reviewerGrant, ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("re-grant group: %v", err)
	}
	directoryPolicy := createTeamPolicyRevision(t, ctx, store, orgID, directoryTeamID, ownerMembershipID, teamPolicyDocument(true), now)
	activateTeamPolicyRevision(t, ctx, store, orgID, directoryTeamID, ownerMembershipID, directoryPolicy.Revision, nil, now)
	applyDirectory("event-2", application.DirectoryCommandRecord{Type: "remove_group", ExternalID: "grp-1"})
	archivedDirectoryTeam, _, err := store.GetTeam(ctx, application.GetTeamInput{OrganizationID: orgID, TeamID: directoryTeamID})
	if err != nil || archivedDirectoryTeam.ArchivedAt == nil || archivedDirectoryTeam.EscalationPolicy.ActiveRevision != nil {
		t.Fatalf("directory archive = %+v, %v", archivedDirectoryTeam, err)
	}

	if count := countRows(t, ctx, store, `SELECT count(*) FROM role_assignments WHERE source = 'directory' AND organization_id = $1`, orgID); count != 0 {
		t.Fatalf("directory rows after remove_group = %d", count)
	}

	// Archiving a team removes its own grants and the grants scoped to it.
	scoped, err := store.CreateTeam(ctx, application.CreateTeamInput{OrganizationID: orgID, Name: "Scoped", ActorMembershipID: ownerMembershipID, Now: now})
	if err != nil {
		t.Fatalf("CreateTeam(scoped): %v", err)
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: scoped.ID,
		Assignments:       []domain.AssignmentSpec{organizationGrant(domain.RoleAuditorID)},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("grant to scoped team: %v", err)
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: admitted.MembershipID,
		Assignments:       []domain.AssignmentSpec{organizationGrant(domain.RoleAuditorID), {RoleID: domain.RoleAdminID, ScopeKind: domain.ScopeTeam, ScopeID: string(scoped.ID)}},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("grant at scoped team: %v", err)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM role_assignments WHERE principal_id = $1 OR scope_id = $1::text`, scoped.ID); count != 2 {
		t.Fatalf("rows touching scoped team = %d, want 2", count)
	}
	if err := store.ArchiveTeam(ctx, application.ArchiveTeamInput{OrganizationID: orgID, TeamID: scoped.ID, ActorMembershipID: ownerMembershipID, Now: now}); err != nil {
		t.Fatalf("ArchiveTeam: %v", err)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM role_assignments WHERE principal_id = $1 OR scope_id = $1::text`, scoped.ID); count != 0 {
		t.Fatalf("rows touching archived team = %d", count)
	}

	// The purge removes assignments and custom roles, and still copes
	// with legacy membership_roles rows the expand migration copied.
	if _, err := store.CreateRole(ctx, application.CreateRoleInput{
		OrganizationID: orgID, RoleID: domain.ID(uuid.NewString()), Key: "leftover", Name: "Leftover",
		Permissions: []domain.Permission{domain.PermissionAgentRead}, BindableScopeKinds: []domain.ScopeKind{domain.ScopeOrganization},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("CreateRole(leftover): %v", err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO membership_roles (membership_id, role) VALUES ($1, 'owner')`, ownerMembershipID); err != nil {
		t.Fatalf("plant legacy membership_roles row: %v", err)
	}
	scheduled := now.Add(-time.Minute)
	if _, err := store.SetOrganizationStatus(ctx, application.SetOrganizationStatusInput{
		OrganizationID: orgID, Status: domain.OrganizationPendingDeletion, Reason: "test",
		DeletionScheduledFor: &scheduled, ActorType: "system", Now: now,
	}); err != nil {
		t.Fatalf("schedule deletion: %v", err)
	}
	if purged, err := store.PurgeOrganization(ctx, application.PurgeOrganizationInput{OrganizationID: orgID, Now: now}); err != nil || !purged {
		t.Fatalf("PurgeOrganization = %v, %v", purged, err)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM role_assignments WHERE organization_id = $1`, orgID); count != 0 {
		t.Fatalf("assignments after purge = %d", count)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM roles WHERE organization_id = $1`, orgID); count != 0 {
		t.Fatalf("custom roles after purge = %d", count)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM roles WHERE built_in`); count != 5 {
		t.Fatalf("built-in roles after purge = %d, want 5", count)
	}
	if count := countRows(t, ctx, store, `SELECT count(*) FROM role_assignments WHERE organization_id = $1`, other.Organization.ID); count != 1 {
		t.Fatalf("other tenant's assignments after purge = %d, want 1", count)
	}
}
