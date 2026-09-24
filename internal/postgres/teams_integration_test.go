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

func admitTeamTestMember(t *testing.T, ctx context.Context, store *Store, orgID, ownerMembershipID domain.ID, email, secret string, now time.Time) domain.ID {
	t.Helper()
	invitationID := domain.ID(uuid.NewString())
	if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
		OrganizationID: orgID, InvitationID: invitationID,
		Email: email, Grants: []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
		InvitedByMembershipID: ownerMembershipID,
		TokenHash:             tokenHashOf(secret),
		ExpiresAt:             now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("CreateInvitation %s: %v", email, err)
	}
	userID := domain.ID(uuid.NewString())
	membershipID := domain.ID(uuid.NewString())
	if _, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{
		InvitationID: invitationID, TokenHash: tokenHashOf(secret),
		AssertedEmail: email, NewUserID: userID, DisplayName: email,
		PasswordHash: "argon2id$member", ProviderKey: "local",
		Issuer: "withhuman:self-hosted", Subject: string(userID),
		MembershipID: membershipID, SeatQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("AcceptInvitation %s: %v", email, err)
	}
	return membershipID
}

func teamTargetPathDocument(teamID domain.ID) json.RawMessage {
	return json.RawMessage(`{"working_hours":[],"nodes":[{"id":"` + uuid.NewString() +
		`","type":"level","targets":[{"type":"team","id":"` + string(teamID) + `"}],"escalate_after":"15m"}]}`)
}

func teamTestDecision(t *testing.T, ctx context.Context, store *Store, orgID, requestID, membershipID domain.ID, idempotencyKey string, now time.Time) domain.DecisionOperation {
	t.Helper()
	return teamTestDecisionWith(t, ctx, store, orgID, requestID, membershipID, idempotencyKey, now, false)
}

