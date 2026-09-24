package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// TeamRepository owns manual team management. Teams are mutable ops data,
// never revisioned. Directory sync writes the same tables through
// DirectoryRepository; manual and directory member assignments coexist by
// source, and a directory-linked team refuses manual rename and archive.
type TeamRepository interface {
	CreateTeam(ctx context.Context, input CreateTeamInput) (domain.Team, error)
	ListTeams(ctx context.Context, input ListTeamsInput) ([]domain.Team, error)
	GetTeam(ctx context.Context, input GetTeamInput) (domain.Team, []TeamMemberRecord, error)
	RenameTeam(ctx context.Context, input RenameTeamInput) (domain.Team, error)
	ArchiveTeam(ctx context.Context, input ArchiveTeamInput) error
	SetTeamManualMembers(ctx context.Context, input SetTeamManualMembersInput) ([]TeamMemberRecord, error)
	// CountTeamMembersLacking counts the team's active members and how
	// many of them hold the permission nowhere at organization scope,
	// directly or through a team, so the path editor can warn that a
	// target would not reach everyone it names.
	CountTeamMembersLacking(ctx context.Context, input CountTeamMembersLackingInput) (TeamMemberPermissionCount, error)
}

type CountTeamMembersLackingInput struct {
	OrganizationID domain.ID
	TeamID         domain.ID
	Permission     domain.Permission
}

func (input CountTeamMembersLackingInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
	}); err != nil {
		return err
	}
	if _, ok := domain.LookupPermission(input.Permission); !ok {
		return invalidField("permission", "unknown permission "+string(input.Permission))
	}
	return nil
}

// TeamMemberPermissionCount is the answer: how many active members the
// team has and how many of them lack the permission.
type TeamMemberPermissionCount struct {
	MemberCount  int
	LackingCount int
}

// TeamMemberRecord joins one team member's membership with the identity the
// management surface displays. Sources lists where the assignment came from
// ("manual", "directory"); both can be present at once.
type TeamMemberRecord struct {
	MembershipID domain.ID
	DisplayName  string
	Email        string
	Status       domain.MembershipStatus
	Sources      []string
}

type CreateTeamInput struct {
	OrganizationID    domain.ID
	Name              string
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input CreateTeamInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("team name", input.Name),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	})
}

type TeamListStatus string

const (
	TeamListStatusLive     TeamListStatus = "live"
	TeamListStatusArchived TeamListStatus = "archived"
	TeamListStatusAll      TeamListStatus = "all"
)

func validTeamListStatus(status TeamListStatus) error {
	switch status {
	case "", TeamListStatusLive, TeamListStatusArchived, TeamListStatusAll:
		return nil
	default:
		return invalidField("status", "must be live, archived, or all")
	}
}

type ListTeamsInput struct {
	Status         TeamListStatus
	OrganizationID domain.ID
	// Scope is where the caller holds team.read: everything, or a set of
	// team ids.
	Scope domain.ScopeSet
	Limit int
}

func (input ListTeamsInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validScopeSet("scope", input.Scope, domain.ScopeTeam),
		nonNegative("limit", int64(input.Limit)),
		validTeamListStatus(input.Status),
	})
}

type GetTeamInput struct {
	OrganizationID domain.ID
	TeamID         domain.ID
}

func (input GetTeamInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
	})
}

type RenameTeamInput struct {
	OrganizationID    domain.ID
	TeamID            domain.ID
	Name              string
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input RenameTeamInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
		requiredString("team name", input.Name),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	})
}

type ArchiveTeamInput struct {
	OrganizationID    domain.ID
	TeamID            domain.ID
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input ArchiveTeamInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	})
}

// SetTeamManualMembersInput replaces the team's manual member set.
// Directory-sourced assignments are untouched; the team's effective
// membership is the union of both sources.
type SetTeamManualMembersInput struct {
	OrganizationID    domain.ID
	TeamID            domain.ID
	MembershipIDs     []domain.ID
	ActorMembershipID domain.ID
	// Force accepts removing a member the team's active escalation policy
	// still names; the level that names them then skips them.
	Force bool
	Now   time.Time
}

func (input SetTeamManualMembersInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
		requiredID("acting membership ID", input.ActorMembershipID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	seen := make(map[domain.ID]struct{}, len(input.MembershipIDs))
	for _, membershipID := range input.MembershipIDs {
		if membershipID == "" {
			return invalidField("membership IDs", "must not contain empty values")
		}
		if _, duplicate := seen[membershipID]; duplicate {
			return invalidField("membership IDs", "must not contain duplicates")
		}
		seen[membershipID] = struct{}{}
	}
	return nil
}
