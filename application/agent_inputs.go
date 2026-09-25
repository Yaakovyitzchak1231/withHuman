package application

import (
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

type AgentCredentialRecord struct {
	ID            domain.ID
	SecretHash    []byte
	IssuedAt      time.Time
	ExpiresAt     time.Time
	RotatedFromID domain.ID
}

func (record AgentCredentialRecord) Validate() error {
	if err := firstError([]error{
		requiredID("credential ID", record.ID),
		requiredTokenHash("credential secret hash", record.SecretHash),
		requiredTime("credential issued time", record.IssuedAt),
		requiredTime("credential expiry time", record.ExpiresAt),
	}); err != nil {
		return err
	}
	if !record.ExpiresAt.After(record.IssuedAt) {
		return invalidField("credential expiry time", "must be after the issued time")
	}
	return nil
}

type ResolveAgentCredentialInput struct {
	CredentialID domain.ID
	SecretHash   []byte
	Now          time.Time
}

func (input ResolveAgentCredentialInput) Validate() error {
	return firstError([]error{
		requiredID("credential ID", input.CredentialID),
		requiredTokenHash("credential secret hash", input.SecretHash),
		requiredTime("current time", input.Now),
	})
}

// LookupAgentSlugInput names exactly one of an agent, an instance, a
// credential, or a provisioner within the organization.
type LookupAgentSlugInput struct {
	OrganizationID  domain.ID
	AgentID         domain.ID
	AgentInstanceID domain.ID
	CredentialID    domain.ID
	ProvisionerID   domain.ID
}

func (input LookupAgentSlugInput) Validate() error {
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	set := 0
	for _, id := range []domain.ID{input.AgentID, input.AgentInstanceID, input.CredentialID, input.ProvisionerID} {
		if id != "" {
			set++
		}
	}
	if set != 1 {
		return invalidField("agent lookup", "exactly one of agent, instance, credential, or provisioner ID is required")
	}
	return nil
}

type CreateAgentInput struct {
	OrganizationID domain.ID
	ActorID        domain.ID
	Name           string
	AgentSlug      domain.AgentSlug
	// Kind is service or personal, chosen once; see domain.AgentKind.
	Kind domain.AgentKind
	// AgentQuota is enforced inside the creating transaction.
	AgentQuota AllocationQuota
	Now        time.Time
}

func (input CreateAgentInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("actor ID", input.ActorID),
		requiredString("agent name", input.Name),
		validAgentSlug(input.AgentSlug),
		validAgentKind(input.Kind),
		input.AgentQuota.Validate(),
		requiredTime("current time", input.Now),
	})
}

type GetAgentInput struct {
	OrganizationID domain.ID
	AgentID        domain.ID
}

func (input GetAgentInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
	})
}

// RecentRequestWindow is how far back the agent inventory counts
// requests: the "recent" number on every agent and instance.
const RecentRequestWindow = RecentRequestDays * 24 * time.Hour

// RecentRequestDays is the window in days: one trend bucket each.
const RecentRequestDays = 7

// AgentListStatus selects which agents the inventory returns: the live
// ones (active and disabled, the default), the archived ones, or both.
type AgentListStatus string

const (
	AgentListStatusLive     AgentListStatus = "live"
	AgentListStatusArchived AgentListStatus = "archived"
	AgentListStatusAll      AgentListStatus = "all"
)

func validAgentListStatus(status AgentListStatus) error {
	switch status {
	case "", AgentListStatusLive, AgentListStatusArchived, AgentListStatusAll:
		return nil
	}
	return invalidField("agent status filter", "must be live, archived, or all")
}

// ListAgentSummariesInput lists the inventory. Now anchors credential
// liveness and the recent request window.
type ListAgentSummariesInput struct {
	OrganizationID domain.ID
	// Scope is where the caller holds agent.read: everything, or a set of
	// agent types the query is narrowed to.
	Scope domain.ScopeSet
	// Status defaults to live.
	Status AgentListStatus
	Limit  int
	Now    time.Time
}

func (input ListAgentSummariesInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validScopeSet("scope", input.Scope, domain.ScopeAgent),
		validAgentListStatus(input.Status),
		nonNegative("limit", int64(input.Limit)),
		requiredTime("current time", input.Now),
	})
}

// RenameAgentInput changes the agent's display name. The slug never
// changes; the new name must be free among the organization's live agents.
type RenameAgentInput struct {
	OrganizationID domain.ID
	AgentID        domain.ID
	Name           string
	ActorID        domain.ID
	Now            time.Time
}

