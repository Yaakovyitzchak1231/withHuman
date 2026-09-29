package application

import (
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

func validEscalationPathKey(value string) error {
	return validRegistryKey("escalation path key", value)
}

func validOptionalEscalationPathKey(value string) error {
	if value == "" {
		return nil
	}
	return validEscalationPathKey(value)
}

func positiveDocumentVersion(version int) error {
	if version <= 0 {
		return invalidField("document version", "must be positive")
	}
	return nil
}

func validPathKeys(keys []string) error {
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if err := validEscalationPathKey(key); err != nil {
			return err
		}
		if _, duplicate := seen[key]; duplicate {
			return invalidField("escalation path keys", "must be unique")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// HumanReviewRoute is what the pipeline resolved for a request that needs
// a human: the escalation path its terminal block named, else the default
// of the pipeline the request was in, else the defaults of the pipelines
// that delegated into it (nearest first), else none. The router turns it
// into the pinned routing snapshot. PipelineRevisionID and BlockID are
// evidence of where the path came from: for a pipeline default the
// revision that supplied it, which may be a delegated pipeline's.
type HumanReviewRoute struct {
	PathKey string
	// AskAgentOwner sends the request to the agent's owner first; PathKey,
	// possibly empty, is then the fallback for a request without an owner
	// able to decide.
	AskAgentOwner      bool
	Reason             string
	PipelineRevisionID domain.ID
	BlockID            domain.ID
}

// CreateEscalationPathInput creates a path and persists its validated
// first document as revision one of the key (or the next number, when the
// key was archived before). The document arrives canonicalized by the
// routing management service.
type CreateEscalationPathInput struct {
	ID               domain.ID
	OrganizationID   domain.ID
	PathKey          string
	Name             string
	Document         json.RawMessage
	DocumentVersion  int
	CreatedByActorID domain.ID
	Now              time.Time
}

func (input CreateEscalationPathInput) Validate() error {
	return CreateEscalationPathRevisionInput(input).Validate()
}

// CreateEscalationPathRevisionInput persists one validated escalation path
// document as the next immutable revision for an existing path key. The
// document arrives canonicalized by the routing management service.
type CreateEscalationPathRevisionInput struct {
	ID               domain.ID
	OrganizationID   domain.ID
	PathKey          string
	Name             string
	Document         json.RawMessage
	DocumentVersion  int
	CreatedByActorID domain.ID
	Now              time.Time
}

func (input CreateEscalationPathRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("escalation path revision ID", input.ID),
		requiredID("organization ID", input.OrganizationID),
		validEscalationPathKey(input.PathKey),
		requiredString("escalation path name", input.Name),
		validJSON("escalation path document", input.Document),
		positiveDocumentVersion(input.DocumentVersion),
		requiredID("creator membership ID", input.CreatedByActorID),
		requiredTime("current time", input.Now),
	})
}

// EscalationPathStatus selects which paths a listing returns: the live
// ones (the default), the archived ones, or both.
type EscalationPathStatus string

const (
	EscalationPathStatusLive     EscalationPathStatus = "live"
	EscalationPathStatusArchived EscalationPathStatus = "archived"
	EscalationPathStatusAll      EscalationPathStatus = "all"
)

func validEscalationPathStatus(status EscalationPathStatus) error {
	switch status {
	case "", EscalationPathStatusLive, EscalationPathStatusArchived, EscalationPathStatusAll:
		return nil
	}
	return invalidField("escalation path status", "must be live, archived, or all")
}

type ListEscalationPathSummariesInput struct {
	OrganizationID domain.ID
	// Status defaults to live.
	Status EscalationPathStatus
	Limit  int
}

func (input ListEscalationPathSummariesInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validEscalationPathStatus(input.Status),
		nonNegative("limit", int64(input.Limit)),
	})
}

type ListEscalationPathRevisionsInput struct {
	OrganizationID domain.ID
	PathKey        string
	Limit          int
	// Cursor is the NextCursor of the previous page; empty for the first.
	Cursor string
}

func (input ListEscalationPathRevisionsInput) Validate() error {
	_, cursorErr := DecodeRevisionCursor(input.Cursor)
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validEscalationPathKey(input.PathKey),
		nonNegative("limit", int64(input.Limit)),
		cursorErr,
	})
}

type GetEscalationPathRevisionInput struct {
	OrganizationID domain.ID
	PathKey        string
	Revision       int64
}

func (input GetEscalationPathRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validEscalationPathKey(input.PathKey),
		positiveRevision(input.Revision),
	})
}

type GetActiveEscalationPathRevisionInput struct {
	OrganizationID domain.ID
	PathKey        string
}

func (input GetActiveEscalationPathRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validEscalationPathKey(input.PathKey),
	})
}

// ResolveEscalationPathsInput asks which of the named path keys exist and
// which have an active revision. Pipeline revisions consult it: creation
// needs every key to exist, activation needs every key active.
type ResolveEscalationPathsInput struct {
	OrganizationID domain.ID
	PathKeys       []string
}

func (input ResolveEscalationPathsInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validPathKeys(input.PathKeys),
	})
}

type ResolvedEscalationPaths struct {
	// Missing keys have no revision at all; Inactive keys exist without
	// an active revision.
	Missing  []string
	Inactive []string
}

