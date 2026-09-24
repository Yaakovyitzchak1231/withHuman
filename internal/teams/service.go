// Package teams owns the manual team management surface: creating,
// renaming, and archiving teams and replacing their manual member set.
// Directory sync writes the same tables through the provider event
// pipeline and is deliberately not routed through this service.
package teams

import (
	"context"
	"strings"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

type Service struct {
	teams application.TeamRepository
	clock application.Clock
}

func NewService(teams application.TeamRepository, clock application.Clock) *Service {
	return &Service{teams: teams, clock: clock}
}

// requireMember is the shared precondition: an active human principal
// with a membership.
func (s *Service) requireMember(principal domain.Principal) error {
	if !domain.ManagementPrincipal(principal) {
		return domain.ErrForbidden
	}
	return nil
}

func (s *Service) require(principal domain.Principal, permission domain.Permission, resource domain.Resource) error {
	if err := s.requireMember(principal); err != nil {
		return err
	}
	return authorization.Check(principal, permission, resource)
}

func normalizeName(name string) string {
	return strings.TrimSpace(name)
}

func (s *Service) CreateTeam(ctx context.Context, principal domain.Principal, name string) (domain.Team, error) {
	if err := s.require(principal, domain.PermissionTeamWrite, domain.OrganizationResource()); err != nil {
		return domain.Team{}, err
	}
	return s.teams.CreateTeam(ctx, application.CreateTeamInput{
		OrganizationID: principal.OrganizationID, Name: normalizeName(name),
		ActorMembershipID: principal.MembershipID, Now: s.clock.Now(),
	})
}

// ListTeams admits anyone who can read a team somewhere: team names feed
// the escalation path editor and member views. The scope narrows the
// query to the teams the caller holds team.read at.
func (s *Service) ListTeams(ctx context.Context, principal domain.Principal, limit int, status application.TeamListStatus) ([]domain.Team, error) {
	if err := s.requireMember(principal); err != nil {
		return nil, err
	}
	scope := authorization.Scope(principal, domain.PermissionTeamRead)
	if scope.Empty() {
		return nil, authorization.Denied{Permission: domain.PermissionTeamRead, Resource: domain.OrganizationResource()}
	}
	return s.teams.ListTeams(ctx, application.ListTeamsInput{
		OrganizationID: principal.OrganizationID, Scope: scope, Limit: limit, Status: status,
	})
}

func (s *Service) GetTeam(ctx context.Context, principal domain.Principal, teamID domain.ID) (domain.Team, []application.TeamMemberRecord, error) {
	if err := s.require(principal, domain.PermissionTeamRead, domain.TeamResource(teamID)); err != nil {
		return domain.Team{}, nil, err
	}
	return s.teams.GetTeam(ctx, application.GetTeamInput{
		OrganizationID: principal.OrganizationID, TeamID: teamID,
	})
}

func (s *Service) RenameTeam(ctx context.Context, principal domain.Principal, teamID domain.ID, name string) (domain.Team, error) {
	if err := s.require(principal, domain.PermissionTeamWrite, domain.TeamResource(teamID)); err != nil {
		return domain.Team{}, err
	}
	return s.teams.RenameTeam(ctx, application.RenameTeamInput{
		OrganizationID: principal.OrganizationID, TeamID: teamID, Name: normalizeName(name),
		ActorMembershipID: principal.MembershipID, Now: s.clock.Now(),
	})
}

func (s *Service) ArchiveTeam(ctx context.Context, principal domain.Principal, teamID domain.ID) error {
	if err := s.require(principal, domain.PermissionTeamWrite, domain.TeamResource(teamID)); err != nil {
		return err
	}
	return s.teams.ArchiveTeam(ctx, application.ArchiveTeamInput{
		OrganizationID: principal.OrganizationID, TeamID: teamID,
		ActorMembershipID: principal.MembershipID, Now: s.clock.Now(),
	})
}

// SetTeamMembers replaces the manual member set. Removing someone the
// team's active escalation policy still names is refused unless force
// is set, so the caller sees who a level would start skipping.
func (s *Service) SetTeamMembers(ctx context.Context, principal domain.Principal, teamID domain.ID, membershipIDs []domain.ID, force bool) ([]application.TeamMemberRecord, error) {
	if err := s.require(principal, domain.PermissionTeamMemberWrite, domain.TeamResource(teamID)); err != nil {
		return nil, err
	}
	return s.teams.SetTeamManualMembers(ctx, application.SetTeamManualMembersInput{
		OrganizationID: principal.OrganizationID, TeamID: teamID, MembershipIDs: membershipIDs,
		ActorMembershipID: principal.MembershipID, Force: force, Now: s.clock.Now(),
	})
}