// teamTestDecisionWith applies an approval as one membership; outsideRouting
// is the break-glass acknowledgement a decision outside routing needs.
func teamTestDecisionWith(t *testing.T, ctx context.Context, store *Store, orgID, requestID, membershipID domain.ID, idempotencyKey string, now time.Time, outsideRouting bool) domain.DecisionOperation {
	t.Helper()
	var userID string
	if err := store.Pool().QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE id = $1`,
		membershipID).Scan(&userID); err != nil {
		t.Fatalf("resolve decision user: %v", err)
	}
	operation, err := store.CreateDecisionOperation(ctx, application.CreateDecisionOperationInput{
		Operation: domain.DecisionOperation{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, RequestID: requestID,
			Status: domain.OperationPending, CreatedAt: now,
		},
		IdempotencyKey: idempotencyKey, IntentHash: "approve-" + idempotencyKey,
	})
	if err != nil {
		t.Fatalf("CreateDecisionOperation: %v", err)
	}
	operation, err = store.ApplyDecision(ctx, application.ApplyDecisionInput{
		Command: domain.DecisionCommand{
			OperationID: operation.ID, RequestID: requestID, OrganizationID: orgID,
			Principal: domain.Principal{
				ActorType: domain.ActorHuman, ActorID: domain.ID(userID), OrganizationID: orgID,
				MembershipID: membershipID,
				Auth: domain.AuthContext{
					Method: "local_password", Assurance: "single_factor", AuthenticatedAt: now,
					SessionID: domain.ID(uuid.NewString()),
				},
			},
			Status: domain.ApprovalApproved, Channel: "web", IdempotencyKey: idempotencyKey,
			OutsideRouting: outsideRouting,
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("ApplyDecision: %v", err)
	}
	return operation
}

// Exercises manual team management, its coexistence with directory sync,
// the archive guards, and routed-targets decide authorization against a
// real PostgreSQL with the production migrations and triggers.
func TestTeamsAgainstRealPostgres(t *testing.T) {
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
	memberMembershipID := admitTeamTestMember(t, ctx, store, orgID, ownerMembershipID, "member@example.com", "member-secret", now)
	outsiderMembershipID := admitTeamTestMember(t, ctx, store, orgID, ownerMembershipID, "outsider@example.com", "outsider-secret", now)

	team, err := store.CreateTeam(ctx, application.CreateTeamInput{
		OrganizationID: orgID, Name: "payments", ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.CreateTeam(ctx, application.CreateTeamInput{
		OrganizationID: orgID, Name: "payments", ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate team name error = %v", err)
	}

	// Manual member replacement is set-shaped and validates the org scope.
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID,
		MembershipIDs:     []domain.ID{domain.ID(uuid.NewString())},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown membership error = %v", err)
	}
	members, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID,
		MembershipIDs:     []domain.ID{memberMembershipID, ownerMembershipID},
		ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil || len(members) != 2 {
		t.Fatalf("SetTeamManualMembers = %d members, %v", len(members), err)
	}
	members, err = store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID,
		MembershipIDs:     []domain.ID{memberMembershipID},
		ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil || len(members) != 1 || members[0].MembershipID != memberMembershipID {
		t.Fatalf("manual set replacement = %#v, %v", members, err)
	}

	// The members list surfaces team assignments.
	memberRecords, err := store.ListMembers(ctx, application.ListMembersInput{OrganizationID: orgID})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	for _, record := range memberRecords {
		if record.Membership.ID == memberMembershipID && len(record.Membership.TeamIDs) != 1 {
			t.Fatalf("member team_ids = %#v", record.Membership.TeamIDs)
		}
	}

	renamed, err := store.RenameTeam(ctx, application.RenameTeamInput{
		OrganizationID: orgID, TeamID: team.ID, Name: "payments-eu",
		ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil || renamed.Name != "payments-eu" {
		t.Fatalf("RenameTeam = %#v, %v", renamed, err)
	}

	// Target resolution: real team and active member resolve, ghosts miss.
	resolved, err := store.ResolveRoutingTargets(ctx, application.ResolveRoutingTargetsInput{
		OrganizationID:    orgID,
		UserMembershipIDs: []domain.ID{ownerMembershipID, domain.ID(uuid.NewString())},
		TeamIDs:           []domain.ID{team.ID, domain.ID(uuid.NewString())},
	})
	if err != nil || len(resolved.MissingUsers) != 1 || len(resolved.MissingTeams) != 1 {
		t.Fatalf("ResolveRoutingTargets = %#v, %v", resolved, err)
	}

	// Directory adoption: a group with a matching name takes over the team,
	// locking manual rename and archive while manual member rows survive.
	directoryNow := now.Add(time.Minute)
	groupData, _ := json.Marshal(map[string]any{"name": "payments-eu"})
	memberEventData, _ := json.Marshal(map[string]any{
		"member_external_id": "scim-user-1", "member_email": "outsider@example.com", "operation": "add",
	})
	if err := store.ApplyDirectoryCommands(ctx, application.ApplyDirectoryCommandsInput{
		OrganizationID: orgID, AdapterKey: "polis", ConnectionExternalID: "dir-1",
		Commands: []application.DirectoryCommandRecord{
			{Type: "upsert_group", ExternalID: "group-1", Data: groupData},
			{Type: "replace_group_membership", ExternalID: "group-1", Data: memberEventData},
		},
		SeatQuota: application.UnlimitedQuota(), Now: directoryNow,
	}); err != nil {
		t.Fatalf("apply group event: %v", err)
	}
	adopted, adoptedMembers, err := store.GetTeam(ctx, application.GetTeamInput{OrganizationID: orgID, TeamID: team.ID})
	if err != nil || !adopted.DirectoryManaged {
		t.Fatalf("adopted team = %#v, %v", adopted, err)
	}
	if len(adoptedMembers) != 2 {
		t.Fatalf("adopted team members = %#v", adoptedMembers)
	}
	if _, err := store.RenameTeam(ctx, application.RenameTeamInput{
		OrganizationID: orgID, TeamID: team.ID, Name: "renamed",
		ActorMembershipID: ownerMembershipID, Now: directoryNow,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("directory-managed rename error = %v", err)
	}
	if err := store.ArchiveTeam(ctx, application.ArchiveTeamInput{
		OrganizationID: orgID, TeamID: team.ID,
		ActorMembershipID: ownerMembershipID, Now: directoryNow,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("directory-managed archive error = %v", err)
	}

	// Group removal keeps a team with manual assignments and returns it to
	// manual control.
	if err := store.ApplyDirectoryCommands(ctx, application.ApplyDirectoryCommandsInput{
		OrganizationID: orgID, AdapterKey: "polis", ConnectionExternalID: "dir-1",
		Commands:  []application.DirectoryCommandRecord{{Type: "remove_group", ExternalID: "group-1", Data: []byte(`{}`)}},
		SeatQuota: application.UnlimitedQuota(), Now: directoryNow,
	}); err != nil {
		t.Fatalf("apply group removal: %v", err)
	}
	retained, retainedMembers, err := store.GetTeam(ctx, application.GetTeamInput{OrganizationID: orgID, TeamID: team.ID})
	if err != nil || retained.DirectoryManaged {
		t.Fatalf("retained team = %#v, %v", retained, err)
	}
	if len(retainedMembers) != 1 || retainedMembers[0].MembershipID != memberMembershipID {
		t.Fatalf("retained members = %#v", retainedMembers)
	}

	// Routing pins the team as a target; archive is now guarded and decide
	// authorization follows the routed targets.
	revision, err := store.CreateEscalationPath(ctx, application.CreateEscalationPathInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PathKey: "payments",
		Name: "Payments escalation path", Document: teamTargetPathDocument(team.ID), DocumentVersion: 2,
		CreatedByActorID: ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("create path revision: %v", err)
	}
	if _, err := store.ActivateEscalationPathRevision(ctx, application.ActivateEscalationPathRevisionInput{
		OrganizationID: orgID, PathKey: "payments", Revision: revision.Revision,
		ActorID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("activate path revision: %v", err)
	}
	if err := store.ArchiveTeam(ctx, application.ArchiveTeamInput{
		OrganizationID: orgID, TeamID: team.ID,
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("archive of actively targeted team error = %v", err)
	}

	var ownerUserID string
	if err := store.Pool().QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE id = $1`,
		ownerMembershipID).Scan(&ownerUserID); err != nil {
		t.Fatalf("resolve owner user: %v", err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "teams-agent",
		AgentSlug: "billing", AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	secret := sha256.Sum256([]byte("teams-secret"))
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
		OrganizationID: orgID, AgentID: agent.ID, ActorID: domain.ID(ownerUserID), Name: "teams-instance",
		Metadata: json.RawMessage(`{}`),
		Credential: application.AgentCredentialRecord{
			ID: domain.ID(uuid.NewString()), SecretHash: secret[:],
			IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgentInstanceWithCredential: %v", err)
	}
	pinRequest := func(key string) domain.ApprovalRequest {
		request := domain.ApprovalRequest{
			Timeout: "1h",
			ID:      domain.ID(uuid.NewString()), OrganizationID: orgID,
			AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
			Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount":1}`),
			IdempotencyKey: key, SemanticHash: "hash-" + key,
			Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour),
			CreatedAt: now, UpdatedAt: now,
		}
		initialization := pendingApprovalInitialization(request)
		initialization.Routing = &domain.ApprovalRequestRouting{
			RequestID: request.ID, OrganizationID: orgID,
			Outcome: domain.RoutingEscalated, Reason: domain.RoutingReasonPipelineDefault,
			EscalationPathKey: "payments", EscalationPathRevisionID: revision.ID,
			Urgency: "interrupt", CreatedAt: now,
		}
		if _, err := store.InitializeApproval(ctx, initialization); err != nil {
			t.Fatalf("InitializeApproval %s: %v", key, err)
		}
		return request
	}

	firstRequest := pinRequest("teams-req-1")
	recipients, err := store.ResolveApprovalRecipients(ctx, application.ResolveApprovalRecipientsInput{
		OrganizationID: orgID, RequestID: firstRequest.ID,
	})
	if err != nil || recipients.Outcome != domain.RoutingEscalated {
		t.Fatalf("ResolveApprovalRecipients = %#v, %v", recipients, err)
	}
	if len(recipients.Recipients) != 1 || recipients.Recipients[0].MembershipID != memberMembershipID {
		t.Fatalf("routed recipients = %#v", recipients.Recipients)
	}
	if !slices.Contains(recipients.Recipients[0].Emails, "member@example.com") {
		t.Fatalf("routed recipient carries no contact address: %#v", recipients.Recipients[0])
	}

	// A request that never matched a routing rule still has to reach
	// somebody: the default queue resolves to every active member who may
	// decide, so email works before anyone configures routing.
	unrouted := domain.ApprovalRequest{
		Timeout: "1h",
		ID:      domain.ID(uuid.NewString()), OrganizationID: orgID,
		AgentID: agent.ID, AgentInstanceID: instance.ID, AgentSlug: agent.AgentSlug,
		Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount":1}`),
		IdempotencyKey: "teams-unrouted", SemanticHash: "hash-unrouted",
		Status: domain.ApprovalPending, DeadlineAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := store.InitializeApproval(ctx, pendingApprovalInitialization(unrouted)); err != nil {
		t.Fatalf("InitializeApproval unrouted: %v", err)
	}
	fallback, err := store.ResolveApprovalRecipients(ctx, application.ResolveApprovalRecipientsInput{
		OrganizationID: orgID, RequestID: unrouted.ID,
	})
	if err != nil || fallback.Outcome != domain.RoutingDefaultQueue {
		t.Fatalf("unrouted recipients = %#v, %v", fallback, err)
	}
	fallbackIDs := fallback.MembershipIDs()
	for _, expected := range []domain.ID{ownerMembershipID, memberMembershipID, outsiderMembershipID} {
		if !slices.Contains(fallbackIDs, expected) {
			t.Fatalf("default queue skipped an eligible reviewer %s: %#v", expected, fallbackIDs)
		}
	}
	for _, recipient := range fallback.Recipients {
		if !recipient.Reachable() {
			t.Fatalf("fallback recipient carries no address: %#v", recipient)
		}
	}

	// An untargeted reviewer is rejected; a team member decides; an owner
	// breaks glass through request.decide.unrouted, but only after
	// acknowledging the decision is outside routing.
	rejected := teamTestDecision(t, ctx, store, orgID, firstRequest.ID, outsiderMembershipID, "teams-decision-1", now)
	if rejected.Status != domain.OperationRejected || rejected.ErrorCode != "not_targeted" {
		t.Fatalf("untargeted decision = %#v", rejected)
	}
	applied := teamTestDecision(t, ctx, store, orgID, firstRequest.ID, memberMembershipID, "teams-decision-2", now)
	if applied.Status != domain.OperationApplied {
		t.Fatalf("team member decision = %#v", applied)
	}
	secondRequest := pinRequest("teams-req-2")
	if unacknowledged := teamTestDecision(t, ctx, store, orgID, secondRequest.ID, ownerMembershipID, "teams-decision-3-plain", now); unacknowledged.Status != domain.OperationRejected || unacknowledged.ErrorCode != "outside_routing_required" {
		t.Fatalf("unacknowledged owner decision = %#v", unacknowledged)
	}
	ownerApplied := teamTestDecisionWith(t, ctx, store, orgID, secondRequest.ID, ownerMembershipID, "teams-decision-3", now, true)
	if ownerApplied.Status != domain.OperationApplied {
		t.Fatalf("owner bypass decision = %#v", ownerApplied)
	}

	// A pending request's pinned revision guards the team even after the
	// path is archived; expiry releases it.
	thirdRequest := pinRequest("teams-req-3")
	if _, err := store.ArchiveEscalationPath(ctx, application.ArchiveEscalationPathInput{
		OrganizationID: orgID, PathKey: "payments", ActorID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("archive path: %v", err)
	}
	if err := store.ArchiveTeam(ctx, application.ArchiveTeamInput{
		OrganizationID: orgID, TeamID: team.ID,
		ActorMembershipID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("archive with pending pinned request error = %v", err)
	}
	if _, err := store.ExpireApproval(ctx, application.ExpireApprovalInput{
		OrganizationID: orgID, RequestID: thirdRequest.ID, Now: now.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("expire request: %v", err)
	}

	if err := store.ArchiveTeam(ctx, application.ArchiveTeamInput{
		OrganizationID: orgID, TeamID: team.ID,
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("final archive: %v", err)
	}
	if archived, _, err := store.GetTeam(ctx, application.GetTeamInput{OrganizationID: orgID, TeamID: team.ID}); err != nil || archived.ArchivedAt == nil {
		t.Fatalf("archived team lookup = %+v, %v", archived, err)
	}
}

// Exercises the team escalation policy guards: the member removal refusal
// and its force, the status fields, and the archiving on archive.
func TestTeamEscalationPolicyGuardsAgainstRealPostgres(t *testing.T) {
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
	orgID, owner := insertPipelineTestTenant(t, ctx, store, "team-guards", now)
	alice := insertEscalationTestMember(t, ctx, store, orgID, "guard-alice", now)
	bob := insertEscalationTestMember(t, ctx, store, orgID, "guard-bob", now)
	team, err := store.CreateTeam(ctx, application.CreateTeamInput{
		OrganizationID: orgID, Name: "payments", ActorMembershipID: owner, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{alice, bob},
		ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatalf("SetTeamManualMembers: %v", err)
	}

	// No policy yet: both status fields are absent.
	fetched, _, err := store.GetTeam(ctx, application.GetTeamInput{OrganizationID: orgID, TeamID: team.ID})
	if err != nil || fetched.EscalationPolicy.ActiveRevision != nil || fetched.EscalationPolicy.LatestRevision != nil {
		t.Fatalf("team without a policy = %+v, %v", fetched.EscalationPolicy, err)
	}
	policy := createTeamPolicyRevision(t, ctx, store, orgID, team.ID, owner, teamPolicyDocument(false, alice), now)
	fetched, _, err = store.GetTeam(ctx, application.GetTeamInput{OrganizationID: orgID, TeamID: team.ID})
	if err != nil || fetched.EscalationPolicy.ActiveRevision != nil || fetched.EscalationPolicy.LatestRevision == nil || *fetched.EscalationPolicy.LatestRevision != 1 {
		t.Fatalf("team with an inactive policy = %+v, %v", fetched.EscalationPolicy, err)
	}
	// An inactive policy guards nothing.
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{bob},
		ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatalf("removal under an inactive policy: %v", err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{alice, bob},
		ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatalf("restore members: %v", err)
	}
	activateTeamPolicyRevision(t, ctx, store, orgID, team.ID, owner, policy.Revision, nil, now)
	listed, err := store.ListTeams(ctx, application.ListTeamsInput{OrganizationID: orgID, Scope: domain.ScopeSet{All: true}})
	if err != nil || len(listed) != 1 || listed[0].EscalationPolicy.ActiveRevision == nil || *listed[0].EscalationPolicy.ActiveRevision != 1 ||
		listed[0].EscalationPolicy.LatestRevision == nil || *listed[0].EscalationPolicy.LatestRevision != 1 {
		t.Fatalf("listed team status = %+v, %v", listed, err)
	}

	// Removing Bob is fine: the policy does not name him. Removing Alice
	// is refused until forced.
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{alice},
		ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatalf("removal of an unnamed member: %v", err)
	}
	var inPolicy domain.TeamMemberInEscalationPolicyError
	_, err = store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{bob},
		ActorMembershipID: owner, Now: now,
	})
	if !errors.As(err, &inPolicy) || !errors.Is(err, domain.ErrTeamMemberInEscalationPolicy) ||
		inPolicy.TeamID != team.ID || inPolicy.Revision != 1 || !slices.Equal(inPolicy.MembershipIDs, []domain.ID{alice}) {
		t.Fatalf("removal of a named member = %v (%+v)", err, inPolicy)
	}
	_, members, err := store.GetTeam(ctx, application.GetTeamInput{OrganizationID: orgID, TeamID: team.ID})
	if err != nil || len(members) != 1 || members[0].MembershipID != alice {
		t.Fatalf("members after refused removal = %+v, %v", members, err)
	}
	// A directory-sourced row keeps Alice a member, so the manual removal
	// strands nobody.
	if _, err := store.Pool().Exec(ctx, `INSERT INTO membership_teams (membership_id, team_id, organization_id, source, source_key)
		VALUES ($1, $2, $3, 'directory', 'group-1')`, alice, team.ID, orgID); err != nil {
		t.Fatalf("insert directory row: %v", err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{bob},
		ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatalf("removal with a directory row present: %v", err)
	}
	if _, err := store.Pool().Exec(ctx, `DELETE FROM membership_teams WHERE membership_id = $1 AND team_id = $2 AND source = 'directory'`,
		alice, team.ID); err != nil {
		t.Fatalf("delete directory row: %v", err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{alice},
		ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatalf("restore alice: %v", err)
	}
	forcedMembers, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{},
		ActorMembershipID: owner, Force: true, Now: now,
	})
	if err != nil || len(forcedMembers) != 0 {
		t.Fatalf("forced removal = %+v, %v", forcedMembers, err)
	}
	_, changedData := countAuditEvents(t, ctx, store, orgID, "team.members_changed")
	if changedData["forced"] != true || changedData["escalation_policy_revision"] != float64(1) {
		t.Fatalf("forced removal audit data = %v", changedData)
	}

	// Archiving the team deactivates its policy and audits why.
	if err := store.ArchiveTeam(ctx, application.ArchiveTeamInput{
		OrganizationID: orgID, TeamID: team.ID, ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatalf("ArchiveTeam: %v", err)
	}
	var isActive bool
	if err := store.Pool().QueryRow(ctx, `SELECT is_active FROM team_escalation_policies WHERE id = $1`, policy.ID).Scan(&isActive); err != nil {
		t.Fatalf("read policy after archive: %v", err)
	}
	if isActive {
		t.Fatal("archived team's policy is still active")
	}
	count, deactivatedData := countAuditEvents(t, ctx, store, orgID, "team_escalation_policy.deactivated")
	if count != 1 || deactivatedData["team_id"] != string(team.ID) || deactivatedData["revision"] != float64(1) || deactivatedData["reason"] != "team_archived" {
		t.Fatalf("deactivated audit = %d, %v", count, deactivatedData)
	}
	if _, err := store.GetActiveTeamEscalationPolicies(ctx, application.GetActiveTeamEscalationPoliciesInput{
		OrganizationID: orgID, TeamIDs: []domain.ID{team.ID},
	}); err != nil {
		t.Fatalf("active policies after archive: %v", err)
	}
}

// Archiving preserves the record while removing every way to use the team
// for access or routing. Repeated requests must not rewrite its history.
func TestTeamArchiveLifecycleAgainstRealPostgres(t *testing.T) {
	server := embeddedpgtest.Start(t)
	ctx := context.Background()
	store, err := Open(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatal(err)
	}
	recordAudit(t, store)
	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID, owner := insertPipelineTestTenant(t, ctx, store, "team-archive", now)
	member := insertEscalationTestMember(t, ctx, store, orgID, "archive-member", now)
	team, err := store.CreateTeam(ctx, application.CreateTeamInput{OrganizationID: orgID, Name: "Archive me", ActorMembershipID: owner, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{member}, ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	policy := createTeamPolicyRevision(t, ctx, store, orgID, team.ID, owner, teamPolicyDocument(false, member), now)
	activateTeamPolicyRevision(t, ctx, store, orgID, team.ID, owner, policy.Revision, nil, now)
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{
		OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: team.ID,
		Assignments:       []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
		ActorMembershipID: owner, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	input := application.ArchiveTeamInput{OrganizationID: orgID, TeamID: team.ID, ActorMembershipID: owner, Now: now}
	foreign := input
	foreign.OrganizationID = domain.ID(uuid.NewString())
	if err := store.ArchiveTeam(ctx, foreign); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-organization archive = %v", err)
	}
	if err := store.ArchiveTeam(ctx, input); err != nil {
		t.Fatal(err)
	}
	input.Now = now.Add(time.Hour)
	if err := store.ArchiveTeam(ctx, input); err != nil {
		t.Fatalf("repeat archive: %v", err)
	}
	archived, members, err := store.GetTeam(ctx, application.GetTeamInput{OrganizationID: orgID, TeamID: team.ID})
	if err != nil || archived.ArchivedAt == nil || !archived.ArchivedAt.Equal(now) || archived.Name != team.Name || len(members) != 1 || members[0].MembershipID != member || len(archived.Assignments) != 0 {
		t.Fatalf("archived record = %+v, roster = %+v, error = %v", archived, members, err)
	}
	if count, _ := countAuditEvents(t, ctx, store, orgID, "team.archived"); count != 1 {
		t.Fatalf("archive audit count = %d", count)
	}
	for _, status := range []application.TeamListStatus{"", application.TeamListStatusLive, application.TeamListStatusArchived, application.TeamListStatusAll} {
		listed, err := store.ListTeams(ctx, application.ListTeamsInput{OrganizationID: orgID, Scope: domain.ScopeSet{All: true}, Status: status})
		want := 0
		if status == application.TeamListStatusArchived || status == application.TeamListStatusAll {
			want = 1
		}
		if err != nil || len(listed) != want {
			t.Fatalf("list %q = %+v, %v", status, listed, err)
		}
	}
	if _, err := store.ListTeams(ctx, application.ListTeamsInput{OrganizationID: orgID, Scope: domain.ScopeSet{All: true}, Status: "invalid"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid status = %v", err)
	}
	if ids, err := membershipTeamIDs(ctx, store.Pool(), orgID, member); err != nil || len(ids) != 0 {
		t.Fatalf("live membership teams = %v, %v", ids, err)
	}
	resolved, err := store.ResolveRoutingTargets(ctx, application.ResolveRoutingTargetsInput{OrganizationID: orgID, TeamIDs: []domain.ID{team.ID}})
	if err != nil || !slices.Equal(resolved.MissingTeams, []domain.ID{team.ID}) {
		t.Fatalf("archived routing target = %+v, %v", resolved, err)
	}
	if _, err := store.RenameTeam(ctx, application.RenameTeamInput{OrganizationID: orgID, TeamID: team.ID, Name: "Changed", ActorMembershipID: owner, Now: now}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("rename archived = %v", err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{OrganizationID: orgID, TeamID: team.ID, ActorMembershipID: owner, Now: now}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("edit archived roster = %v", err)
	}
	if _, err := store.ReplaceAssignments(ctx, application.ReplaceAssignmentsInput{OrganizationID: orgID, PrincipalKind: domain.PrincipalTeam, PrincipalID: team.ID, ActorMembershipID: owner, Now: now}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("edit archived grants = %v", err)
	}
	if _, err := store.CreateTeamEscalationPolicyRevision(ctx, application.CreateTeamEscalationPolicyRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, TeamID: team.ID, Document: policy.Document, DocumentVersion: policy.DocumentVersion, CreatedByActorID: owner, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("create archived policy = %v", err)
	}
	if _, err := store.ActivateTeamEscalationPolicyRevision(ctx, application.ActivateTeamEscalationPolicyRevisionInput{
		OrganizationID: orgID, TeamID: team.ID, Revision: policy.Revision, ActorID: owner, Now: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("activate archived policy = %v", err)
	}
	if _, err := store.GetTeamEscalationPolicyRevision(ctx, application.GetTeamEscalationPolicyRevisionInput{OrganizationID: orgID, TeamID: team.ID, Revision: policy.Revision}); err != nil {
		t.Fatalf("read archived policy: %v", err)
	}
	replacement, err := store.CreateTeam(ctx, application.CreateTeamInput{OrganizationID: orgID, Name: team.Name, ActorMembershipID: owner, Now: now})
	if err != nil || replacement.ID == team.ID || replacement.ArchivedAt != nil {
		t.Fatalf("reuse archived name = %+v, %v", replacement, err)
	}
}