// ResolveRoutingTargetsInput asks which of a document's targets exist:
// user targets must be active memberships of the organization and team
// targets must be existing teams. The result lists what is missing.
type ResolveRoutingTargetsInput struct {
	OrganizationID    domain.ID
	UserMembershipIDs []domain.ID
	TeamIDs           []domain.ID
}

func (input ResolveRoutingTargetsInput) Validate() error {
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	for _, membershipID := range input.UserMembershipIDs {
		if membershipID == "" {
			return invalidField("user membership IDs", "must not contain empty values")
		}
	}
	for _, teamID := range input.TeamIDs {
		if teamID == "" {
			return invalidField("team IDs", "must not contain empty values")
		}
	}
	return nil
}

type ResolvedRoutingTargets struct {
	MissingUsers []domain.ID
	MissingTeams []domain.ID
}

// ResolveApprovalRecipientsInput expands a request's pinned routing
// snapshot into the memberships its escalation path could notify. Team
// targets resolve to active members at call time, never snapshot time.
type ResolveApprovalRecipientsInput struct {
	OrganizationID domain.ID
	RequestID      domain.ID
}

func (input ResolveApprovalRecipientsInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("approval request ID", input.RequestID),
	})
}

// ApprovalRecipients is who to tell about a pending request, resolved at
// notification time. An escalated snapshot yields the union of every
// target in the pinned path document; a request that fell to the default
// queue yields every active reviewer, because the queue's promise is that
// the request waits visibly rather than silently.
type ApprovalRecipients struct {
	Outcome domain.RoutingOutcome
	// Urgency is the request's pinned urgency, standard when unrouted. It
	// selects which of each recipient's ladders applies.
	Urgency    domain.NotificationUrgency
	Recipients []NotificationRecipient
}

// MembershipIDs lists the resolved recipients by membership, for logs and
// audit records that identify people without carrying contact details.
func (recipients ApprovalRecipients) MembershipIDs() []domain.ID {
	ids := make([]domain.ID, 0, len(recipients.Recipients))
	for _, recipient := range recipients.Recipients {
		if recipient.MembershipID != "" {
			ids = append(ids, recipient.MembershipID)
		}
	}
	return ids
}

// ActivateEscalationPathRevisionInput switches the active revision for one
// path key. A nil ExpectedActiveRevision means the caller expects no
// revision to be active.
type ActivateEscalationPathRevisionInput struct {
	OrganizationID         domain.ID
	PathKey                string
	Revision               int64
	ExpectedActiveRevision *int64
	ActorID                domain.ID
	Now                    time.Time
}

func (input ActivateEscalationPathRevisionInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validEscalationPathKey(input.PathKey),
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

type ListEscalationPathUsesInput struct {
	OrganizationID domain.ID
	PathKey        string
}

func (input ListEscalationPathUsesInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validPathKeys([]string{input.PathKey}),
	})
}

// ArchiveEscalationPathInput archives one path key with every revision it
// has. Requests already routed keep the revision they pinned.
type ArchiveEscalationPathInput struct {
	OrganizationID domain.ID
	PathKey        string
	ActorID        domain.ID
	Now            time.Time
}

func (input ArchiveEscalationPathInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validEscalationPathKey(input.PathKey),
		requiredID("actor actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

type GetApprovalRoutingInput struct {
	OrganizationID domain.ID
	RequestID      domain.ID
}

func (input GetApprovalRoutingInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("approval request ID", input.RequestID),
	})
}

// ApprovalRoutingTarget is one person or team the pinned path can reach
// for this request, resolved from the document and the pinned facts.
type ApprovalRoutingTarget struct {
	// Type is "user" (ID is a membership) or "team".
	Type string
	ID   domain.ID
	Name string
}

// ApprovalRoutingView is the pinned snapshot with the escalation path's
// display name resolved for the product API.
type ApprovalRoutingView struct {
	domain.ApprovalRequestRouting
	EscalationPathName string
	// AgentOwnerName is the display name of an agent_owner snapshot's
	// owner. AgentOwnerHandedOffAt is set once the owner was deactivated
	// and the request moved to the pinned fallback.
	AgentOwnerName        string
	AgentOwnerHandedOffAt *time.Time
	// Targets is who current routing admits, in path order: the owner of
	// an agent_owner snapshot until a handoff, otherwise who the pinned
	// path reaches. Only these people, plus the added reviewers, may claim
	// or decide. Empty for a default queue snapshot; empty for an
	// escalated one means the path reaches nobody and only a break-glass
	// reviewer may decide.
	Targets []ApprovalRoutingTarget
	// PinnedTeamPolicies names the team policy revisions the request runs.
	PinnedTeamPolicies []PinnedTeamPolicy
	// Escalation is the executor's current position, nil before the first
	// tick or for a default queue snapshot.
	Escalation *EscalationProgress
}

// GetAgentOwnerStandingInput asks whether one membership, the owner of the
// personal agent instance that raised RequestID, can take the request.
type GetAgentOwnerStandingInput struct {
	OrganizationID domain.ID
	MembershipID   domain.ID
	RequestID      domain.ID
}

func (input GetAgentOwnerStandingInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("membership ID", input.MembershipID),
		requiredID("request ID", input.RequestID),
	})
}

// AgentOwnerStanding is what routing needs to know about an agent's owner:
// whether their membership and account are active, and whether they hold
// request.decide for the request. An owner who cannot decide is never
// routed to, so a request never waits on someone who cannot act.
type AgentOwnerStanding struct {
	Active    bool
	CanDecide bool
}
