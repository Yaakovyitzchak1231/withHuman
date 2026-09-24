package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// TeamEscalationPolicyRepository owns monotonically revisioned team
// escalation policy documents addressed by team. Every lookup and
// mutation is scoped by organization. Active switches use the expected
// active revision as their optimistic concurrency precondition.
type TeamEscalationPolicyRepository interface {
	CreateTeamEscalationPolicyRevision(context.Context, CreateTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error)
	ListTeamEscalationPolicyRevisions(context.Context, ListTeamEscalationPolicyRevisionsInput) ([]domain.TeamEscalationPolicyRevision, error)
	GetTeamEscalationPolicyRevision(context.Context, GetTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error)
	GetActiveTeamEscalationPolicyRevision(context.Context, GetActiveTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error)
	ActivateTeamEscalationPolicyRevision(context.Context, ActivateTeamEscalationPolicyRevisionInput) (domain.TeamEscalationPolicyRevision, error)
	DeactivateTeamEscalationPolicy(context.Context, DeactivateTeamEscalationPolicyInput) (domain.TeamEscalationPolicyRevision, error)
}

// CreateTeamEscalationPolicyRevisionInput persists one validated team
// policy document as the next immutable revision for its team. The
// document arrives canonicalized by the team escalation service.
type CreateTeamEscalationPolicyRevisionInput struct {
	ID               domain.ID
	OrganizationID   domain.ID
	TeamID           domain.ID
	Document         json.RawMessage
	DocumentVersion  int
	CreatedByActorID domain.ID
	Now              time.Time
}

func (input CreateTeamEscalationPolicyRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("team escalation policy revision ID", input.ID),
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
		validJSON("team escalation policy document", input.Document),
		positiveDocumentVersion(input.DocumentVersion),
		requiredID("creator membership ID", input.CreatedByActorID),
		requiredTime("current time", input.Now),
	})
}

type ListTeamEscalationPolicyRevisionsInput struct {
	OrganizationID domain.ID
	TeamID         domain.ID
	Limit          int
}

func (input ListTeamEscalationPolicyRevisionsInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
		nonNegative("limit", int64(input.Limit)),
	})
}

type GetTeamEscalationPolicyRevisionInput struct {
	OrganizationID domain.ID
	TeamID         domain.ID
	Revision       int64
}

func (input GetTeamEscalationPolicyRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
		positiveRevision(input.Revision),
	})
}

type GetActiveTeamEscalationPolicyRevisionInput struct {
	OrganizationID domain.ID
	TeamID         domain.ID
}

func (input GetActiveTeamEscalationPolicyRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
	})
}

// ActivateTeamEscalationPolicyRevisionInput switches the active revision
// for one team. A nil ExpectedActiveRevision means the caller expects no
// revision to be active.
type ActivateTeamEscalationPolicyRevisionInput struct {
	OrganizationID         domain.ID
	TeamID                 domain.ID
	Revision               int64
	ExpectedActiveRevision *int64
	ActorID                domain.ID
	Now                    time.Time
}

func (input ActivateTeamEscalationPolicyRevisionInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
		positiveRevision(input.Revision),
		requiredID("actor actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if input.ExpectedActiveRevision != nil {
		return positiveRevision(*input.ExpectedActiveRevision)
	}
	return nil
}

type DeactivateTeamEscalationPolicyInput struct {
	OrganizationID         domain.ID
	TeamID                 domain.ID
	ExpectedActiveRevision int64
	ActorID                domain.ID
	Now                    time.Time
}

func (input DeactivateTeamEscalationPolicyInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("team ID", input.TeamID),
		positiveRevision(input.ExpectedActiveRevision),
		requiredID("actor actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

// GetActiveTeamEscalationPoliciesInput asks which of the named teams
// currently have an active policy revision. The router pins the answer
// beside the path revision.
type GetActiveTeamEscalationPoliciesInput struct {
	OrganizationID domain.ID
	TeamIDs        []domain.ID
}

func (input GetActiveTeamEscalationPoliciesInput) Validate() error {
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	for _, teamID := range input.TeamIDs {
		if teamID == "" {
			return invalidField("team IDs", "must not contain empty values")
		}
	}
	return nil
}

// PinnedTeamPolicy is one pinned team policy revision with the team's
// display name resolved for the product API.
type PinnedTeamPolicy struct {
	TeamID           domain.ID
	TeamName         string
	PolicyRevisionID domain.ID
	Revision         int64
}
