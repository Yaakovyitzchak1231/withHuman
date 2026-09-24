package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

func TestEnrollmentSelectsExistingAgent(t *testing.T) {
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
	activate := func(slug string) (domain.ID, domain.ID) {
		org, member := domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
		_, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: org, OrganizationSlug: slug, OrganizationName: slug, UserID: domain.ID(uuid.NewString()), MembershipID: member, DisplayName: "Owner", Email: slug + "@example.com", PasswordHash: "argon2id$test", AdmissionSource: domain.AdmissionManual, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		return org, member
	}
	org, member := activate("enrollment")
	foreignOrg, foreignMember := activate("foreign")
	create := func(org, member domain.ID, name, kind string) domain.Agent {
		a, err := store.CreateAgent(ctx, application.CreateAgentInput{OrganizationID: org, ActorID: member, Name: name, AgentSlug: domain.AgentSlug(kind), AgentQuota: application.UnlimitedQuota(), Now: now})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	coding := create(org, member, "coding-agents", "coding-agents")
	other := create(org, member, "operations", "operations")
	foreign := create(foreignOrg, foreignMember, "coding-agents", "coding-agents")
	begin := func(runtime, target, name string) string {
		code := uuid.NewString()
		err := store.CreateAgentEnrollment(ctx, application.CreateAgentEnrollmentInput{Enrollment: domain.AgentEnrollment{ID: domain.ID(uuid.NewString()), EnrollmentCodeHash: tokenHashOf(code), UserCode: code, Runtime: runtime, RequestedAgentSlug: target, InstanceName: name, InstanceMetadata: json.RawMessage(`{}`), Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}})
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	authorize := func(code string, agent domain.ID) (domain.AgentInstance, error) {
		return store.AuthorizeAgentEnrollment(ctx, application.AuthorizeAgentEnrollmentInput{OrganizationID: org, MembershipID: member, UserCode: code, AgentID: agent, Now: now})
	}
	code := begin("claude-code", "", "desktop")
	preview, err := store.GetAgentEnrollment(ctx, application.GetAgentEnrollmentInput{OrganizationID: org, UserCode: code, Now: now})
	if err != nil || preview.Runtime != "claude-code" || preview.RequestedAgentSlug != "" {
		t.Fatal(preview, err)
	}
	first, err := authorize(code, coding.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := authorize(code, coding.ID)
	if err != nil || again.ID != first.ID {
		t.Fatalf("retry duplicated instance: %+v %v", again, err)
	}
	if _, err := authorize(code, other.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("retarget: %v", err)
	}
	if _, err := store.GetAgentEnrollment(ctx, application.GetAgentEnrollmentInput{OrganizationID: foreignOrg, UserCode: code, Now: now}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign preview: %v", err)
	}
	if _, err := authorize(begin("codex", "", "foreign"), foreign.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign target: %v", err)
	}
	fixed := begin("codex", "coding-agents", "laptop")
	if _, err := authorize(fixed, other.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CLI target override: %v", err)
	}
	second, err := authorize(fixed, coding.ID)
	if err != nil || second.AgentID != first.AgentID || second.ID == first.ID {
		t.Fatal(second, err)
	}
	if _, err := authorize(begin("claude-code", "", "desktop"), coding.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	expired := begin("claude-code", "", "expired")
	if _, err := store.AuthorizeAgentEnrollment(ctx, application.AuthorizeAgentEnrollmentInput{OrganizationID: org, MembershipID: member, UserCode: expired, AgentID: coding.ID, Now: now.Add(time.Hour)}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}
	cred := application.AgentCredentialRecord{ID: domain.ID(uuid.NewString()), SecretHash: tokenHashOf("instance-secret"), IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
	identity, err := store.ExchangeAgentEnrollment(ctx, application.ExchangeAgentEnrollmentInput{EnrollmentCodeHash: tokenHashOf(code), Credential: cred, Now: now})
	if err != nil || identity.Agent.ID != coding.ID || identity.Agent.Name != "coding-agents" || identity.AgentInstance.ID != first.ID {
		t.Fatal(identity, err)
	}
	if _, err := store.ExchangeAgentEnrollment(ctx, application.ExchangeAgentEnrollmentInput{EnrollmentCodeHash: tokenHashOf(code), Credential: cred, Now: now}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("exchange replay: %v", err)
	}
	if replay, err := authorize(code, coding.ID); err != nil || replay.ID != first.ID {
		t.Fatalf("authorization after exchange: %+v %v", replay, err)
	}

	concurrentCode := begin("codex", "", "concurrent")
	type outcome struct {
		instance domain.AgentInstance
		err      error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() { instance, err := authorize(concurrentCode, coding.ID); results <- outcome{instance, err} }()
	}
	x, y := <-results, <-results
	if x.err != nil || y.err != nil || x.instance.ID != y.instance.ID {
		t.Fatalf("concurrent authorization: %+v %+v", x, y)
	}
	// Pagination must reach eligible agents after the old 200-item inventory cap.
	// Every agent has a slug of its own, so the wide listing is the whole
	// organization and a scope at one slug narrows to exactly that agent.
	for i := 0; i < 205; i++ {
		create(org, member, fmt.Sprintf("agent-%03d", i), fmt.Sprintf("agent-%03d", i))
	}
	everywhere := domain.ScopeSet{All: true}
	offset, count := 0, 0
	for {
		page, err := store.ListEnrollmentAgents(ctx, application.ListEnrollmentAgentsInput{OrganizationID: org, Scope: everywhere, Offset: offset, Limit: 25})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Agents {
			if a.OrganizationID != org {
				t.Fatal(a)
			}
		}
		count += len(page.Agents)
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if count != 207 {
		t.Fatalf("eligible count=%d", count)
	}
	page, err := store.ListEnrollmentAgents(ctx, application.ListEnrollmentAgentsInput{OrganizationID: org, Scope: everywhere, Query: "AGENT-204", Limit: 25})
	if err != nil || len(page.Agents) != 1 {
		t.Fatal(page, err)
	}
	codingOnly := domain.ScopeSet{Scopes: []domain.Scope{{Kind: domain.ScopeAgent, ID: "coding-agents"}}}
	page, err = store.ListEnrollmentAgents(ctx, application.ListEnrollmentAgentsInput{OrganizationID: org, Scope: codingOnly, Limit: 25})
	if err != nil || len(page.Agents) != 1 || page.Agents[0].ID != coding.ID || page.Agents[0].AgentSlug != "coding-agents" || page.NextOffset != nil {
		t.Fatalf("agent scope must narrow to the one agent holding the slug: %+v %v", page, err)
	}
	if _, err := store.SetAgentStatus(ctx, application.SetAgentStatusInput{OrganizationID: org, AgentID: coding.ID, Status: domain.AgentDisabled, ActorID: member, Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := authorize(begin("codex", "coding-agents", "disabled"), coding.ID); !errors.Is(err, domain.ErrAgentInactive) {
		t.Fatalf("disabled agent: %v", err)
	}
	if _, err := store.ArchiveAgent(ctx, application.ArchiveAgentInput{OrganizationID: org, AgentID: other.ID, ActorID: member, Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := authorize(begin("codex", "operations", "archived"), other.ID); !errors.Is(err, domain.ErrAgentInactive) {
		t.Fatalf("archived agent: %v", err)
	}

	// Live grants are reloaded inside authorization, even if the caller held them earlier.
	invitationID := domain.ID(uuid.NewString())
	hash := tokenHashOf("installer-invite")
	_, err = store.CreateInvitation(ctx, application.CreateInvitationInput{OrganizationID: org, InvitationID: invitationID, Email: "installer@example.com", Grants: []domain.AssignmentSpec{organizationGrant(domain.RoleAdminID)}, InvitedByMembershipID: member, TokenHash: hash, ExpiresAt: now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	installer, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{InvitationID: invitationID, TokenHash: hash, AssertedEmail: "installer@example.com", NewUserID: domain.ID(uuid.NewString()), DisplayName: "Installer", PasswordHash: "argon2id$test", ProviderKey: "local", Issuer: "withhuman:self-hosted", MembershipID: domain.ID(uuid.NewString()), SeatQuota: application.UnlimitedQuota(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	eligible := create(org, member, "still-active", "still-active")
	member = installer.MembershipID
	if _, err := authorize(begin("codex", "", "permitted"), eligible.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `DELETE FROM role_assignments WHERE organization_id=$1 AND principal_kind='membership' AND principal_id=$2`, org, member); err != nil {
		t.Fatal(err)
	}
	if _, err := authorize(begin("codex", "", "revoked-permission"), eligible.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("revoked issuance permission: %v", err)
	}
}
