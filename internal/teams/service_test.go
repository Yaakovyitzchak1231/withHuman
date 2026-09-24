package teams

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type teamRepositoryStub struct {
	application.TeamRepository
	createInput application.CreateTeamInput
	setInput    application.SetTeamManualMembersInput
	listInput   application.ListTeamsInput
}

func (r *teamRepositoryStub) CreateTeam(_ context.Context, input application.CreateTeamInput) (domain.Team, error) {
	r.createInput = input
	return domain.Team{ID: "team-1", OrganizationID: input.OrganizationID, Name: input.Name, CreatedAt: input.Now}, nil
}
func (r *teamRepositoryStub) ListTeams(_ context.Context, input application.ListTeamsInput) ([]domain.Team, error) {
	r.listInput = input
	return []domain.Team{{ID: "team-1"}}, nil
}
func (r *teamRepositoryStub) GetTeam(context.Context, application.GetTeamInput) (domain.Team, []application.TeamMemberRecord, error) {
	return domain.Team{ID: "team-1"}, nil, nil
}
func (r *teamRepositoryStub) RenameTeam(_ context.Context, input application.RenameTeamInput) (domain.Team, error) {
	return domain.Team{ID: input.TeamID, Name: input.Name}, nil
}
func (r *teamRepositoryStub) ArchiveTeam(context.Context, application.ArchiveTeamInput) error {
	return nil
}
func (r *teamRepositoryStub) SetTeamManualMembers(_ context.Context, input application.SetTeamManualMembersInput) ([]application.TeamMemberRecord, error) {
	r.setInput = input
	return nil, nil
}

func manager() domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionTeamRead, domain.PermissionTeamWrite, domain.PermissionTeamMemberWrite}))
}

func reviewer() domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, OrganizationID: "org-1", MembershipID: "membership-2",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestDecide, domain.PermissionTeamRead}))
}

func TestTeamMutationsRequireTeamWrite(t *testing.T) {
	service := NewService(&teamRepositoryStub{}, fixedClock{time.Unix(10_000, 0).UTC()})
	ctx := context.Background()

	if _, err := service.CreateTeam(ctx, reviewer(), "payments"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("CreateTeam without team.manage = %v, want forbidden", err)
	}
	if _, err := service.RenameTeam(ctx, reviewer(), "team-1", "payments"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("RenameTeam without team.manage = %v, want forbidden", err)
	}
	if err := service.ArchiveTeam(ctx, reviewer(), "team-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ArchiveTeam without team.manage = %v, want forbidden", err)
	}
	if _, err := service.SetTeamMembers(ctx, reviewer(), "team-1", nil, false); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("SetTeamMembers without team.manage = %v, want forbidden", err)
	}
	if _, err := service.CreateTeam(ctx, domain.Principal{ActorType: domain.ActorAgent, OrganizationID: "org-1"}, "payments"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("agent principals must not manage teams")
	}
}

func TestTeamReadsAllowAnyActiveMember(t *testing.T) {
	service := NewService(&teamRepositoryStub{}, fixedClock{time.Unix(10_000, 0).UTC()})
	ctx := context.Background()

	if _, err := service.ListTeams(ctx, reviewer(), 50, ""); err != nil {
		t.Fatalf("reviewers must be able to list teams: %v", err)
	}
	if _, _, err := service.GetTeam(ctx, reviewer(), "team-1"); err != nil {
		t.Fatalf("reviewers must be able to read a team: %v", err)
	}
	if _, err := service.ListTeams(ctx, domain.Principal{ActorType: domain.ActorAgent, OrganizationID: "org-1"}, 50, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("agent principals must not read teams")
	}
}

func TestTeamMutationsCarryActorAndNormalizedName(t *testing.T) {
	repository := &teamRepositoryStub{}
	now := time.Unix(10_000, 0).UTC()
	service := NewService(repository, fixedClock{now})

	if _, err := service.CreateTeam(context.Background(), manager(), "  payments  "); err != nil {
		t.Fatal(err)
	}
	if repository.createInput.Name != "payments" || repository.createInput.ActorMembershipID != "membership-1" || !repository.createInput.Now.Equal(now) {
		t.Fatalf("create input lost normalization or actor: %#v", repository.createInput)
	}
}

// The list query carries the caller's team.read scope: every team for an
// organization-wide grant, the bound teams for a scoped one, and
// forbidden when the permission is held nowhere.
func TestListTeamsNarrowsToTheReadScope(t *testing.T) {
	repository := &teamRepositoryStub{}
	service := NewService(repository, fixedClock{time.Unix(10_000, 0).UTC()})
	ctx := context.Background()

	if _, err := service.ListTeams(ctx, reviewer(), 50, ""); err != nil {
		t.Fatalf("organization-wide read: %v", err)
	}
	if !repository.listInput.Scope.All {
		t.Fatalf("organization-wide grant must list everything: %#v", repository.listInput.Scope)
	}
	scoped := reviewer().WithGrants([]domain.Grant{{Permission: domain.PermissionTeamRead, ScopeKind: domain.ScopeTeam, ScopeID: "team-1"}})
	if _, err := service.ListTeams(ctx, scoped, 50, ""); err != nil {
		t.Fatalf("scoped read: %v", err)
	}
	if ids := repository.listInput.Scope.IDs(domain.ScopeTeam); repository.listInput.Scope.All || len(ids) != 1 || ids[0] != "team-1" {
		t.Fatalf("scoped grant must narrow the query: %#v", repository.listInput.Scope)
	}
	unscoped := reviewer().WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestDecide}))
	if _, err := service.ListTeams(ctx, unscoped, 50, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("no team.read anywhere = %v, want forbidden", err)
	}
}