func (input RenameAgentInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredString("agent name", input.Name),
		requiredID("acting actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

// RestoreAgentInput brings an archived agent back to active, with its
// approval pipeline. Credentials and provisioner tokens revoked when it
// was archived stay revoked.
type RestoreAgentInput struct {
	OrganizationID domain.ID
	AgentID        domain.ID
	ActorID        domain.ID
	Now            time.Time
}

func (input RestoreAgentInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredID("acting actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

// ArchiveAgentInput archives an agent. The agent keeps its slug,
// instances, credentials, and history, none of which resolve again, and
// its approval pipeline is archived in the same transaction. Restore
// reverses it.
type ArchiveAgentInput struct {
	OrganizationID domain.ID
	AgentID        domain.ID
	ActorID        domain.ID
	Now            time.Time
}

func (input ArchiveAgentInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredID("acting actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

type GetAgentDetailInput struct {
	ProvisionerLimit  int
	ProvisionerOffset int
	ProvisionerQuery  string
	ProvisionerStatus string
	InstanceLimit     int
	InstanceOffset    int
	InstanceQuery     string
	InstanceStatus    domain.AgentStatus
	OrganizationID    domain.ID
	AgentID           domain.ID
	Now               time.Time
}

func (input GetAgentDetailInput) Validate() error {
	switch input.ProvisionerStatus {
	case "", "active", "expired", "revoked":
	default:
		return invalidField("provisioner_status", "Choose active, expired, or revoked.")
	}
	if input.InstanceStatus != "" && input.InstanceStatus != domain.AgentActive && input.InstanceStatus != domain.AgentDisabled {
		return invalidField("instance_status", "Choose active or disabled.")
	}
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredTime("current time", input.Now),
		nonNegative("instance offset", int64(input.InstanceOffset)),
		nonNegative("provisioner offset", int64(input.ProvisionerOffset)),
		nonNegative("provisioner limit", int64(input.ProvisionerLimit)),
		nonNegative("instance limit", int64(input.InstanceLimit)),
	})
}

// SetAgentStatusInput disables or re-enables an agent. A disabled agent
// keeps its instances and credentials, none of which can authenticate
// until it is enabled again.
type SetAgentStatusInput struct {
	OrganizationID domain.ID
	AgentID        domain.ID
	Status         domain.AgentStatus
	ActorID        domain.ID
	Now            time.Time
}

func (input SetAgentStatusInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		validAgentStatus(input.Status),
		requiredID("acting actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

// SetAgentInstanceStatusInput disables or re-enables one runtime instance
// without touching the agent or its other instances.
type SetAgentInstanceStatusInput struct {
	OrganizationID domain.ID
	InstanceID     domain.ID
	Status         domain.AgentStatus
	ActorID        domain.ID
	Now            time.Time
}

func (input SetAgentInstanceStatusInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent instance ID", input.InstanceID),
		validAgentStatus(input.Status),
		requiredID("acting actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

// RenameAgentInstanceInput changes one instance's display name. The name
// must be free among the agent's instances; requests already made keep
// the name the instance had when they were created.
type RenameAgentInstanceInput struct {
	OrganizationID domain.ID
	InstanceID     domain.ID
	Name           string
	ActorID        domain.ID
	Now            time.Time
}

func (input RenameAgentInstanceInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent instance ID", input.InstanceID),
		requiredString("agent instance name", input.Name),
		requiredID("acting actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

func validAgentStatus(status domain.AgentStatus) error {
	if !status.Valid() {
		return invalidField("agent status", "must be active or disabled")
	}
	return nil
}

type CreateAgentInstanceWithCredentialInput struct {
	ActorType      domain.ActorType
	OrganizationID domain.ID
	AgentID        domain.ID
	ActorID        domain.ID
	// MembershipID is the member the instance belongs to when the agent
	// is personal: the human registering it. Empty for a service agent;
	// the store refuses a mismatch either way.
	MembershipID domain.ID
	Name         string
	Metadata     json.RawMessage
	Credential   AgentCredentialRecord
	Now          time.Time
}

func (input CreateAgentInstanceWithCredentialInput) Validate() error {
	if input.ActorType != domain.ActorHuman && input.ActorType != domain.ActorAPIKey {
		return domain.ErrInvalid
	}
	if input.MembershipID != "" && input.ActorType != domain.ActorHuman {
		return invalidField("membership ID", "only a person can own an instance")
	}
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredID("actor ID", input.ActorID),
		requiredString("agent instance name", input.Name),
		validJSON("agent instance metadata", input.Metadata),
		input.Credential.Validate(),
		requiredTime("current time", input.Now),
	})
}

type AgentProvisionerRecord struct {
	ID         domain.ID
	SecretHash []byte
	IssuedAt   time.Time
	ExpiresAt  time.Time
}

func (record AgentProvisionerRecord) Validate() error {
	if err := firstError([]error{
		requiredID("provisioner ID", record.ID),
		requiredTokenHash("provisioner secret hash", record.SecretHash),
		requiredTime("provisioner issued time", record.IssuedAt),
		requiredTime("provisioner expiry time", record.ExpiresAt),
	}); err != nil {
		return err
	}
	if !record.ExpiresAt.After(record.IssuedAt) {
		return invalidField("provisioner expiry time", "must be after the issued time")
	}
	return nil
}

type CreateAgentProvisionerInput struct {
	OrganizationID domain.ID
	AgentID        domain.ID
	ActorID        domain.ID
	Provisioner    AgentProvisionerRecord
	Now            time.Time
}

func (input CreateAgentProvisionerInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredID("actor ID", input.ActorID),
		input.Provisioner.Validate(),
		requiredTime("current time", input.Now),
	})
}

// MintAgentInstanceInput registers a runtime and issues its credential in one
// step, authorized by a provisioner instead of a human principal. The instance
// name is optional; the adapter generates one from the instance identity when
// the minting caller has nothing meaningful to say.
type MintAgentInstanceInput struct {
	ProvisionerID domain.ID
	SecretHash    []byte
	InstanceName  string
	Metadata      json.RawMessage
	Credential    AgentCredentialRecord
	Now           time.Time
}

func (input MintAgentInstanceInput) Validate() error {
	return firstError([]error{
		requiredID("provisioner ID", input.ProvisionerID),
		requiredTokenHash("provisioner secret hash", input.SecretHash),
		validJSON("agent instance metadata", input.Metadata),
		input.Credential.Validate(),
		requiredTime("current time", input.Now),
	})
}

type RevokeAgentProvisionerInput struct {
	ActorID        domain.ID
	Now            time.Time
	OrganizationID domain.ID
	ProvisionerID  domain.ID
}

func (input RevokeAgentProvisionerInput) Validate() error {
	return firstError([]error{
		requiredID("actor ID", input.ActorID), requiredTime("current time", input.Now),
		requiredID("organization ID", input.OrganizationID),
		requiredID("provisioner ID", input.ProvisionerID),
	})
}

type RevokeAgentCredentialInput struct {
	ActorID        domain.ID
	Now            time.Time
	OrganizationID domain.ID
	CredentialID   domain.ID
}

func (input RevokeAgentCredentialInput) Validate() error {
	return firstError([]error{
		requiredID("actor ID", input.ActorID), requiredTime("current time", input.Now),
		requiredID("organization ID", input.OrganizationID),
		requiredID("credential ID", input.CredentialID),
	})
}

type CreateAgentEnrollmentInput struct {
	Enrollment domain.AgentEnrollment
}

func (input CreateAgentEnrollmentInput) Validate() error {
	enrollment := input.Enrollment
	if err := firstError([]error{
		requiredID("enrollment ID", enrollment.ID),
		requiredTokenHash("enrollment code hash", enrollment.EnrollmentCodeHash),
		requiredString("enrollment user code", enrollment.UserCode),
		requiredString("runtime", enrollment.Runtime),
		requiredString("agent instance name", enrollment.InstanceName),
		validJSON("agent instance metadata", enrollment.InstanceMetadata),
		requiredString("enrollment status", enrollment.Status),
		requiredTime("enrollment creation time", enrollment.CreatedAt),
		requiredTime("enrollment expiry time", enrollment.ExpiresAt),
	}); err != nil {
		return err
	}
	if enrollment.Status != "pending" {
		return invalidField("enrollment status", "must be pending when created")
	}
	if !enrollment.ExpiresAt.After(enrollment.CreatedAt) {
		return invalidField("enrollment expiry time", "must be after the creation time")
	}
	return nil
}

type AuthorizeAgentEnrollmentInput struct {
	OrganizationID domain.ID
	UserCode       string
	MembershipID   domain.ID
	AgentID        domain.ID
	Now            time.Time
}

func (input AuthorizeAgentEnrollmentInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("enrollment user code", input.UserCode),
		requiredID("membership ID", input.MembershipID),
		requiredID("agent ID", input.AgentID),
		requiredTime("current time", input.Now),
	})
}

type GetAgentEnrollmentInput struct {
	OrganizationID domain.ID
	UserCode       string
	Now            time.Time
}

func (input GetAgentEnrollmentInput) Validate() error {
	return firstError([]error{requiredID("organization ID", input.OrganizationID), requiredString("user code", input.UserCode), requiredTime("current time", input.Now)})
}

type ListEnrollmentAgentsInput struct {
	OrganizationID domain.ID
	Scope          domain.ScopeSet
	Query          string
	Offset         int
	Limit          int
}

func (input ListEnrollmentAgentsInput) Validate() error {
	// An empty scope lists only personal agents, which every member may
	// enroll their own instance of; the scope names the service agents.
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	if !input.Scope.Empty() {
		if err := validScopeSet("scope", input.Scope, domain.ScopeAgent); err != nil {
			return err
		}
	}
	if input.Offset < 0 || input.Limit < 1 || input.Limit > 100 {
		return invalidField("pagination", "offset must be nonnegative and limit between 1 and 100")
	}
	return nil
}

type ExchangeAgentEnrollmentInput struct {
	EnrollmentCodeHash []byte
	Credential         AgentCredentialRecord
	Now                time.Time
}

func (input ExchangeAgentEnrollmentInput) Validate() error {
	return firstError([]error{
		requiredTokenHash("enrollment code hash", input.EnrollmentCodeHash),
		input.Credential.Validate(),
		requiredTime("current time", input.Now),
	})
}
