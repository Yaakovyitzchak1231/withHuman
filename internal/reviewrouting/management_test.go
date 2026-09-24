package reviewrouting

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/routing"
)

type managementClock struct{}

func (managementClock) Now() time.Time { return time.Unix(90_000, 0).UTC() }

type pathRepositoryStub struct {
	application.EscalationPathRepository
	resolved application.ResolvedRoutingTargets
	created  bool
	// createdPath and archived record the explicit lifecycle calls.
	createdPath string
	archived    string
}

func (r *pathRepositoryStub) CreateEscalationPath(ctx context.Context, input application.CreateEscalationPathInput) (domain.EscalationPathRevision, error) {
	r.createdPath = input.PathKey
	return r.CreateEscalationPathRevision(ctx, application.CreateEscalationPathRevisionInput(input))
}

func (r *pathRepositoryStub) ArchiveEscalationPath(_ context.Context, input application.ArchiveEscalationPathInput) ([]domain.EscalationPathRevision, error) {
	r.archived = input.PathKey
	return []domain.EscalationPathRevision{{PathKey: input.PathKey, Revision: 1}}, nil
}

func (r *pathRepositoryStub) CreateEscalationPathRevision(_ context.Context, input application.CreateEscalationPathRevisionInput) (domain.EscalationPathRevision, error) {
	r.created = true
	return domain.EscalationPathRevision{
		ID: input.ID, OrganizationID: input.OrganizationID, PathKey: input.PathKey,
		Revision: 1, Name: input.Name, Document: input.Document,
		DocumentVersion: input.DocumentVersion, CreatedByActorID: input.CreatedByActorID,
		CreatedAt: input.Now,
	}, nil
}

func (r *pathRepositoryStub) ResolveRoutingTargets(context.Context, application.ResolveRoutingTargetsInput) (application.ResolvedRoutingTargets, error) {
	return r.resolved, nil
}

// teamRosterStub answers the roster warning for one team.
type teamRosterStub struct {
	counts map[domain.ID]application.TeamMemberPermissionCount
	asked  []application.CountTeamMembersLackingInput
}

func (r *teamRosterStub) GetTeam(_ context.Context, input application.GetTeamInput) (domain.Team, []application.TeamMemberRecord, error) {
	return domain.Team{ID: input.TeamID, OrganizationID: input.OrganizationID, Name: "Payments"}, nil, nil
}

func (r *teamRosterStub) CountTeamMembersLacking(_ context.Context, input application.CountTeamMembersLackingInput) (application.TeamMemberPermissionCount, error) {
	r.asked = append(r.asked, input)
	return r.counts[input.TeamID], nil
}

func pathManagerPrincipal() domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionEscalationPathRead, domain.PermissionEscalationPathWrite, domain.PermissionEscalationPathActivate}))
}

func targetedPathDocument(target routing.Target) routing.PathDocument {
	return routing.PathDocument{Nodes: []routing.Node{{
		ID: uuid.NewString(), Type: routing.NodeLevel,
		Targets: []routing.Target{target}, EscalateAfter: "15m",
	}}}
}

func TestCreatePathRevisionRejectsUnresolvableTargets(t *testing.T) {
	teamID := uuid.NewString()
	repository := &pathRepositoryStub{resolved: application.ResolvedRoutingTargets{
		MissingTeams: []domain.ID{domain.ID(teamID)},
	}}
	service := NewManagementService(repository, &teamRosterStub{}, managementClock{})

	_, err := service.CreatePathRevision(context.Background(), pathManagerPrincipal(), "payments", "Payments",
		targetedPathDocument(routing.Target{Type: routing.TargetTeam, ID: teamID}))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unresolvable team target error = %v, want ErrInvalid", err)
	}
	if repository.created {
		t.Fatal("revision was persisted despite unresolvable targets")
	}
}

func TestCreatePathRevisionAcceptsResolvableTargets(t *testing.T) {
	repository := &pathRepositoryStub{}
	service := NewManagementService(repository, &teamRosterStub{}, managementClock{})

	revision, err := service.CreatePathRevision(context.Background(), pathManagerPrincipal(), "payments", "Payments",
		targetedPathDocument(routing.Target{Type: routing.TargetTeam, ID: uuid.NewString()}))
	if err != nil {
		t.Fatalf("CreatePathRevision: %v", err)
	}
	if revision.Revision.DocumentVersion != routing.DocumentVersion || revision.Revision.Name != "Payments" {
		t.Fatalf("stored revision = %+v", revision.Revision)
	}
	var document routing.PathDocument
	if err := json.Unmarshal(revision.Revision.Document, &document); err != nil {
		t.Fatalf("stored document is unreadable: %v", err)
	}
	if targets := document.Targets(); len(targets) != 1 || targets[0].Type != routing.TargetTeam {
		t.Fatalf("stored targets = %#v", targets)
	}
}

