package teamescalation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/routing"
)

const (
	testTeamID    = domain.ID("71111111-1111-4111-8111-111111111111")
	memberOne     = "51111111-1111-4111-8111-111111111111"
	memberTwo     = "52222222-2222-4222-8222-222222222222"
	formerMember  = "53333333-3333-4333-8333-333333333333"
	outsideMember = "54444444-4444-4444-8444-444444444444"
)

type serviceClock struct{}

func (serviceClock) Now() time.Time { return time.Unix(90_000, 0).UTC() }

type policyRepositoryStub struct {
	application.TeamEscalationPolicyRepository
	stored    map[int64]domain.TeamEscalationPolicyRevision
	created   *application.CreateTeamEscalationPolicyRevisionInput
	activated *application.ActivateTeamEscalationPolicyRevisionInput
}

func (r *policyRepositoryStub) CreateTeamEscalationPolicyRevision(_ context.Context, input application.CreateTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error) {
	r.created = &input
	return domain.TeamEscalationPolicyRevision{
		ID: input.ID, OrganizationID: input.OrganizationID, TeamID: input.TeamID, Revision: int64(len(r.stored) + 1),
		Document: input.Document, DocumentVersion: input.DocumentVersion,
		CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
	}, nil
}

func (r *policyRepositoryStub) GetTeamEscalationPolicyRevision(_ context.Context, input application.GetTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error) {
	revision, ok := r.stored[input.Revision]
	if !ok || revision.TeamID != input.TeamID {
		return domain.TeamEscalationPolicyRevision{}, domain.ErrNotFound
	}
	return revision, nil
}

func (r *policyRepositoryStub) GetActiveTeamEscalationPolicyRevision(_ context.Context, input application.GetActiveTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error) {
	for _, revision := range r.stored {
		if revision.TeamID == input.TeamID && revision.IsActive {
			return revision, nil
		}
	}
	return domain.TeamEscalationPolicyRevision{}, domain.ErrNotFound
}

func (r *policyRepositoryStub) ActivateTeamEscalationPolicyRevision(_ context.Context, input application.ActivateTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error) {
	r.activated = &input
	revision := r.stored[input.Revision]
	revision.IsActive = true
	return revision, nil
}

// teamsStub is a one-team roster: members carry their status so the
// service can tell a departed target from a current one.
type teamsStub struct {
	application.TeamRepository
	members []application.TeamMemberRecord
	count   application.TeamMemberPermissionCount
	asked   []application.CountTeamMembersLackingInput
}

func (t *teamsStub) GetTeam(_ context.Context, input application.GetTeamInput) (domain.Team, []application.TeamMemberRecord, error) {
	if input.TeamID != testTeamID {
		return domain.Team{}, nil, domain.ErrNotFound
	}
	return domain.Team{ID: input.TeamID, OrganizationID: input.OrganizationID, Name: "Payments"}, t.members, nil
}

func (t *teamsStub) CountTeamMembersLacking(_ context.Context, input application.CountTeamMembersLackingInput) (application.TeamMemberPermissionCount, error) {
	t.asked = append(t.asked, input)
	return t.count, nil
}

func roster() *teamsStub {
	return &teamsStub{
		members: []application.TeamMemberRecord{
			{MembershipID: memberOne, DisplayName: "Ada", Status: domain.MembershipActive},
			{MembershipID: memberTwo, DisplayName: "Grace", Status: domain.MembershipActive},
			{MembershipID: formerMember, DisplayName: "Linus", Status: domain.MembershipSuspended},
		},
		count: application.TeamMemberPermissionCount{MemberCount: 2},
	}
}

func adminPrincipal() domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{
		domain.PermissionTeamRead, domain.PermissionTeamEscalationWrite, domain.PermissionTeamEscalationActivate,
	}))
}

func policyDocument(targets ...routing.Target) routing.TeamPolicyDocument {
	return routing.TeamPolicyDocument{Nodes: []routing.Node{{
		ID: uuid.NewString(), Type: routing.NodeLevel, Targets: targets, EscalateAfter: "15m",
	}}}
}

func userTarget(id string) routing.Target { return routing.Target{Type: routing.TargetUser, ID: id} }

func storedRevision(number int64, active bool, document routing.TeamPolicyDocument) domain.TeamEscalationPolicyRevision {
	raw, _ := json.Marshal(document)
	return domain.TeamEscalationPolicyRevision{
		ID: domain.ID(uuid.NewString()), OrganizationID: "org-1", TeamID: testTeamID, Revision: number, IsActive: active,
		Document: raw, DocumentVersion: routing.DocumentVersion, CreatedByActorID: "membership-1",
		CreatedAt: time.Unix(80_000, 0).UTC(),
	}
}

