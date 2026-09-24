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
	"github.com/withHumanAI/withHuman/routing"
)

// Approval policy knobs against real PostgreSQL (design doc 10, phase 4):
// the policy update and its audit, the handoff that adds a reviewer to a
// routed request, break-glass decisions and their audit event, the
// routed_only visibility filter, and the break_glass_only policy for
// unrouted requests.
func TestApprovalPolicyKnobsAgainstRealPostgres(t *testing.T) {
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
		OrganizationID: orgID, OrganizationSlug: "knobs", OrganizationName: "Knobs",
		UserID: ownerID, MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	admit := func(email string, roleID domain.ID) domain.ID {
		t.Helper()
		invitationID := domain.ID(uuid.NewString())
		hash := tokenHashOf("invite-" + email)
		if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
			OrganizationID: orgID, InvitationID: invitationID, Email: email,
			Grants:                []domain.AssignmentSpec{{RoleID: roleID, ScopeKind: domain.ScopeOrganization}},
			InvitedByMembershipID: ownerMembershipID, TokenHash: hash, ExpiresAt: now.Add(time.Hour),
			SeatQuota: application.UnlimitedQuota(), Now: now,
		}); err != nil {
			t.Fatalf("CreateInvitation(%s): %v", email, err)
		}
		membershipID := domain.ID(uuid.NewString())
		if _, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{
			InvitationID: invitationID, TokenHash: hash, AssertedEmail: email,
			NewUserID: domain.ID(uuid.NewString()), DisplayName: email, PasswordHash: "argon2id$test",
			ProviderKey: "local", Issuer: "withhuman:self-hosted", MembershipID: membershipID,
			SeatQuota: application.UnlimitedQuota(), Now: now,
		}); err != nil {
			t.Fatalf("AcceptInvitation(%s): %v", email, err)
		}
		return membershipID
	}
	routedMembershipID := admit("routed@example.com", domain.RoleReviewerID)
	colleagueMembershipID := admit("colleague@example.com", domain.RoleReviewerID)
	strangerMembershipID := admit("stranger@example.com", domain.RoleReviewerID)
	auditorMembershipID := admit("auditor@example.com", domain.RoleAuditorID)

	team, err := store.CreateTeam(ctx, application.CreateTeamInput{
		OrganizationID: orgID, Name: "payments", ActorMembershipID: ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: orgID, TeamID: team.ID, MembershipIDs: []domain.ID{routedMembershipID},
		ActorMembershipID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("SetTeamManualMembers: %v", err)
	}
	revision, err := store.CreateEscalationPath(ctx, application.CreateEscalationPathInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PathKey: "payments",
		Name: "Payments escalation path", Document: teamTargetPathDocument(team.ID), DocumentVersion: 2,
		CreatedByActorID: ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("create path revision: %v", err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "knobs-agent",
		AgentSlug: "billing", AgentQuota: application.UnlimitedQuota(), Now: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	secret := sha256.Sum256([]byte("knobs-secret"))
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{ActorType: domain.ActorHuman,
		OrganizationID: orgID, AgentID: agent.ID, ActorID: ownerID, Name: "knobs-instance",
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
	pinRequest := func(key string, routed bool) domain.ApprovalRequest {
		t.Helper()
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
		if routed {
			initialization.Routing = &domain.ApprovalRequestRouting{
				RequestID: request.ID, OrganizationID: orgID,
				Outcome: domain.RoutingEscalated, Reason: domain.RoutingReasonPipelineDefault,
				EscalationPathKey: "payments", EscalationPathRevisionID: revision.ID,
				Urgency: "interrupt", CreatedAt: now,
			}
		}
		if _, err := store.InitializeApproval(ctx, initialization); err != nil {
			t.Fatalf("InitializeApproval %s: %v", key, err)
		}
		return request
	}
	principalOf := func(membershipID domain.ID) domain.Principal {
		t.Helper()
		grants, err := store.LoadGrants(ctx, application.LoadGrantsInput{
			OrganizationID: orgID, PrincipalKind: domain.PrincipalMembership, PrincipalID: membershipID,
		})
		if err != nil {
			t.Fatalf("LoadGrants(%s): %v", membershipID, err)
		}
		var userID string
		if err := store.Pool().QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE id = $1`, membershipID).Scan(&userID); err != nil {
			t.Fatalf("resolve user for %s: %v", membershipID, err)
		}
		return domain.Principal{
			ActorType: domain.ActorHuman, ActorID: domain.ID(userID), OrganizationID: orgID, MembershipID: membershipID,
		}.WithGrants(grants)
	}
	addReviewer := func(actor, member, requestID domain.ID) (domain.ApprovalReviewer, error) {
		t.Helper()
		return store.AddApprovalReviewer(ctx, application.AddApprovalReviewerInput{
			OrganizationID: orgID, RequestID: requestID, MembershipID: member, Actor: principalOf(actor), Now: now,
		})
	}
	routingState := func(membershipID, requestID domain.ID) application.ApprovalRoutingState {
		t.Helper()
		state, err := store.GetApprovalRoutingState(ctx, application.GetApprovalRoutingStateInput{
			OrganizationID: orgID, RequestID: requestID, MembershipID: membershipID,
		})
		if err != nil {
			t.Fatalf("GetApprovalRoutingState: %v", err)
		}
		return state
	}
	eventData := func(eventType string, subjectID domain.ID) map[string]any {
		t.Helper()
		var raw []byte
		if err := store.Pool().QueryRow(ctx, `SELECT data FROM test_audit_events
			WHERE organization_id = $1 AND event_type = $2 AND subject_id = $3
			ORDER BY occurred_at DESC, sequence DESC LIMIT 1`, orgID, eventType, subjectID).Scan(&raw); err != nil {
			t.Fatalf("read %s event: %v", eventType, err)
		}
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatalf("decode %s event: %v", eventType, err)
		}
		return data
	}
	setPolicy := func(unrouted domain.UnroutedRequestsPolicy, visibility domain.RequestVisibilityPolicy) domain.OrganizationPolicy {
		t.Helper()
		current, err := store.GetOrganizationPolicy(ctx, application.GetOrganizationPolicyInput{OrganizationID: orgID})
		if err != nil {
			t.Fatalf("GetOrganizationPolicy: %v", err)
		}
		updated, err := store.UpdateOrganizationPolicy(ctx, application.UpdateOrganizationPolicyInput{
			OrganizationID: orgID, AllowSelfApproval: current.AllowSelfApproval, MFAMode: current.MFAMode,
			ApprovalReauthWindow: current.ApprovalReauthWindow, ClaimRetriggerWindow: current.ClaimRetriggerWindow,
			UnroutedRequests: unrouted, RequestVisibility: visibility,
			ActorMembershipID: ownerMembershipID, Now: now,
		})
		if err != nil {
			t.Fatalf("UpdateOrganizationPolicy: %v", err)
		}
		return updated
	}

	t.Run("policy update round-trips and audits", func(t *testing.T) {
		defaults, err := store.GetOrganizationPolicy(ctx, application.GetOrganizationPolicyInput{OrganizationID: orgID})
		if err != nil {
			t.Fatalf("GetOrganizationPolicy: %v", err)
		}
		if defaults.UnroutedRequests != domain.UnroutedAnyReviewer || defaults.RequestVisibility != domain.VisibilityAllReaders {
			t.Fatalf("default knobs = %+v", defaults)
		}
		updated, err := store.UpdateOrganizationPolicy(ctx, application.UpdateOrganizationPolicyInput{
			OrganizationID: orgID, AllowSelfApproval: true, MFAMode: domain.MFAReviewersRequired,
			ApprovalReauthWindow: 30 * time.Minute, ClaimRetriggerWindow: 10 * time.Minute,
			UnroutedRequests: domain.UnroutedBreakGlassOnly, RequestVisibility: domain.VisibilityRoutedOnly,
			ActorMembershipID: ownerMembershipID, Now: now,
		})
		if err != nil {
			t.Fatalf("UpdateOrganizationPolicy: %v", err)
		}
		read, err := store.GetOrganizationPolicy(ctx, application.GetOrganizationPolicyInput{OrganizationID: orgID})
		if err != nil || read != updated {
			t.Fatalf("read back = %+v, %v; updated = %+v", read, err, updated)
		}
		if !read.AllowSelfApproval || read.MFAMode != domain.MFAReviewersRequired || read.ApprovalReauthWindow != 30*time.Minute ||
			read.ClaimRetriggerWindow != 10*time.Minute || read.UnroutedRequests != domain.UnroutedBreakGlassOnly ||
			read.RequestVisibility != domain.VisibilityRoutedOnly || read.SessionMaxAge != defaults.SessionMaxAge ||
			read.AdmissionMode != defaults.AdmissionMode {
			t.Fatalf("updated policy = %+v", read)
		}
		if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events
			WHERE organization_id = $1 AND event_type = 'organization.policy_updated'
			  AND subject_type = 'organization' AND subject_id = $1 AND actor_id = $2`, orgID, ownerMembershipID); count != 1 {
			t.Fatalf("organization.policy_updated events = %d", count)
		}
		data := eventData("organization.policy_updated", orgID)
		before, after := data["before"].(map[string]any), data["after"].(map[string]any)
		if before["unrouted_requests"] != "any_reviewer" || after["unrouted_requests"] != "break_glass_only" ||
			before["request_visibility"] != "all_readers" || after["request_visibility"] != "routed_only" ||
			before["mfa_mode"] != "optional" || after["mfa_mode"] != "reviewers_required" ||
			before["allow_self_approval"] != false || after["allow_self_approval"] != true ||
			after["approval_reauth_window"] != "30m0s" || after["claim_retrigger_window"] != "10m0s" {
			t.Fatalf("audit data = %v", data)
		}
		if _, err := store.UpdateOrganizationPolicy(ctx, application.UpdateOrganizationPolicyInput{
			OrganizationID: domain.ID(uuid.NewString()), MFAMode: domain.MFAOptional,
			ApprovalReauthWindow: time.Hour, ClaimRetriggerWindow: time.Hour,
			UnroutedRequests: domain.UnroutedAnyReviewer, RequestVisibility: domain.VisibilityAllReaders,
			ActorMembershipID: ownerMembershipID, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown organization error = %v", err)
		}
		// Restore the defaults so the later subtests start from them.
		if _, err := store.UpdateOrganizationPolicy(ctx, application.UpdateOrganizationPolicyInput{
			OrganizationID: orgID, AllowSelfApproval: defaults.AllowSelfApproval, MFAMode: defaults.MFAMode,
			ApprovalReauthWindow: defaults.ApprovalReauthWindow, ClaimRetriggerWindow: defaults.ClaimRetriggerWindow,
			UnroutedRequests: defaults.UnroutedRequests, RequestVisibility: defaults.RequestVisibility,
			ActorMembershipID: ownerMembershipID, Now: now,
		}); err != nil {
			t.Fatalf("restore policy: %v", err)
		}
	})

	t.Run("an added reviewer may claim and decide, a stranger stays untargeted", func(t *testing.T) {
		request := pinRequest("knobs-handoff-1", true)
		for index, membershipID := range []domain.ID{strangerMembershipID, colleagueMembershipID} {
			if rejected := teamTestDecision(t, ctx, store, orgID, request.ID, membershipID, "knobs-handoff-1-rejected-"+string(rune('a'+index)), now); rejected.Status != domain.OperationRejected || rejected.ErrorCode != "not_targeted" {
				t.Fatalf("untargeted decision = %#v", rejected)
			}
		}
		// Only a routed reviewer (or a break-glass holder) pulls a colleague in.
		if _, err := addReviewer(strangerMembershipID, colleagueMembershipID, request.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("stranger adding a reviewer error = %v", err)
		}
		// The colleague must be an active member who may decide.
		if _, err := addReviewer(routedMembershipID, auditorMembershipID, request.ID); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("adding an auditor error = %v", err)
		}
		if _, err := addReviewer(routedMembershipID, domain.ID(uuid.NewString()), request.ID); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("adding an unknown membership error = %v", err)
		}
		reviewer, err := addReviewer(routedMembershipID, colleagueMembershipID, request.ID)
		if err != nil {
			t.Fatalf("AddApprovalReviewer: %v", err)
		}
		if reviewer.RequestID != request.ID || reviewer.MembershipID != colleagueMembershipID || reviewer.DisplayName != "colleague@example.com" ||
			reviewer.AddedByMembershipID != routedMembershipID || reviewer.AddedByDisplayName != "routed@example.com" || !reviewer.CreatedAt.Equal(now) {
			t.Fatalf("reviewer = %+v", reviewer)
		}
		if _, err := addReviewer(routedMembershipID, colleagueMembershipID, request.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate reviewer error = %v", err)
		}
		if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events
			WHERE organization_id = $1 AND event_type = 'request.reviewer_added' AND subject_type = 'approval_request' AND subject_id = $2`,
			orgID, request.ID); count != 1 {
			t.Fatalf("request.reviewer_added events = %d", count)
		}
		if data := eventData("request.reviewer_added", request.ID); data["membership_id"] != string(colleagueMembershipID) ||
			data["added_by_membership_id"] != string(routedMembershipID) {
			t.Fatalf("reviewer_added data = %v", data)
		}
		assertSQLRejected(t, ctx, store, `DELETE FROM approval_request_reviewers WHERE request_id = $1`, "append-only", request.ID)

		// The read model sees the handoff from every side.
		if state := routingState(colleagueMembershipID, request.ID); !state.HasTargets || !state.Targeted ||
			len(state.AddedReviewers) != 1 || !sameReviewer(state.AddedReviewers[0], reviewer) {
			t.Fatalf("colleague routing state = %+v", state)
		}
		if state := routingState(routedMembershipID, request.ID); !state.HasTargets || !state.Targeted {
			t.Fatalf("routed member routing state = %+v", state)
		}
		if state := routingState(strangerMembershipID, request.ID); !state.HasTargets || state.Targeted {
			t.Fatalf("stranger routing state = %+v", state)
		}
		recipients, err := store.ResolveApprovalRecipients(ctx, application.ResolveApprovalRecipientsInput{
			OrganizationID: orgID, RequestID: request.ID,
		})
		if err != nil {
			t.Fatalf("ResolveApprovalRecipients: %v", err)
		}
		recipientIDs := recipients.MembershipIDs()
		if len(recipientIDs) != 2 || !slices.Contains(recipientIDs, routedMembershipID) || !slices.Contains(recipientIDs, colleagueMembershipID) {
			t.Fatalf("recipients after handoff = %v", recipientIDs)
		}

		// Claiming follows the same rule, and the colleague's decision is
		// inside routing: no break-glass event.
		if _, err := store.ClaimApproval(ctx, application.ClaimApprovalInput{
			OrganizationID: orgID, RequestID: request.ID, Principal: principalOf(colleagueMembershipID), Now: now,
		}); err != nil {
			t.Fatalf("colleague claim: %v", err)
		}
		if applied := teamTestDecision(t, ctx, store, orgID, request.ID, colleagueMembershipID, "knobs-handoff-1-applied", now); applied.Status != domain.OperationApplied {
			t.Fatalf("colleague decision = %#v", applied)
		}
		if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events
			WHERE organization_id = $1 AND event_type = 'decision.unrouted' AND subject_id = $2`, orgID, request.ID); count != 0 {
			t.Fatalf("routed decision wrote decision.unrouted: %d", count)
		}
		if _, err := addReviewer(routedMembershipID, strangerMembershipID, request.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("adding to a decided request error = %v", err)
		}

		// A break-glass holder who is not targeted may still hand off, and
		// an unrouted request has nothing to widen.
		second := pinRequest("knobs-handoff-2", true)
		if _, err := addReviewer(ownerMembershipID, strangerMembershipID, second.ID); err != nil {
			t.Fatalf("owner handoff: %v", err)
		}
		unrouted := pinRequest("knobs-handoff-3", false)
		if _, err := addReviewer(ownerMembershipID, strangerMembershipID, unrouted.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("handoff on an unrouted request error = %v", err)
		}
		if state := routingState(strangerMembershipID, unrouted.ID); state.HasTargets || state.Targeted || len(state.AddedReviewers) != 0 {
			t.Fatalf("unrouted routing state = %+v", state)
		}
		if _, err := store.AddApprovalReviewer(ctx, application.AddApprovalReviewerInput{
			OrganizationID: orgID, RequestID: domain.ID(uuid.NewString()), MembershipID: strangerMembershipID,
			Actor: principalOf(ownerMembershipID), Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("handoff on a missing request error = %v", err)
		}
	})

	t.Run("a decision outside routing is audited as decision.unrouted", func(t *testing.T) {
		request := pinRequest("knobs-break-glass-1", true)
		if plain := teamTestDecision(t, ctx, store, orgID, request.ID, ownerMembershipID, "knobs-break-glass-1-plain", now); plain.Status != domain.OperationRejected || plain.ErrorCode != "outside_routing_required" {
			t.Fatalf("unacknowledged break-glass decision = %#v", plain)
		}
		applied := teamTestDecisionWith(t, ctx, store, orgID, request.ID, ownerMembershipID, "knobs-break-glass-1", now, true)
		if applied.Status != domain.OperationApplied || applied.Decision == nil {
			t.Fatalf("break-glass decision = %#v", applied)
		}
		if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events
			WHERE organization_id = $1 AND event_type = 'decision.unrouted' AND subject_type = 'approval_request'
			  AND subject_id = $2 AND actor_type = 'human' AND actor_id = $3`, orgID, request.ID, ownerID); count != 1 {
			t.Fatalf("decision.unrouted events = %d", count)
		}
		if data := eventData("decision.unrouted", request.ID); data["decision_id"] != string(applied.Decision.ID) ||
			data["reason"] != "not_targeted" || data["dangerous"] != true {
			t.Fatalf("decision.unrouted data = %v", data)
		}
		// A targeted reviewer acknowledging needlessly is still inside routing.
		routed := pinRequest("knobs-break-glass-2", true)
		if applied := teamTestDecisionWith(t, ctx, store, orgID, routed.ID, routedMembershipID, "knobs-break-glass-2", now, true); applied.Status != domain.OperationApplied {
			t.Fatalf("routed decision with acknowledgement = %#v", applied)
		}
		if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events
			WHERE organization_id = $1 AND event_type = 'decision.unrouted' AND subject_id = $2`, orgID, routed.ID); count != 0 {
			t.Fatalf("targeted decision wrote decision.unrouted: %d", count)
		}
	})

	t.Run("routed_only visibility narrows the queue to what was routed to the member", func(t *testing.T) {
		setPolicy(domain.UnroutedAnyReviewer, domain.VisibilityRoutedOnly)
		defer setPolicy(domain.UnroutedAnyReviewer, domain.VisibilityAllReaders)
		routedRequest := pinRequest("knobs-visibility-routed", true)
		unroutedRequest := pinRequest("knobs-visibility-unrouted", false)
		handedOff := pinRequest("knobs-visibility-handoff", true)
		if _, err := addReviewer(routedMembershipID, colleagueMembershipID, handedOff.ID); err != nil {
			t.Fatalf("handoff: %v", err)
		}
		listFor := func(membershipID domain.ID) []domain.ID {
			t.Helper()
			requests, err := store.ListApprovals(ctx, application.ListApprovalsInput{
				OrganizationID: orgID, Limit: 200, HumanReviewOnly: true, VisibleTo: membershipID,
			})
			if err != nil {
				t.Fatalf("ListApprovals(%s): %v", membershipID, err)
			}
			return approvalIDs(requests)
		}
		expect := func(name string, listed []domain.ID, shown, hidden []domain.ID) {
			t.Helper()
			for _, id := range shown {
				if !slices.Contains(listed, id) {
					t.Fatalf("%s: request %s hidden, listed %v", name, id, listed)
				}
			}
			for _, id := range hidden {
				if slices.Contains(listed, id) {
					t.Fatalf("%s: request %s visible, listed %v", name, id, listed)
				}
			}
		}
		all := []domain.ID{routedRequest.ID, unroutedRequest.ID, handedOff.ID}
		expect("stranger", listFor(strangerMembershipID), []domain.ID{unroutedRequest.ID}, []domain.ID{routedRequest.ID, handedOff.ID})
		expect("routed member", listFor(routedMembershipID), all, nil)
		expect("added reviewer", listFor(colleagueMembershipID), []domain.ID{unroutedRequest.ID, handedOff.ID}, []domain.ID{routedRequest.ID})
		expect("everyone", listFor(""), all, nil)

		if _, err := store.GetApproval(ctx, application.GetApprovalInput{
			OrganizationID: orgID, RequestID: routedRequest.ID, VisibleTo: strangerMembershipID,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("hidden request read error = %v", err)
		}
		for name, membershipID := range map[string]domain.ID{"routed member": routedMembershipID, "everyone": ""} {
			if read, err := store.GetApproval(ctx, application.GetApprovalInput{
				OrganizationID: orgID, RequestID: routedRequest.ID, VisibleTo: membershipID,
			}); err != nil || read.ID != routedRequest.ID {
				t.Fatalf("%s read = %+v, %v", name, read, err)
			}
		}
		if read, err := store.GetApproval(ctx, application.GetApprovalInput{
			OrganizationID: orgID, RequestID: handedOff.ID, VisibleTo: colleagueMembershipID,
		}); err != nil || read.ID != handedOff.ID {
			t.Fatalf("added reviewer read = %+v, %v", read, err)
		}
		if read, err := store.GetApproval(ctx, application.GetApprovalInput{
			OrganizationID: orgID, RequestID: unroutedRequest.ID, VisibleTo: strangerMembershipID,
		}); err != nil || read.ID != unroutedRequest.ID {
			t.Fatalf("unrouted read = %+v, %v", read, err)
		}
	})

	// The queue's layers: the notified layer is what a plan addressed to
	// the member or a colleague handed them, the decidable layer is the
	// routing rule with unrouted requests hidden from whoever may not take
	// them.
	t.Run("notified and decidable layers narrow the queue", func(t *testing.T) {
		routed := pinRequest("layers-routed", true)
		unrouted := pinRequest("layers-unrouted", false)
		handedOff := pinRequest("layers-handoff", true)
		mine := []domain.ID{routed.ID, unrouted.ID, handedOff.ID}
		record := func(requestID domain.ID, at time.Time, members ...domain.ID) {
			t.Helper()
			if err := store.RecordApprovalNotifications(ctx, application.RecordApprovalNotificationsInput{
				OrganizationID: orgID, RequestID: requestID, MembershipIDs: members, Now: at,
			}); err != nil {
				t.Fatalf("RecordApprovalNotifications: %v", err)
			}
		}
		// A member the organization does not hold is skipped, not refused;
		// a second page keeps the first time.
		record(routed.ID, now, routedMembershipID, domain.ID(uuid.NewString()))
		record(routed.ID, now.Add(time.Minute), routedMembershipID)
		var notifiedAt time.Time
		if err := store.Pool().QueryRow(ctx, `SELECT notified_at FROM approval_request_notifications
			WHERE request_id = $1 AND membership_id = $2`, routed.ID, routedMembershipID).Scan(&notifiedAt); err != nil {
			t.Fatalf("read notification: %v", err)
		}
		if !notifiedAt.Equal(now) {
			t.Fatalf("notified_at = %s, want the first page at %s", notifiedAt, now)
		}
		if count := countRows(t, ctx, store, `SELECT count(*) FROM approval_request_notifications WHERE request_id = $1`, routed.ID); count != 1 {
			t.Fatalf("notification rows = %d", count)
		}
		if _, err := addReviewer(routedMembershipID, colleagueMembershipID, handedOff.ID); err != nil {
			t.Fatalf("handoff: %v", err)
		}
		listed := func(input application.ListApprovalsInput) []domain.ID {
			t.Helper()
			input.OrganizationID, input.Limit, input.HumanReviewOnly = orgID, 200, true
			requests, err := store.ListApprovals(ctx, input)
			if err != nil {
				t.Fatalf("ListApprovals(%#v): %v", input, err)
			}
			return approvalIDs(requests)
		}
		expect := func(name string, ids []domain.ID, shown ...domain.ID) {
			t.Helper()
			for _, id := range mine {
				if slices.Contains(ids, id) != slices.Contains(shown, id) {
					t.Fatalf("%s: listed %v, want %v of %v", name, ids, shown, mine)
				}
			}
		}
		expect("routed member notified", listed(application.ListApprovalsInput{NotifiedTo: routedMembershipID}), routed.ID)
		expect("handed-off colleague notified", listed(application.ListApprovalsInput{NotifiedTo: colleagueMembershipID}), handedOff.ID)
		expect("stranger notified", listed(application.ListApprovalsInput{NotifiedTo: strangerMembershipID}))
		expect("stranger decidable", listed(application.ListApprovalsInput{VisibleTo: strangerMembershipID}), unrouted.ID)
		expect("stranger decidable, unrouted hidden", listed(application.ListApprovalsInput{VisibleTo: strangerMembershipID, HideUnrouted: true}))
		expect("colleague decidable, unrouted hidden", listed(application.ListApprovalsInput{VisibleTo: colleagueMembershipID, HideUnrouted: true}), handedOff.ID)
		expect("routed member decidable, unrouted hidden", listed(application.ListApprovalsInput{VisibleTo: routedMembershipID, HideUnrouted: true}), routed.ID, handedOff.ID)
		expect("notified inside routing", listed(application.ListApprovalsInput{VisibleTo: colleagueMembershipID, NotifiedTo: colleagueMembershipID}), handedOff.ID)
	})

	t.Run("break_glass_only sends unrouted requests to break-glass holders", func(t *testing.T) {
		setPolicy(domain.UnroutedBreakGlassOnly, domain.VisibilityAllReaders)
		defer setPolicy(domain.UnroutedAnyReviewer, domain.VisibilityAllReaders)
		request := pinRequest("knobs-break-glass-only", false)
		if rejected := teamTestDecision(t, ctx, store, orgID, request.ID, strangerMembershipID, "knobs-break-glass-only-reviewer", now); rejected.Status != domain.OperationRejected || rejected.ErrorCode != "break_glass_only" {
			t.Fatalf("plain reviewer decision = %#v", rejected)
		}
		if _, err := store.ClaimApproval(ctx, application.ClaimApprovalInput{
			OrganizationID: orgID, RequestID: request.ID, Principal: principalOf(strangerMembershipID), Now: now,
		}); !errors.Is(err, domain.ErrNotTargeted) {
			t.Fatalf("plain reviewer claim error = %v", err)
		}
		if plain := teamTestDecision(t, ctx, store, orgID, request.ID, ownerMembershipID, "knobs-break-glass-only-plain", now); plain.Status != domain.OperationRejected || plain.ErrorCode != "outside_routing_required" {
			t.Fatalf("unacknowledged owner decision = %#v", plain)
		}
		if _, err := store.ClaimApproval(ctx, application.ClaimApprovalInput{
			OrganizationID: orgID, RequestID: request.ID, Principal: principalOf(ownerMembershipID), Now: now,
		}); err != nil {
			t.Fatalf("owner claim: %v", err)
		}
		if applied := teamTestDecisionWith(t, ctx, store, orgID, request.ID, ownerMembershipID, "knobs-break-glass-only-owner", now, true); applied.Status != domain.OperationApplied {
			t.Fatalf("owner decision = %#v", applied)
		}
		if data := eventData("decision.unrouted", request.ID); data["reason"] != "break_glass_only" {
			t.Fatalf("decision.unrouted data = %v", data)
		}
		// Back on any_reviewer the same reviewer decides a fresh unrouted request.
		setPolicy(domain.UnroutedAnyReviewer, domain.VisibilityAllReaders)
		fresh := pinRequest("knobs-any-reviewer", false)
		if applied := teamTestDecision(t, ctx, store, orgID, fresh.ID, strangerMembershipID, "knobs-any-reviewer", now); applied.Status != domain.OperationApplied {
			t.Fatalf("any_reviewer decision = %#v", applied)
		}
	})

	// The path's request conditions select who is reachable: the facts the
	// router pinned decide which branch's targets may act, notification
	// follows the same set, and a path that reaches nobody leaves the
	// request to break-glass reviewers.
	t.Run("branch conditions narrow who is reachable", func(t *testing.T) {
		conditionID := uuid.NewString()
		user := func(membershipID domain.ID) routing.Target {
			return routing.Target{Type: routing.TargetUser, ID: string(membershipID)}
		}
		strangerTarget := user(strangerMembershipID)
		branching := createRoutingTestPathRevision(t, ctx, store, orgID, ownerMembershipID, "knobs-branching",
			branchingPathDocument(t, conditionID, "stripe.refund", user(routedMembershipID), &strangerTarget), now)
		thenOnly := createRoutingTestPathRevision(t, ctx, store, orgID, ownerMembershipID, "knobs-then-only",
			branchingPathDocument(t, conditionID, "stripe.refund", user(routedMembershipID), nil), now)
		pinWithFacts := func(key, pathKey string, revisionID domain.ID, facts map[string]bool) domain.ApprovalRequest {
			t.Helper()
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
				EscalationPathKey: pathKey, EscalationPathRevisionID: revisionID,
				Urgency: "standard", Facts: facts, CreatedAt: now,
			}
			if _, err := store.InitializeApproval(ctx, initialization); err != nil {
				t.Fatalf("InitializeApproval %s: %v", key, err)
			}
			return request
		}
		matched := pinWithFacts("knobs-branch-then", "knobs-branching", branching.ID, map[string]bool{"/" + conditionID: true})
		unmatched := pinWithFacts("knobs-branch-else", "knobs-branching", branching.ID, nil)
		nobody := pinWithFacts("knobs-branch-nobody", "knobs-then-only", thenOnly.ID, map[string]bool{"/" + conditionID: false})

		// The facts round-trip and the view names who is reachable.
		view, err := store.GetApprovalRouting(ctx, application.GetApprovalRoutingInput{OrganizationID: orgID, RequestID: matched.ID})
		if err != nil || len(view.Facts) != 1 || !view.Facts["/"+conditionID] {
			t.Fatalf("matched routing view = %+v, %v", view, err)
		}
		if len(view.Targets) != 1 || view.Targets[0].Type != "user" || view.Targets[0].ID != routedMembershipID || view.Targets[0].Name != "routed@example.com" {
			t.Fatalf("matched routing targets = %+v", view.Targets)
		}
		view, err = store.GetApprovalRouting(ctx, application.GetApprovalRoutingInput{OrganizationID: orgID, RequestID: unmatched.ID})
		if err != nil || len(view.Facts) != 0 || len(view.Targets) != 1 || view.Targets[0].ID != strangerMembershipID {
			t.Fatalf("unmatched routing view = %+v, %v", view, err)
		}
		view, err = store.GetApprovalRouting(ctx, application.GetApprovalRoutingInput{OrganizationID: orgID, RequestID: nobody.ID})
		if err != nil || len(view.Targets) != 0 || view.Targets == nil {
			t.Fatalf("nobody routing view = %+v, %v", view, err)
		}
		program, err := store.LoadEscalationProgram(ctx, application.LoadEscalationProgramInput{OrganizationID: orgID, RequestID: matched.ID})
		if err != nil || !program.Escalated || len(program.Facts) != 1 || !program.Facts["/"+conditionID] {
			t.Fatalf("escalation program facts = %+v, %v", program.Facts, err)
		}

		// Only the reachable branch's target is targeted, notified, and
		// admitted to claim and decide.
		if state := routingState(routedMembershipID, matched.ID); !state.HasTargets || !state.Targeted {
			t.Fatalf("routed member state on the matched request = %+v", state)
		}
		if state := routingState(strangerMembershipID, matched.ID); !state.HasTargets || state.Targeted {
			t.Fatalf("stranger state on the matched request = %+v", state)
		}
		if state := routingState(strangerMembershipID, unmatched.ID); !state.HasTargets || !state.Targeted {
			t.Fatalf("stranger state on the unmatched request = %+v", state)
		}
		if state := routingState(routedMembershipID, unmatched.ID); !state.HasTargets || state.Targeted {
			t.Fatalf("routed member state on the unmatched request = %+v", state)
		}
		recipients, err := store.ResolveApprovalRecipients(ctx, application.ResolveApprovalRecipientsInput{OrganizationID: orgID, RequestID: matched.ID})
		if err != nil || !slices.Equal(recipients.MembershipIDs(), []domain.ID{routedMembershipID}) {
			t.Fatalf("matched recipients = %v, %v", recipients.MembershipIDs(), err)
		}
		if _, err := store.ClaimApproval(ctx, application.ClaimApprovalInput{
			OrganizationID: orgID, RequestID: matched.ID, Principal: principalOf(strangerMembershipID), Now: now,
		}); !errors.Is(err, domain.ErrNotTargeted) {
			t.Fatalf("else-branch claim error = %v", err)
		}
		if rejected := teamTestDecision(t, ctx, store, orgID, matched.ID, strangerMembershipID, "knobs-branch-then-stranger", now); rejected.Status != domain.OperationRejected || rejected.ErrorCode != "not_targeted" {
			t.Fatalf("else-branch decision = %#v", rejected)
		}
		if applied := teamTestDecision(t, ctx, store, orgID, matched.ID, routedMembershipID, "knobs-branch-then-routed", now); applied.Status != domain.OperationApplied {
			t.Fatalf("then-branch decision = %#v", applied)
		}
		if rejected := teamTestDecision(t, ctx, store, orgID, unmatched.ID, routedMembershipID, "knobs-branch-else-routed", now); rejected.Status != domain.OperationRejected || rejected.ErrorCode != "not_targeted" {
			t.Fatalf("then-branch decision on the unmatched request = %#v", rejected)
		}

		// A path that reaches nobody scopes the request to break-glass
		// reviewers: a plain reviewer is not targeted and cannot pull anyone
		// in, an owner must acknowledge deciding outside routing, and an
		// owner may hand the request to a colleague who then decides inside
		// routing.
		if state := routingState(routedMembershipID, nobody.ID); !state.HasTargets || state.Targeted {
			t.Fatalf("routed member state on the unreachable request = %+v", state)
		}
		recipients, err = store.ResolveApprovalRecipients(ctx, application.ResolveApprovalRecipientsInput{OrganizationID: orgID, RequestID: nobody.ID})
		if err != nil || recipients.Outcome != domain.RoutingEscalated || len(recipients.MembershipIDs()) != 0 {
			t.Fatalf("unreachable recipients = %v, %v", recipients.MembershipIDs(), err)
		}
		if rejected := teamTestDecision(t, ctx, store, orgID, nobody.ID, routedMembershipID, "knobs-branch-nobody-routed", now); rejected.Status != domain.OperationRejected || rejected.ErrorCode != "not_targeted" {
			t.Fatalf("reviewer decision on the unreachable request = %#v", rejected)
		}
		if _, err := addReviewer(routedMembershipID, colleagueMembershipID, nobody.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("reviewer handoff on the unreachable request error = %v", err)
		}
		if plain := teamTestDecision(t, ctx, store, orgID, nobody.ID, ownerMembershipID, "knobs-branch-nobody-owner-plain", now); plain.Status != domain.OperationRejected || plain.ErrorCode != "outside_routing_required" {
			t.Fatalf("unacknowledged owner decision on the unreachable request = %#v", plain)
		}
		if _, err := addReviewer(ownerMembershipID, colleagueMembershipID, nobody.ID); err != nil {
			t.Fatalf("owner handoff on the unreachable request: %v", err)
		}
		recipients, err = store.ResolveApprovalRecipients(ctx, application.ResolveApprovalRecipientsInput{OrganizationID: orgID, RequestID: nobody.ID})
		if err != nil || !slices.Equal(recipients.MembershipIDs(), []domain.ID{colleagueMembershipID}) {
			t.Fatalf("unreachable recipients after handoff = %v, %v", recipients.MembershipIDs(), err)
		}
		if applied := teamTestDecision(t, ctx, store, orgID, nobody.ID, colleagueMembershipID, "knobs-branch-nobody-colleague", now); applied.Status != domain.OperationApplied {
			t.Fatalf("added reviewer decision on the unreachable request = %#v", applied)
		}
		if count := countRows(t, ctx, store, `SELECT count(*) FROM test_audit_events
			WHERE organization_id = $1 AND event_type = 'decision.unrouted' AND subject_id = $2`, orgID, nobody.ID); count != 0 {
			t.Fatalf("a handed-off decision is inside routing, got %d decision.unrouted events", count)
		}

		// routed_only visibility follows the same reachable set.
		setPolicy(domain.UnroutedAnyReviewer, domain.VisibilityRoutedOnly)
		defer setPolicy(domain.UnroutedAnyReviewer, domain.VisibilityAllReaders)
		visibleMatched := pinWithFacts("knobs-branch-visible", "knobs-branching", branching.ID, map[string]bool{"/" + conditionID: true})
		listed, err := store.ListApprovals(ctx, application.ListApprovalsInput{
			OrganizationID: orgID, Limit: 200, HumanReviewOnly: true, VisibleTo: strangerMembershipID,
		})
		if err != nil {
			t.Fatalf("ListApprovals(stranger): %v", err)
		}
		if ids := approvalIDs(listed); slices.Contains(ids, visibleMatched.ID) || !slices.Contains(ids, unmatched.ID) {
			t.Fatalf("stranger sees %v; the then-branch request must be hidden and the else-branch one shown", ids)
		}
		listed, err = store.ListApprovals(ctx, application.ListApprovalsInput{
			OrganizationID: orgID, Limit: 200, HumanReviewOnly: true, VisibleTo: routedMembershipID,
		})
		if err != nil {
			t.Fatalf("ListApprovals(routed): %v", err)
		}
		if ids := approvalIDs(listed); !slices.Contains(ids, visibleMatched.ID) || slices.Contains(ids, unmatched.ID) {
			t.Fatalf("routed member sees %v; the then-branch request must be shown and the else-branch one hidden", ids)
		}
	})

	t.Run("pagination counts only visible rows", func(t *testing.T) {
		input := application.ListApprovalsInput{OrganizationID: orgID, Limit: 200, HumanReviewOnly: true, VisibleTo: colleagueMembershipID}
		all, err := store.ListApprovals(ctx, input)
		if err != nil || len(all) < 2 {
			t.Fatalf("visible fixture: %d rows, %v", len(all), err)
		}
		input.Limit = 1
		for offset := range len(all) {
			input.Offset = offset
			page, err := store.ListApprovals(ctx, input)
			if err != nil || len(page) != 1 || page[0].ID != all[offset].ID {
				t.Fatalf("visible page %d: %#v, %v; want %s", offset, page, err, all[offset].ID)
			}
		}
	})

	// Runs last because it destroys the fixture organization.
	t.Run("purge removes added reviewers with the organization", func(t *testing.T) {
		request := pinRequest("knobs-purge", true)
		if _, err := addReviewer(routedMembershipID, colleagueMembershipID, request.ID); err != nil {
			t.Fatalf("handoff: %v", err)
		}
		purgeAt := now.Add(time.Hour)
		if _, err := store.SetOrganizationStatus(ctx, application.SetOrganizationStatusInput{
			OrganizationID: orgID, Status: domain.OrganizationPendingDeletion,
			Reason: "offboarding", DeletionScheduledFor: &purgeAt,
			ActorType: "human", ActorID: ownerID, Now: now,
		}); err != nil {
			t.Fatalf("schedule deletion: %v", err)
		}
		purged, err := store.PurgeOrganization(ctx, application.PurgeOrganizationInput{
			OrganizationID: orgID, Now: purgeAt.Add(time.Minute),
		})
		if err != nil || !purged {
			t.Fatalf("purge = %v, %v", purged, err)
		}
		if count := countRows(t, ctx, store, `SELECT count(*) FROM approval_request_reviewers WHERE organization_id = $1`, orgID); count != 0 {
			t.Fatalf("added reviewers survived the purge: %d", count)
		}
	})
}

// sameReviewer compares two reviewer records field by field, since the
// stored timestamp comes back in the driver's location.
func sameReviewer(a, b domain.ApprovalReviewer) bool {
	return a.RequestID == b.RequestID && a.MembershipID == b.MembershipID && a.DisplayName == b.DisplayName &&
		a.AddedByMembershipID == b.AddedByMembershipID && a.AddedByDisplayName == b.AddedByDisplayName && a.CreatedAt.Equal(b.CreatedAt)
}