func TestPathManagementRequiresItsPermission(t *testing.T) {
	service := NewManagementService(&pathRepositoryStub{}, &teamRosterStub{}, managementClock{})
	principal := pathManagerPrincipal()
	principal = principal.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionPipelineRead}))
	if _, err := service.ListPaths(context.Background(), principal, application.EscalationPathStatusLive, 10); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ListPaths without escalation_path.read = %v, want ErrForbidden", err)
	}
	if _, err := service.CreatePathRevision(context.Background(), pathManagerPrincipal(), "Not Valid", "x",
		targetedPathDocument(routing.Target{Type: routing.TargetUser, ID: uuid.NewString()})); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid path key error = %v, want ErrInvalid", err)
	}
}

// A team target whose members cannot all decide is reported, never
// refused: the path narrows who decides, it never grants.
func TestCreatePathRevisionWarnsWhenTeamMembersCannotDecide(t *testing.T) {
	teamID := domain.ID(uuid.NewString())
	roster := &teamRosterStub{counts: map[domain.ID]application.TeamMemberPermissionCount{teamID: {MemberCount: 5, LackingCount: 2}}}
	repository := &pathRepositoryStub{}
	service := NewManagementService(repository, roster, managementClock{})
	document := routing.PathDocument{Nodes: []routing.Node{
		{ID: uuid.NewString(), Type: routing.NodeLevel, Targets: []routing.Target{{Type: routing.TargetTeam, ID: string(teamID)}}, EscalateAfter: "15m"},
		{ID: uuid.NewString(), Type: routing.NodeLevel, Targets: []routing.Target{{Type: routing.TargetTeam, ID: string(teamID)}, {Type: routing.TargetUser, ID: uuid.NewString()}}, EscalateAfter: "15m"},
	}}

	revision, err := service.CreatePathRevision(context.Background(), pathManagerPrincipal(), "payments", "Payments", document)
	if err != nil {
		t.Fatalf("CreatePathRevision: %v", err)
	}
	if !repository.created {
		t.Fatal("the warning must not block the write")
	}
	if len(roster.asked) != 1 || roster.asked[0].TeamID != teamID || roster.asked[0].Permission != domain.PermissionRequestDecide {
		t.Fatalf("each team is asked once about request.decide: %#v", roster.asked)
	}
	if len(revision.Warnings) != 1 {
		t.Fatalf("warnings = %#v, want one", revision.Warnings)
	}
	warning := revision.Warnings[0]
	if warning.Code != PathWarningTeamMembersCannotDecide || warning.TeamID != teamID || warning.TeamName != "Payments" || warning.Count != 2 || warning.MemberCount != 5 {
		t.Fatalf("warning = %#v", warning)
	}

	// A roster that can decide in full produces nothing.
	roster.counts[teamID] = application.TeamMemberPermissionCount{MemberCount: 5}
	revision, err = service.CreatePathRevision(context.Background(), pathManagerPrincipal(), "payments", "Payments", document)
	if err != nil || len(revision.Warnings) != 0 {
		t.Fatalf("clean roster: warnings = %#v, err = %v", revision.Warnings, err)
	}
}

func TestCreatePathValidatesLikeARevision(t *testing.T) {
	teamID := uuid.NewString()
	repository := &pathRepositoryStub{resolved: application.ResolvedRoutingTargets{
		MissingTeams: []domain.ID{domain.ID(teamID)},
	}}
	service := NewManagementService(repository, &teamRosterStub{}, managementClock{})

	_, err := service.CreatePath(context.Background(), pathManagerPrincipal(), "payments", "Payments",
		targetedPathDocument(routing.Target{Type: routing.TargetTeam, ID: teamID}))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unresolvable team target error = %v, want ErrInvalid", err)
	}
	if repository.created || repository.createdPath != "" {
		t.Fatal("path was persisted despite unresolvable targets")
	}
	if _, err := service.CreatePath(context.Background(), pathManagerPrincipal(), "Not Valid", "Payments",
		targetedPathDocument(routing.Target{Type: routing.TargetTeam, ID: teamID})); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid path key error = %v, want ErrInvalid", err)
	}

	repository.resolved = application.ResolvedRoutingTargets{}
	revision, err := service.CreatePath(context.Background(), pathManagerPrincipal(), "payments", "Payments",
		targetedPathDocument(routing.Target{Type: routing.TargetTeam, ID: teamID}))
	if err != nil {
		t.Fatalf("CreatePath: %v", err)
	}
	if repository.createdPath != "payments" || revision.Revision.Revision != 1 || revision.Revision.DocumentVersion != routing.DocumentVersion {
		t.Fatalf("created path = %q, revision = %+v", repository.createdPath, revision.Revision)
	}
}

