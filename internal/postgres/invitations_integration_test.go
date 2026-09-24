package postgres

import (
	"context"
	"crypto/sha256"
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

func tokenHashOf(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}

// Exercises the invitation lifecycle, the admission transaction, the
// membership management guard rails, and the directory apply pipeline
// against a real PostgreSQL with the production migrations and triggers.
func TestInvitationsAndMembershipsAgainstRealPostgres(t *testing.T) {
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
	orgID := domain.ID(uuid.NewString())
	ownerID := domain.ID(uuid.NewString())
	ownerMembershipID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "acme", OrganizationName: "Acme",
		UserID: ownerID, MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}

	invitationID := domain.ID(uuid.NewString())
	invitation, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
		OrganizationID: orgID, InvitationID: invitationID,
		Email: "Invitee@Example.com", Grants: []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
		InvitedByMembershipID: ownerMembershipID,
		TokenHash:             tokenHashOf("secret-1"),
		ExpiresAt:             now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if invitation.Status != domain.InvitationPending {
		t.Fatalf("invitation status = %s", invitation.Status)
	}

	// One live invitation per email per organization, case-insensitively.
	if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
		OrganizationID: orgID, InvitationID: domain.ID(uuid.NewString()),
		Email: "invitee@example.com", Grants: []domain.AssignmentSpec{{RoleID: domain.RoleAuditorID, ScopeKind: domain.ScopeOrganization}},
		InvitedByMembershipID: ownerMembershipID,
		TokenHash:             tokenHashOf("secret-2"),
		ExpiresAt:             now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate pending invitation error = %v", err)
	}

	// Inviting an existing member conflicts.
	if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
		OrganizationID: orgID, InvitationID: domain.ID(uuid.NewString()),
		Email: "owner@example.com", Grants: []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
		InvitedByMembershipID: ownerMembershipID,
		TokenHash:             tokenHashOf("secret-3"),
		ExpiresAt:             now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("existing member invitation error = %v", err)
	}

	// Preview requires the right token hash.
	if _, err := store.GetInvitationByToken(ctx, application.GetInvitationByTokenInput{
		InvitationID: invitationID, TokenHash: tokenHashOf("wrong"), Now: now,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("wrong token error = %v", err)
	}
	preview, err := store.GetInvitationByToken(ctx, application.GetInvitationByTokenInput{
		InvitationID: invitationID, TokenHash: tokenHashOf("secret-1"), Now: now,
	})
	if err != nil {
		t.Fatalf("GetInvitationByToken: %v", err)
	}
	if preview.OrganizationName != "Acme" || preview.UserExists {
		t.Fatalf("unexpected preview: %#v", preview)
	}

	// Resend rotates the token; the old secret stops working.
	if _, err := store.RotateInvitationToken(ctx, application.RotateInvitationTokenInput{
		OrganizationID: orgID, InvitationID: invitationID,
		TokenHash: tokenHashOf("secret-1b"), ExpiresAt: now.Add(2 * time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("RotateInvitationToken: %v", err)
	}
	if _, err := store.GetInvitationByToken(ctx, application.GetInvitationByTokenInput{
		InvitationID: invitationID, TokenHash: tokenHashOf("secret-1"), Now: now,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old token still previews: %v", err)
	}

	// Email binding is enforced in the admission transaction.
	if _, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{
		InvitationID: invitationID, TokenHash: tokenHashOf("secret-1b"),
		AssertedEmail: "other@example.com",
		NewUserID:     domain.ID(uuid.NewString()), DisplayName: "Wrong Person",
		ProviderKey: "local", Issuer: "withhuman:self-hosted",
		MembershipID: domain.ID(uuid.NewString()), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); !errors.Is(err, domain.ErrEmailMismatch) {
		t.Fatalf("email mismatch error = %v", err)
	}

	// Admission creates the user, credential, identity, membership, roles.
	inviteeID := domain.ID(uuid.NewString())
	inviteeMembershipID := domain.ID(uuid.NewString())
	principal, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{
		InvitationID: invitationID, TokenHash: tokenHashOf("secret-1b"),
		AssertedEmail: "invitee@example.com",
		NewUserID:     inviteeID, DisplayName: "Invitee",
		PasswordHash: "argon2id$invitee",
		ProviderKey:  "local", Issuer: "withhuman:self-hosted", Subject: string(inviteeID),
		MembershipID: inviteeMembershipID, SeatQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}
	if principal.ActorID != inviteeID || !slices.Contains(principal.Permissions, domain.PermissionRequestDecide) {
		t.Fatalf("unexpected admitted principal: %#v", principal)
	}
	membership, err := store.GetMembership(ctx, application.GetMembershipInput{
		OrganizationID: orgID, MembershipID: inviteeMembershipID,
	})
	if err != nil || membership.AdmissionSource != domain.AdmissionInvite || membership.Status != domain.MembershipActive {
		t.Fatalf("unexpected membership: %#v, %v", membership, err)
	}

	// Acceptance is terminal.
	if _, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{
		InvitationID: invitationID, TokenHash: tokenHashOf("secret-1b"),
		AssertedEmail: "invitee@example.com", UserID: inviteeID,
		MembershipID: domain.ID(uuid.NewString()), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("re-accept error = %v", err)
	}

	// Assignment replacement, last-owner protection, and directory guard rails.
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: inviteeMembershipID,
		Assignments: []domain.AssignmentSpec{
			{RoleID: domain.RoleAdminID, ScopeKind: domain.ScopeOrganization},
			{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization},
		},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("ReplaceAssignments: %v", err)
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: ownerMembershipID,
		Assignments:       []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("last owner role removal error = %v", err)
	}
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: ownerMembershipID,
		Status: domain.MembershipSuspended, ActorMembershipID: inviteeMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("last owner suspension error = %v", err)
	}

	// An active escalation path targeting the member refuses suspension and
	// names the path; forcing it records the bypass in the audit event.
	pathRevision, err := store.CreateEscalationPath(ctx, application.CreateEscalationPathInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PathKey: "payments",
		Name: "Payments", Document: userTargetPathDocument(inviteeMembershipID), DocumentVersion: 2,
		CreatedByActorID: ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("create path revision: %v", err)
	}
	if _, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
		OrganizationID: orgID, PathKey: "payments", Revision: pathRevision.Revision,
		ActorID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("activate path revision: %v", err)
	}
	var inPath domain.MembershipInEscalationPathError
	_, err = store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: inviteeMembershipID,
		Status: domain.MembershipSuspended, ActorMembershipID: ownerMembershipID, Now: now,
	})
	if !errors.As(err, &inPath) || !errors.Is(err, domain.ErrMembershipInEscalationPath) {
		t.Fatalf("suspension of a targeted member error = %v", err)
	}
	if inPath.MembershipID != inviteeMembershipID || len(inPath.Paths) != 1 || inPath.Paths[0].PathKey != "payments" ||
		inPath.Paths[0].Name != "Payments" || inPath.Paths[0].Revision != pathRevision.Revision {
		t.Fatalf("targeting paths = %+v", inPath)
	}
	forced, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: inviteeMembershipID,
		Status: domain.MembershipSuspended, Force: true, ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil || forced.Status != domain.MembershipSuspended {
		t.Fatalf("forced suspension = %+v, %v", forced, err)
	}
	suspendedEvents := auditEvents(t, ctx, store, `organization_id = $1 AND event_type = 'membership.suspended'`, orgID)
	if len(suspendedEvents) != 1 {
		t.Fatalf("suspended audit events = %+v", suspendedEvents)
	}
	var suspendedData struct {
		Forced          bool     `json:"forced"`
		EscalationPaths []string `json:"escalation_paths"`
	}
	if err := json.Unmarshal(suspendedEvents[0].Data, &suspendedData); err != nil {
		t.Fatal(err)
	}
	if !suspendedData.Forced || len(suspendedData.EscalationPaths) != 1 || suspendedData.EscalationPaths[0] != "payments" {
		t.Fatalf("forced suspension audit data = %s", suspendedEvents[0].Data)
	}
	// Reactivation never consults the paths, and an archived path no
	// longer guards anything.
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: inviteeMembershipID,
		Status: domain.MembershipActive, ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if _, err := store.ArchiveEscalationPath(ctx, application.ArchiveEscalationPathInput{
		OrganizationID: orgID, PathKey: "payments", ActorID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("archive path: %v", err)
	}

	// An active team escalation policy naming the member guards the same
	// way, naming the team instead of a path.
	guardTeam, err := store.CreateTeam(ctx, application.CreateTeamInput{
		OrganizationID: orgID, Name: "payments", ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: guardTeam.ID, MembershipIDs: []domain.ID{inviteeMembershipID},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("SetTeamManualMembers: %v", err)
	}
	teamPolicy := createTeamPolicyRevision(t, ctx, store, orgID, guardTeam.ID, ownerMembershipID, teamPolicyDocument(false, inviteeMembershipID), now)
	activateTeamPolicyRevision(t, ctx, store, orgID, guardTeam.ID, ownerMembershipID, teamPolicy.Revision, nil, now)
	_, err = store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: inviteeMembershipID,
		Status: domain.MembershipSuspended, ActorMembershipID: ownerMembershipID, Now: now,
	})
	inPath = domain.MembershipInEscalationPathError{}
	if !errors.As(err, &inPath) || len(inPath.Paths) != 0 || len(inPath.TeamPolicies) != 1 ||
		inPath.TeamPolicies[0].TeamID != guardTeam.ID || inPath.TeamPolicies[0].TeamName != "payments" ||
		inPath.TeamPolicies[0].Revision != teamPolicy.Revision {
		t.Fatalf("suspension of a policy-targeted member error = %v (%+v)", err, inPath)
	}
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: inviteeMembershipID,
		Status: domain.MembershipSuspended, Force: true, ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("forced suspension past a team policy: %v", err)
	}
	suspendedEvents = auditEvents(t, ctx, store, `organization_id = $1 AND event_type = 'membership.suspended'`, orgID)
	if len(suspendedEvents) != 2 {
		t.Fatalf("suspended audit events = %+v", suspendedEvents)
	}
	var policyForcedData struct {
		Forced                 bool     `json:"forced"`
		EscalationPaths        []string `json:"escalation_paths"`
		TeamEscalationPolicies []string `json:"team_escalation_policies"`
	}
	if err := json.Unmarshal(suspendedEvents[0].Data, &policyForcedData); err != nil {
		t.Fatal(err)
	}
	if !policyForcedData.Forced || len(policyForcedData.EscalationPaths) != 0 ||
		len(policyForcedData.TeamEscalationPolicies) != 1 || policyForcedData.TeamEscalationPolicies[0] != string(guardTeam.ID) {
		t.Fatalf("forced suspension audit data = %s", suspendedEvents[0].Data)
	}
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: inviteeMembershipID,
		Status: domain.MembershipActive, ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("reactivate after team policy: %v", err)
	}
	if _, err := store.DeactivateTeamEscalationPolicy(ctx, application.DeactivateTeamEscalationPolicyInput{
		OrganizationID: orgID, TeamID: guardTeam.ID, ExpectedActiveRevision: teamPolicy.Revision,
		ActorID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("archive team policy: %v", err)
	}

	// Deprovisioning revokes the member's sessions in the organization.
	sessionSecret := tokenHashOf("session-secret")
	if _, err := store.CreateSession(ctx, application.CreateSessionInput{
		UserID: inviteeID, OrganizationID: orgID, SecretHash: sessionSecret,
		IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		Auth: domain.AuthContext{Method: "local_password", Assurance: "single_factor", AuthenticatedAt: now},
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: inviteeMembershipID,
		Status: domain.MembershipDeprovisioned, RevokeSessions: true,
		ActorMembershipID: ownerMembershipID, Now: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("deprovision: %v", err)
	}
	if _, err := store.ResolveSession(ctx, application.ResolveSessionInput{
		SecretHash: sessionSecret, Now: now.Add(2 * time.Minute),
	}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("session survived deprovisioning: %v", err)
	}

	// Directory pipeline: inbox insert is idempotent, apply materializes a
	// directory-managed membership, deactivation suspends, reapply is a
	// no-op, and manual edits on directory-managed rows are refused.
	directoryNow := now.Add(5 * time.Minute)
	userData, _ := json.Marshal(map[string]any{
		"email": "scim.user@example.com", "display_name": "SCIM User", "active": true, "removed": false,
	})
	applyInput := application.ApplyDirectoryCommandsInput{
		OrganizationID: orgID, AdapterKey: "polis", ConnectionExternalID: "dir-1",
		Commands:  []application.DirectoryCommandRecord{{Type: "upsert_user", ExternalID: "scim-1", Data: userData}},
		SeatQuota: application.UnlimitedQuota(), Now: directoryNow,
	}
	if err := store.ApplyDirectoryCommands(ctx, applyInput); err != nil {
		t.Fatalf("ApplyDirectoryCommands: %v", err)
	}
	if err := store.ApplyDirectoryCommands(ctx, applyInput); err != nil {
		t.Fatalf("reapply: %v", err)
	}
	members, err := store.ListMembers(ctx, application.ListMembersInput{OrganizationID: orgID})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	var scimMembershipID domain.ID
	for _, member := range members {
		if member.Email == "scim.user@example.com" {
			if !member.Membership.DirectoryManaged || member.Membership.Status != domain.MembershipActive {
				t.Fatalf("unexpected directory member: %#v", member.Membership)
			}
			scimMembershipID = member.Membership.ID
		}
	}
	if scimMembershipID == "" {
		t.Fatal("directory member was not materialized")
	}
	if _, err := store.SetMembershipStatus(ctx, application.SetMembershipStatusInput{
		OrganizationID: orgID, MembershipID: scimMembershipID,
		Status: domain.MembershipSuspended, ActorMembershipID: ownerMembershipID, Now: directoryNow,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("manual edit of directory-managed membership error = %v", err)
	}

	deactivateData, _ := json.Marshal(map[string]any{
		"email": "scim.user@example.com", "display_name": "SCIM User", "active": false, "removed": false,
	})
	if err := store.ApplyDirectoryCommands(ctx, application.ApplyDirectoryCommandsInput{
		OrganizationID: orgID, AdapterKey: "polis", ConnectionExternalID: "dir-1",
		Commands:  []application.DirectoryCommandRecord{{Type: "upsert_user", ExternalID: "scim-1", Data: deactivateData}},
		SeatQuota: application.UnlimitedQuota(), Now: directoryNow,
	}); err != nil {
		t.Fatalf("apply deactivation: %v", err)
	}
	suspended, err := store.GetMembership(ctx, application.GetMembershipInput{
		OrganizationID: orgID, MembershipID: scimMembershipID,
	})
	if err != nil || suspended.Status != domain.MembershipSuspended {
		t.Fatalf("directory deactivation status = %#v, %v", suspended, err)
	}

	// Revoked invitations stop redeeming.
	revokedID := domain.ID(uuid.NewString())
	if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
		OrganizationID: orgID, InvitationID: revokedID,
		Email: "revoked@example.com", Grants: []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
		InvitedByMembershipID: ownerMembershipID,
		TokenHash:             tokenHashOf("secret-r"), ExpiresAt: now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("create revocable invitation: %v", err)
	}
	if err := store.RevokeInvitation(ctx, application.RevokeInvitationInput{
		OrganizationID: orgID, InvitationID: revokedID, Now: now,
	}); err != nil {
		t.Fatalf("RevokeInvitation: %v", err)
	}
	if _, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{
		InvitationID: revokedID, TokenHash: tokenHashOf("secret-r"),
		AssertedEmail: "revoked@example.com",
		NewUserID:     domain.ID(uuid.NewString()), DisplayName: "Too Late",
		ProviderKey: "local", Issuer: "withhuman:self-hosted",
		MembershipID: domain.ID(uuid.NewString()), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoked acceptance error = %v", err)
	}

	// Expired invitations lazily flip to expired at redemption.
	expiredID := domain.ID(uuid.NewString())
	if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
		OrganizationID: orgID, InvitationID: expiredID,
		Email: "late@example.com", Grants: []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
		InvitedByMembershipID: ownerMembershipID,
		TokenHash:             tokenHashOf("secret-l"), ExpiresAt: now.Add(time.Minute), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("create expiring invitation: %v", err)
	}
	if _, err := store.GetInvitationByToken(ctx, application.GetInvitationByTokenInput{
		InvitationID: expiredID, TokenHash: tokenHashOf("secret-l"), Now: now.Add(time.Hour),
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired preview error = %v", err)
	}
	invitations, err := store.ListInvitations(ctx, application.ListInvitationsInput{OrganizationID: orgID})
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	statuses := map[domain.ID]domain.InvitationStatus{}
	for _, item := range invitations {
		statuses[item.ID] = item.Status
	}
	if statuses[expiredID] != domain.InvitationExpired || statuses[revokedID] != domain.InvitationRevoked || statuses[invitationID] != domain.InvitationAccepted {
		t.Fatalf("unexpected invitation statuses: %#v", statuses)
	}
}

func userTargetPathDocument(membershipID domain.ID) json.RawMessage {
	return json.RawMessage(`{"working_hours":[],"nodes":[{"id":"` + uuid.NewString() +
		`","type":"level","targets":[{"type":"user","id":"` + string(membershipID) + `"}],"escalate_after":"15m"}]}`)
}

// An unauthenticated acceptance never attaches an existing global account
// by email on its own; only an admission that vouched for the address
// (LinkExistingUser) may, and then it links rather than duplicates.
func TestAcceptInvitationAttachesExistingAccountOnlyWhenVouchedFor(t *testing.T) {
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
	victimID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: "corp-a", OrganizationName: "Corp A",
		UserID: victimID, MembershipID: domain.ID(uuid.NewString()), DisplayName: "Victim",
		Email: "victim@corp-a.example", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization corp-a: %v", err)
	}
	attackerOrgID := domain.ID(uuid.NewString())
	attackerMembershipID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: attackerOrgID, OrganizationSlug: "corp-b", OrganizationName: "Corp B",
		UserID: domain.ID(uuid.NewString()), MembershipID: attackerMembershipID, DisplayName: "Attacker",
		Email: "owner@corp-b.example", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization corp-b: %v", err)
	}
	invitationID := domain.ID(uuid.NewString())
	if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
		OrganizationID: attackerOrgID, InvitationID: invitationID,
		Email: "victim@corp-a.example", Grants: []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
		InvitedByMembershipID: attackerMembershipID, TokenHash: tokenHashOf("secret"),
		ExpiresAt: now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	accept := application.AcceptInvitationInput{
		InvitationID: invitationID, TokenHash: tokenHashOf("secret"),
		AssertedEmail: "victim@corp-a.example",
		NewUserID:     domain.ID(uuid.NewString()), DisplayName: "Victim",
		ProviderKey: "polis", Issuer: "https://auth.example/tenants/" + string(attackerOrgID), Subject: "attacker-idp-subject",
		MembershipID: domain.ID(uuid.NewString()), SeatQuota: application.UnlimitedQuota(), Now: now,
	}
	if _, err := store.AcceptInvitation(ctx, accept); !errors.Is(err, domain.ErrUserExists) {
		t.Fatalf("attaching an existing account by email alone: err = %v, want ErrUserExists", err)
	}
	if _, err := store.ResolveExternalIdentity(ctx, application.ResolveExternalIdentityInput{
		ProviderKey: "polis", Issuer: accept.Issuer, Subject: accept.Subject, Now: now,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("refused acceptance must not link an identity: %v", err)
	}
	accept.LinkExistingUser = true
	principal, err := store.AcceptInvitation(ctx, accept)
	if err != nil {
		t.Fatalf("vouched acceptance: %v", err)
	}
	if principal.ActorID != victimID {
		t.Fatalf("vouched acceptance attached %s, want the existing user %s", principal.ActorID, victimID)
	}
}
