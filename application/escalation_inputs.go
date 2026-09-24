package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/routing"
)

// EscalationRepository is what the escalation executor reads and writes:
// the pinned program a request runs, and the progress it has made.
type EscalationRepository interface {
	// LoadEscalationProgram reads the pinned path revision and every pinned
	// team policy revision for one request. Escalated is false for a
	// default queue snapshot, in which case nothing else is set.
	LoadEscalationProgram(ctx context.Context, input LoadEscalationProgramInput) (EscalationProgramRecord, error)
	// RecordEscalationProgress persists one tick's progress and audit
	// events. It is a no-op that reports Applied false when the sequence is
	// not newer than the stored one or the request is no longer pending,
	// so activity retries and duplicate ticks change nothing.
	RecordEscalationProgress(ctx context.Context, input RecordEscalationProgressInput) (EscalationProgressResult, error)
	// GetActiveTeamEscalationPolicies reports the active policy revision of
	// each named team that has one.
	GetActiveTeamEscalationPolicies(ctx context.Context, input GetActiveTeamEscalationPoliciesInput) ([]domain.TeamEscalationPolicyRevision, error)
}

type LoadEscalationProgramInput struct {
	OrganizationID domain.ID
	RequestID      domain.ID
}

func (input LoadEscalationProgramInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("approval request ID", input.RequestID),
	})
}

// EscalationProgramRecord is the pinned material before decoding: the
// path document, the team policy documents keyed by team, and the pinned
// urgency.
type EscalationProgramRecord struct {
	Escalated    bool
	Urgency      string
	PathDocument json.RawMessage
	// PathDocumentVersion and the per-team versions let the decoder refuse
	// a grammar it does not know.
	PathDocumentVersion int
	TeamPolicies        []PinnedTeamPolicyDocument
	// Facts is the request-condition results pinned with the snapshot,
	// keyed like escalation.FactKey; the executor reads them as is.
	Facts map[string]bool
}

type PinnedTeamPolicyDocument struct {
	TeamID          domain.ID
	Document        json.RawMessage
	DocumentVersion int
}

// EscalationPhase is where a walker stands.
type EscalationPhase string

const (
	EscalationPhaseLevel      EscalationPhase = "level"
	EscalationPhaseDeferred   EscalationPhase = "deferred"
	EscalationPhaseRepeatWait EscalationPhase = "repeat_wait"
	EscalationPhasePaused     EscalationPhase = "paused"
	EscalationPhaseExhausted  EscalationPhase = "exhausted"
)

func (phase EscalationPhase) Valid() bool {
	switch phase {
	case EscalationPhaseLevel, EscalationPhaseDeferred, EscalationPhaseRepeatWait, EscalationPhasePaused, EscalationPhaseExhausted:
		return true
	}
	return false
}

// EscalationWalkerProgress is one walker's position: the organization
// path when TeamID is empty, else that team's policy.
type EscalationWalkerProgress struct {
	TeamID        domain.ID
	Phase         EscalationPhase
	CurrentNodeID string
	LevelOrdinal  int
	LevelCount    int
	Repeat        int
	// NextAt is when the walker's armed timer fires; zero while paused,
	// exhausted, or waiting on nothing.
	NextAt time.Time
}

// EscalationProgress is the whole machine's position after a tick.
type EscalationProgress struct {
	Sequence     int64
	Urgency      routing.Urgency
	Organization EscalationWalkerProgress
	Teams        []EscalationWalkerProgress
	UpdatedAt    time.Time
}

// RecordEscalationProgressInput persists a tick: the progress rows and
// the audit events the tick produced.
type RecordEscalationProgressInput struct {
	OrganizationID domain.ID
	RequestID      domain.ID
	Progress       EscalationProgress
	Events         []domain.AuditEvent
	Now            time.Time
}

func (input RecordEscalationProgressInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("approval request ID", input.RequestID),
		nonNegative("sequence", input.Progress.Sequence),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if !input.Progress.Organization.Phase.Valid() {
		return invalidField("organization phase", "unknown phase "+string(input.Progress.Organization.Phase))
	}
	for _, team := range input.Progress.Teams {
		if team.TeamID == "" {
			return invalidField("team progress", "team ID is required")
		}
		if !team.Phase.Valid() {
			return invalidField("team phase", "unknown phase "+string(team.Phase))
		}
	}
	if input.Progress.Urgency != routing.UrgencyStandard && input.Progress.Urgency != routing.UrgencyInterrupt {
		return invalidField("urgency", "must be standard or interrupt")
	}
	return nil
}

type EscalationProgressResult struct {
	Applied bool
}

// LevelEntry is one level the executor entered in a tick: the
// organization path's when TeamID is empty, else that team policy's.
// Targets are the level's own; the resolver expands them to people.
type LevelEntry struct {
	TeamID  domain.ID
	Targets []routing.Target
}

// ResolveLevelRecipientsInput expands the levels one tick entered into
// the memberships to page. Organization levels resolve user targets and
// the current members of policy-less team targets; team levels resolve
// user targets only while they are still on the team, and broadcast to
// every current member. Nobody outside a team is ever reached through
// that team's policy.
type ResolveLevelRecipientsInput struct {
	OrganizationID domain.ID
	RequestID      domain.ID
	Urgency        domain.NotificationUrgency
	Entries        []LevelEntry
}

func (input ResolveLevelRecipientsInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("approval request ID", input.RequestID),
	}); err != nil {
		return err
	}
	if input.Urgency != domain.NotificationStandard && input.Urgency != domain.NotificationInterrupt {
		return invalidField("urgency", "must be standard or interrupt")
	}
	for _, entry := range input.Entries {
		for _, target := range entry.Targets {
			switch target.Type {
			case routing.TargetUser, routing.TargetTeam:
				if target.ID == "" {
					return invalidField("level targets", "user and team targets need an id")
				}
			case routing.TargetBroadcast:
				if entry.TeamID == "" {
					return invalidField("level targets", "broadcast belongs to a team level")
				}
			default:
				return invalidField("level targets", "unsupported target type "+string(target.Type))
			}
		}
	}
	return nil
}

// SubmitClaimReleaseInput tells the request's workflow that a claim was
// released early, so escalation resumes without waiting for the claim's
// retrigger timer. The release itself committed before this is sent.
type SubmitClaimReleaseInput struct {
	OrganizationID domain.ID
	RequestID      domain.ID
	ClaimID        domain.ID
}

func (input SubmitClaimReleaseInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("approval request ID", input.RequestID),
		requiredID("claim ID", input.ClaimID),
	})
}