func TestArchivePathRequiresPermissionAndAValidKey(t *testing.T) {
	repository := &pathRepositoryStub{}
	service := NewManagementService(repository, &teamRosterStub{}, managementClock{})
	// Archiving stops a path being live, so it needs the activate
	// permission; write alone creates and appends revisions.
	writer := domain.Principal{ActorType: domain.ActorHuman, OrganizationID: "org-1", MembershipID: "membership-1"}.
		WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionEscalationPathRead, domain.PermissionEscalationPathWrite}))
	if err := service.ArchivePath(context.Background(), writer, "payments"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ArchivePath without escalation_path.activate = %v, want ErrForbidden", err)
	}
	if _, err := service.CreatePath(context.Background(), writer, "payments", "Payments",
		targetedPathDocument(routing.Target{Type: routing.TargetUser, ID: uuid.NewString()})); err != nil {
		t.Fatalf("CreatePath with escalation_path.write = %v", err)
	}
	if err := service.ArchivePath(context.Background(), pathManagerPrincipal(), "Not Valid"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ArchivePath with an invalid key = %v, want ErrInvalid", err)
	}
	if repository.archived != "" {
		t.Fatal("a refused archive reached the repository")
	}
	if err := service.ArchivePath(context.Background(), pathManagerPrincipal(), "payments"); err != nil || repository.archived != "payments" {
		t.Fatalf("ArchivePath = %v, archived %q", err, repository.archived)
	}
}

// A branch layout some request walks without meeting a level is reported,
// never refused: such a request reaches nobody and only a break-glass
// reviewer can decide it.
func TestCreatePathRevisionWarnsWhenThePathMayReachNobody(t *testing.T) {
	repository := &pathRepositoryStub{}
	service := NewManagementService(repository, &teamRosterStub{}, managementClock{})
	conditionID := uuid.NewString()
	thenOnly := routing.PathDocument{Nodes: []routing.Node{{
		ID: uuid.NewString(), Type: routing.NodeIfElse,
		Conditions: []routing.Condition{{Type: routing.ConditionRequest, When: &approvalblock.Condition{
			ID: conditionID, Field: "/request/tool", Operator: approvalblock.Equals, Value: json.RawMessage(`"deploy"`),
		}}},
		Then: []routing.Node{{ID: uuid.NewString(), Type: routing.NodeLevel, Targets: []routing.Target{{Type: routing.TargetUser, ID: uuid.NewString()}}, EscalateAfter: "15m"}},
	}}}

	revision, err := service.CreatePathRevision(context.Background(), pathManagerPrincipal(), "payments", "Payments", thenOnly)
	if err != nil {
		t.Fatalf("CreatePathRevision: %v", err)
	}
	if !repository.created {
		t.Fatal("the warning must not block the write")
	}
	if len(revision.Warnings) != 1 || revision.Warnings[0].Code != PathWarningMayReachNobody || revision.Warnings[0].TeamID != "" {
		t.Fatalf("warnings = %#v, want one path_may_reach_nobody without a team", revision.Warnings)
	}

	// An else branch with a level covers every request.
	covered := thenOnly
	covered.Nodes[0].Else = []routing.Node{{ID: uuid.NewString(), Type: routing.NodeLevel, Targets: []routing.Target{{Type: routing.TargetUser, ID: uuid.NewString()}}, EscalateAfter: "15m"}}
	revision, err = service.CreatePathRevision(context.Background(), pathManagerPrincipal(), "payments", "Payments", covered)
	if err != nil || len(revision.Warnings) != 0 {
		t.Fatalf("covered path: warnings = %#v, err = %v", revision.Warnings, err)
	}
}