func TestPolicyManagementRequiresItsPermission(t *testing.T) {
	service := NewService(&policyRepositoryStub{}, roster(), serviceClock{})
	principal := adminPrincipal().WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionTeamRead}))

	_, err := service.CreateRevision(context.Background(), principal, testTeamID, policyDocument(userTarget(memberOne)))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("create without team.escalation.write = %v, want ErrForbidden", err)
	}
	if _, err := service.ActivateRevision(context.Background(), principal, testTeamID, 1, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("activate without team.escalation.activate = %v, want ErrForbidden", err)
	}
	if err := service.Deactivate(context.Background(), principal, testTeamID, 1); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("deactivate without team.escalation.activate = %v, want ErrForbidden", err)
	}
	agent := domain.Principal{ActorType: domain.ActorAgent, OrganizationID: "org-1"}
	if _, err := service.ListRevisions(context.Background(), agent, testTeamID, 10); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("agent principal = %v, want ErrForbidden", err)
	}
}

// A grant pinned to a team admits that team and no other: policies are
// team resources.
func TestTeamScopedGrantCoversItsOwnTeamOnly(t *testing.T) {
	repository := &policyRepositoryStub{}
	service := NewService(repository, roster(), serviceClock{})
	principal := domain.Principal{
		ActorType: domain.ActorHuman, OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants([]domain.Grant{{Permission: domain.PermissionTeamEscalationWrite, ScopeKind: domain.ScopeTeam, ScopeID: string(testTeamID)}})

	if _, err := service.CreateRevision(context.Background(), principal, testTeamID, policyDocument(userTarget(memberOne))); err != nil {
		t.Fatalf("own team: %v", err)
	}
	_, err := service.CreateRevision(context.Background(), principal, "other-team", policyDocument(userTarget(memberOne)))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other team = %v, want ErrForbidden", err)
	}
}

func TestCreateRevisionRefusesTargetsOutsideTheTeam(t *testing.T) {
	repository := &policyRepositoryStub{}
	service := NewService(repository, roster(), serviceClock{})

	for _, target := range []string{outsideMember, formerMember} {
		_, err := service.CreateRevision(context.Background(), adminPrincipal(), testTeamID, policyDocument(userTarget(target)))
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("target %s = %v, want ErrInvalid", target, err)
		}
	}
	if repository.created != nil {
		t.Fatal("revision was persisted despite a non-member target")
	}
	_, err := service.CreateRevision(context.Background(), adminPrincipal(), "missing-team", policyDocument(userTarget(memberOne)))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing team = %v, want ErrNotFound", err)
	}
}

func TestCreateRevisionAcceptsMembersAndBroadcast(t *testing.T) {
	repository := &policyRepositoryStub{}
	service := NewService(repository, roster(), serviceClock{})
	document := routing.TeamPolicyDocument{Nodes: []routing.Node{
		{ID: uuid.NewString(), Type: routing.NodeLevel, Targets: []routing.Target{userTarget(memberOne), userTarget(memberTwo)}, EscalateAfter: "15m"},
		{ID: uuid.NewString(), Type: routing.NodeLevel, Targets: []routing.Target{{Type: routing.TargetBroadcast}}, EscalateAfter: "30m"},
	}}

	revision, err := service.CreateRevision(context.Background(), adminPrincipal(), testTeamID, document)
	if err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}
	if repository.created == nil || repository.created.TeamID != testTeamID || repository.created.DocumentVersion != routing.DocumentVersion ||
		repository.created.CreatedByActorID != "membership-1" || !repository.created.Now.Equal(serviceClock{}.Now()) {
		t.Fatalf("create input = %+v", repository.created)
	}
	var stored routing.TeamPolicyDocument
	if err := routing.Decode(repository.created.Document, &stored); err != nil {
		t.Fatalf("stored document is unreadable: %v", err)
	}
	if !stored.Broadcasts() || len(stored.UserTargetIDs()) != 2 {
		t.Fatalf("stored targets = %#v", stored.Targets())
	}
	if len(revision.Warnings) != 0 {
		t.Fatalf("a roster that can decide in full warns nothing: %#v", revision.Warnings)
	}
}

// Team targets belong to organization paths; the document grammar
// refuses them before the roster is consulted.
func TestCreateRevisionRefusesTeamTargets(t *testing.T) {
	teams := roster()
	service := NewService(&policyRepositoryStub{}, teams, serviceClock{})

	_, err := service.CreateRevision(context.Background(), adminPrincipal(), testTeamID,
		policyDocument(routing.Target{Type: routing.TargetTeam, ID: uuid.NewString()}))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("team target = %v, want ErrInvalid", err)
	}
	if len(teams.asked) != 0 {
		t.Fatal("an invalid document must not reach the roster")
	}
}

func TestCreateRevisionWarnsWhenMembersCannotDecide(t *testing.T) {
	teams := roster()
	teams.count = application.TeamMemberPermissionCount{MemberCount: 2, LackingCount: 1}
	repository := &policyRepositoryStub{}
	service := NewService(repository, teams, serviceClock{})

	revision, err := service.CreateRevision(context.Background(), adminPrincipal(), testTeamID, policyDocument(routing.Target{Type: routing.TargetBroadcast}))
	if err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}
	if repository.created == nil {
		t.Fatal("the warning must not block the write")
	}
	if len(teams.asked) != 1 || teams.asked[0].TeamID != testTeamID || teams.asked[0].Permission != domain.PermissionRequestDecide {
		t.Fatalf("the team is asked once about request.decide: %#v", teams.asked)
	}
	if len(revision.Warnings) != 1 || revision.Warnings[0] != (Warning{Code: WarningTeamMembersCannotDecide, Count: 1, MemberCount: 2}) {
		t.Fatalf("warnings = %#v", revision.Warnings)
	}
}

func TestActivateRevisionRechecksRosterAndPassesExpectedRevision(t *testing.T) {
	repository := &policyRepositoryStub{stored: map[int64]domain.TeamEscalationPolicyRevision{
		1: storedRevision(1, true, policyDocument(userTarget(memberOne))),
		2: storedRevision(2, false, policyDocument(userTarget(formerMember))),
		3: storedRevision(3, false, policyDocument(userTarget(memberTwo))),
	}}
	service := NewService(repository, roster(), serviceClock{})

	_, err := service.ActivateRevision(context.Background(), adminPrincipal(), testTeamID, 2, nil)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("activating a revision naming a former member = %v, want ErrInvalid", err)
	}
	if repository.activated != nil {
		t.Fatal("refused activation must not reach the repository")
	}

	expected := int64(1)
	activated, err := service.ActivateRevision(context.Background(), adminPrincipal(), testTeamID, 3, &expected)
	if err != nil {
		t.Fatalf("ActivateRevision: %v", err)
	}
	if repository.activated == nil || repository.activated.Revision != 3 || repository.activated.ExpectedActiveRevision == nil ||
		*repository.activated.ExpectedActiveRevision != 1 || repository.activated.ActorID != "membership-1" {
		t.Fatalf("activate input = %+v", repository.activated)
	}
	if !activated.Revision.IsActive || activated.Revision.Revision != 3 || len(activated.Document.UserTargetIDs()) != 1 {
		t.Fatalf("activated = %+v", activated)
	}

	if _, err := service.ActivateRevision(context.Background(), adminPrincipal(), testTeamID, 9, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown revision = %v, want ErrNotFound", err)
	}
}

// Reading the active revision names every target who left the team,
// with the display name while the roster still knows them.
func TestActiveReadWarnsAboutDepartedTargets(t *testing.T) {
	teams := roster()
	teams.count = application.TeamMemberPermissionCount{MemberCount: 2, LackingCount: 2}
	repository := &policyRepositoryStub{stored: map[int64]domain.TeamEscalationPolicyRevision{
		1: storedRevision(1, true, policyDocument(userTarget(memberOne), userTarget(formerMember), userTarget(outsideMember))),
	}}
	service := NewService(repository, teams, serviceClock{})

	revision, err := service.GetActivePolicy(context.Background(), adminPrincipal(), testTeamID)
	if err != nil {
		t.Fatalf("GetActivePolicy: %v", err)
	}
	want := []Warning{
		{Code: WarningTargetNotMember, MembershipID: formerMember, DisplayName: "Linus"},
		{Code: WarningTargetNotMember, MembershipID: outsideMember},
		{Code: WarningTeamMembersCannotDecide, Count: 2, MemberCount: 2},
	}
	if len(revision.Warnings) != len(want) {
		t.Fatalf("warnings = %#v, want %#v", revision.Warnings, want)
	}
	for index := range want {
		if revision.Warnings[index] != want[index] {
			t.Fatalf("warning %d = %#v, want %#v", index, revision.Warnings[index], want[index])
		}
	}

	// A plain revision read reports departed targets but does not ask
	// about deciders; that finding belongs to the revision in force.
	teams.asked = nil
	read, err := service.GetRevision(context.Background(), adminPrincipal(), testTeamID, 1)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if len(read.Warnings) != 2 || len(teams.asked) != 0 {
		t.Fatalf("revision read warnings = %#v, asked = %#v", read.Warnings, teams.asked)
	}

	repository.stored[1] = storedRevision(1, false, policyDocument(userTarget(memberOne)))
	if _, err := service.GetActivePolicy(context.Background(), adminPrincipal(), testTeamID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no active revision = %v, want ErrNotFound", err)
	}
}

func TestStoredRevisionsOfAnotherGrammarAreRefused(t *testing.T) {
	stale := storedRevision(1, true, policyDocument(userTarget(memberOne)))
	stale.DocumentVersion = routing.DocumentVersion + 1
	repository := &policyRepositoryStub{stored: map[int64]domain.TeamEscalationPolicyRevision{1: stale}}
	service := NewService(repository, roster(), serviceClock{})

	if _, err := service.GetRevision(context.Background(), adminPrincipal(), testTeamID, 1); err == nil {
		t.Fatal("an unsupported document version must not decode")
	}
}
