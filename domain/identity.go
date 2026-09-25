package domain

import (
	"encoding/json"
	"regexp"
	"time"
)

type ID string

type UserStatus string

const (
	UserActive   UserStatus = "active"
	UserDisabled UserStatus = "disabled"
)

type MembershipStatus string

const (
	MembershipActive        MembershipStatus = "active"
	MembershipSuspended     MembershipStatus = "suspended"
	MembershipDeprovisioned MembershipStatus = "deprovisioned"
)

type AdmissionSource string

const (
	AdmissionManual    AdmissionSource = "manual"
	AdmissionInvite    AdmissionSource = "invite"
	AdmissionSignup    AdmissionSource = "signup"
	AdmissionSSOJIT    AdmissionSource = "sso_jit"
	AdmissionDirectory AdmissionSource = "directory"
)

type OrganizationStatus string

const (
	OrganizationActive          OrganizationStatus = "active"
	OrganizationSuspended       OrganizationStatus = "suspended"
	OrganizationPendingDeletion OrganizationStatus = "pending_deletion"
)

func ValidOrganizationStatus(status OrganizationStatus) bool {
	switch status {
	case OrganizationActive, OrganizationSuspended, OrganizationPendingDeletion:
		return true
	}
	return false
}

type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "pending"
	InvitationAccepted InvitationStatus = "accepted"
	InvitationRevoked  InvitationStatus = "revoked"
	InvitationExpired  InvitationStatus = "expired"
)

// Invitation is addressed to an email, not a user: the invitee may not
// exist yet. A membership is created only when the invitation is accepted.
type Invitation struct {
	ID                    ID               `json:"id"`
	OrganizationID        ID               `json:"organization_id"`
	Email                 string           `json:"email"`
	Grants                []AssignmentSpec `json:"grants"`
	InvitedByMembershipID ID               `json:"invited_by_membership_id"`
	Status                InvitationStatus `json:"status"`
	ExpiresAt             time.Time        `json:"expires_at"`
	AcceptedMembershipID  ID               `json:"accepted_membership_id,omitempty"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

// Team is a mutable, org-scoped set of memberships: ops data, not
// revisioned config. DirectoryManaged is derived from directory group
// links, never stored, and locks manual rename and archive while manual
// member assignments continue to coexist with directory ones.
type Team struct {
	ArchivedAt       *time.Time `json:"archived_at"`
	ID               ID         `json:"id"`
	OrganizationID   ID         `json:"organization_id"`
	Name             string     `json:"name"`
	DirectoryManaged bool       `json:"directory_managed"`
	MemberCount      int        `json:"member_count"`
	// Assignments are the roles the team holds; its members inherit them.
	Assignments []RoleAssignment `json:"assignments"`
	// EscalationPolicy summarizes the team's escalation policy lineage:
	// nothing when the team has never had one, in which case a path level
	// that names the team notifies every member at once.
	EscalationPolicy TeamEscalationPolicyStatus `json:"escalation_policy"`
	CreatedAt        time.Time                  `json:"created_at"`
}

type User struct {
	ID           ID         `json:"id"`
	DisplayName  string     `json:"display_name"`
	PrimaryEmail string     `json:"primary_email"`
	Status       UserStatus `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type ExternalIdentity struct {
	UserID              ID        `json:"user_id"`
	ProviderKey         string    `json:"provider_key"`
	Issuer              string    `json:"issuer"`
	Subject             string    `json:"subject"`
	LastObservedEmail   string    `json:"last_observed_email"`
	EmailVerified       bool      `json:"email_verified"`
	LinkedAt            time.Time `json:"linked_at"`
	LastAuthenticatedAt time.Time `json:"last_authenticated_at"`
}

type Organization struct {
	ID                   ID                 `json:"id"`
	Slug                 string             `json:"slug"`
	Name                 string             `json:"name"`
	Status               OrganizationStatus `json:"status"`
	StatusReason         string             `json:"status_reason,omitempty"`
	SuspendedAt          *time.Time         `json:"suspended_at,omitempty"`
	DeletionScheduledFor *time.Time         `json:"deletion_scheduled_for,omitempty"`
	// OnboardingCompletedAt is set once a person finishes or skips the
	// guided setup; while it is null the product opens on /welcome for
	// members who hold OnboardingPermissions.
	OnboardingCompletedAt *time.Time `json:"onboarding_completed_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type Membership struct {
	ID               ID               `json:"id"`
	UserID           ID               `json:"user_id"`
	OrganizationID   ID               `json:"organization_id"`
	Status           MembershipStatus `json:"status"`
	AdmissionSource  AdmissionSource  `json:"admission_source"`
	DirectoryManaged bool             `json:"directory_managed"`
	// Assignments are the membership's grants, direct and inherited
	// through teams (ViaTeamID set), with roles resolved for display.
	Assignments []RoleAssignment `json:"assignments"`
	TeamIDs     []ID             `json:"team_ids"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type AdmissionMode string
type MFAMode string

const (
	AdmissionInviteOnly    AdmissionMode = "invite_only"
	AdmissionJIT           AdmissionMode = "jit"
	AdmissionDirectoryOnly AdmissionMode = "directory_only"

	MFAOptional          MFAMode = "optional"
	MFAReviewersRequired MFAMode = "reviewers_required"
	MFAEveryoneRequired  MFAMode = "everyone_required"
)

// UnroutedRequestsPolicy says who may decide a request no escalation path
// matched: any reviewer, or only holders of request.decide.unrouted.
type UnroutedRequestsPolicy string

// RequestVisibilityPolicy says who sees a request: everyone holding
// request.read, or only the people it was routed to plus owners and
// auditors.
type RequestVisibilityPolicy string

const (
	UnroutedAnyReviewer    UnroutedRequestsPolicy = "any_reviewer"
	UnroutedBreakGlassOnly UnroutedRequestsPolicy = "break_glass_only"

	VisibilityAllReaders RequestVisibilityPolicy = "all_readers"
	VisibilityRoutedOnly RequestVisibilityPolicy = "routed_only"
)

func (p UnroutedRequestsPolicy) Valid() bool {
	return p == UnroutedAnyReviewer || p == UnroutedBreakGlassOnly
}

func (p RequestVisibilityPolicy) Valid() bool {
	return p == VisibilityAllReaders || p == VisibilityRoutedOnly
}

type OrganizationPolicy struct {
	OrganizationID       ID            `json:"organization_id"`
	AdmissionMode        AdmissionMode `json:"admission_mode"`
	MFAMode              MFAMode       `json:"mfa_mode"`
	AllowSelfApproval    bool          `json:"allow_self_approval"`
	ApprovalReauthWindow time.Duration `json:"approval_reauth_window"`
	SessionMaxAge        time.Duration `json:"session_max_age"`
	// ClaimRetriggerWindow is how long a reviewer claim holds before it
	// lapses and escalation resumes.
	ClaimRetriggerWindow time.Duration           `json:"claim_retrigger_window"`
	UnroutedRequests     UnroutedRequestsPolicy  `json:"unrouted_requests"`
	RequestVisibility    RequestVisibilityPolicy `json:"request_visibility"`
	// PersonalAPIKeysAllowed is the organization's switch for personal
	// API keys. Off refuses minting and resolution of every personal key
	// without deleting any; on again brings them back.
	PersonalAPIKeysAllowed     bool `json:"personal_api_keys_allowed"`
	OrganizationAPIKeysAllowed bool `json:"organization_api_keys_allowed"`
}

type ActorType string

const (
	ActorAPIKey  ActorType = "api_key"
	ActorHuman   ActorType = "human"
	ActorAgent   ActorType = "agent"
	ActorSystem  ActorType = "system"
	ActorGateway ActorType = "gateway"
)

type AuthContext struct {
	Method          string    `json:"method"`
	Assurance       string    `json:"assurance"`
	AuthenticatedAt time.Time `json:"authenticated_at"`
	SessionID       ID        `json:"session_id,omitempty"`
	CredentialID    ID        `json:"credential_id,omitempty"`
}

// Principal is the provider-neutral identity every authenticated request
// carries. Grants are the authority: each is a permission pinned to a
// scope, flattened from the actor's role assignments. Permissions is the
// "holds it anywhere" projection of Grants for clients to gate navigation
// on; server code checks Grants through the authorization package and
// never reads Permissions.
type Principal struct {
	ActorType       ActorType    `json:"actor_type"`
	ActorID         ID           `json:"actor_id"`
	OrganizationID  ID           `json:"organization_id"`
	MembershipID    ID           `json:"membership_id,omitempty"`
	AgentInstanceID ID           `json:"agent_instance_id,omitempty"`
	AgentSlug       AgentSlug    `json:"agent_slug,omitempty"`
	Permissions     []Permission `json:"permissions"`
	Grants          []Grant      `json:"grants"`
	TeamIDs         []ID         `json:"team_ids,omitempty"`
	Auth            AuthContext  `json:"auth"`
}

// WithGrants sets the grant set and its projection together so the two
// can never disagree.
func (p Principal) WithGrants(grants []Grant) Principal {
	if grants == nil {
		grants = []Grant{}
	}
	p.Grants = grants
	p.Permissions = ProjectPermissions(grants)
	return p
}

type AgentStatus string

// AgentSlug identifies an agent inside its organization: lowercase, chosen
// once when the agent is created or first connected, never changed and
// never reused, not even after the agent is archived. It is what URLs,
// role scopes, pipeline scopes, audit data and conditions carry.
type AgentSlug string

var agentSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func (s AgentSlug) Valid() bool {
	return agentSlugPattern.MatchString(string(s))
}

const (
	AgentActive   AgentStatus = "active"
	AgentDisabled AgentStatus = "disabled"
	// AgentArchived is terminal: the agent keeps its instances,
	// credentials, and history for the record, but nothing of it resolves
	// again and nothing can be enrolled under it. Only archiving reaches
	// it; the status switch never does.
	AgentArchived AgentStatus = "archived"
)

// Valid reports whether the status is one an operator may set through
// the status switch; archiving is its own operation.
func (s AgentStatus) Valid() bool {
	return s == AgentActive || s == AgentDisabled
}

// AgentKind says whose behalf an agent's instances act on. It is chosen
// once, when the agent is created, and never changes: instances,
// enrollment rules and an edition's gateway access all follow from it.
type AgentKind string

const (
	// AgentService is an agent the organization runs: a support bot, a
	// deploy assistant. Its instances share one identity and belong to
	// nobody; administrators enroll them and hand them provisioner
	// tokens.
	AgentService AgentKind = "service"
	// AgentPersonal is an agent people run for themselves: a coding
	// assistant on a laptop. Every instance belongs to the member who
	// enrolled it, acts on that member's behalf, and goes with them when
	// they leave. Any active member may enroll their own instance;
	// provisioner tokens do not exist for it.
	AgentPersonal AgentKind = "personal"
)

func (k AgentKind) Valid() bool {
	return k == AgentService || k == AgentPersonal
}

// Agent is the stable organization-owned identity that asks for approvals.
// Concrete runtimes authenticate as AgentInstances of this identity. The
// slug is the identity clients see; ID is the internal key instances,
// provisioners, requests and role assignments point at and never leaves
// the process. Name is a display label, unique among live agents and
// editable. ArchivedAt is set exactly when Status is AgentArchived.
type Agent struct {
	ID               ID          `json:"-"`
	OrganizationID   ID          `json:"organization_id"`
	AgentSlug        AgentSlug   `json:"slug"`
	Name             string      `json:"name"`
	Kind             AgentKind   `json:"kind"`
	Status           AgentStatus `json:"status"`
	CreatedByActorID ID          `json:"created_by_actor_id"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	ArchivedAt       *time.Time  `json:"archived_at,omitempty"`
}

// AgentInstance is one enrolled runtime of an Agent, such as a local process,
// CI runner, container, or hosted replica. It is not a process execution.
// MembershipID is the member an instance of a personal agent belongs to
// and acts for; empty for an instance of a service agent.
type AgentInstance struct {
	ID             ID              `json:"id"`
	OrganizationID ID              `json:"organization_id"`
	AgentID        ID              `json:"-"`
	AgentSlug      AgentSlug       `json:"agent_slug"`
	MembershipID   ID              `json:"membership_id,omitempty"`
	Name           string          `json:"name"`
	Status         AgentStatus     `json:"status"`
	Metadata       json.RawMessage `json:"metadata"`
	CreatedAt      time.Time       `json:"created_at"`
	LastSeenAt     time.Time       `json:"last_seen_at"`
	// Delivery is the verified AAP webhook receiver, present only on the
	// instance a provisioner has just minted with one.
	Delivery *AgentInstanceDelivery `json:"delivery,omitempty"`
}

// AgentInstanceDelivery is an instance's verified AAP asynchronous
// receiver as the protocol exposes it; the signing secret never leaves
// the store.
type AgentInstanceDelivery struct {
	Type           string `json:"type"`
	URL            string `json:"url"`
	RegistrationID ID     `json:"registration_id"`
}

// AgentCredentialState is one instance credential as the inventory shows
// it: never the secret, only when it was issued, last used, when it
// expires, and whether it was revoked.
type AgentCredentialState struct {
	ID         ID         `json:"id"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Live reports whether the credential can still authenticate at now.
func (c AgentCredentialState) Live(now time.Time) bool {
	return c.RevokedAt == nil && c.ExpiresAt.After(now)
}

// AgentSummary is one row of the agents inventory: the stable identity
// plus what its runtimes have been doing. Liveness is derived from
// credentials, never from a heartbeat: agents call in only when they have
// work, so a quiet instance is not a broken one. A live instance is an
// active instance holding an unexpired, unrevoked credential. Request
// counts cover the recent window (application.RecentRequestWindow) and
// the requests still pending.
type AgentSummary struct {
	Agent
	InstanceCount       int        `json:"instance_count"`
	LiveInstanceCount   int        `json:"live_instance_count"`
	LastSeenAt          *time.Time `json:"last_seen_at,omitempty"`
	RecentRequestCount  int        `json:"recent_request_count"`
	PendingRequestCount int        `json:"pending_request_count"`
	// RequestTrend is the recent count split into one bucket per day,
	// oldest first: the last entry is the past 24 hours.
	RequestTrend []int `json:"request_trend"`
}

// AgentInstanceDetail is one enrolled runtime with every credential it
// has held, newest first, and its own recent request count and trend.
// OwnerDisplayName names the member a personal instance belongs to.
type AgentInstanceDetail struct {
	AgentInstance
	OwnerDisplayName   string                 `json:"owner_display_name,omitempty"`
	Credentials        []AgentCredentialState `json:"credentials"`
	RecentRequestCount int                    `json:"recent_request_count"`
	// RequestTrend is the recent count split into one bucket per day,
	// oldest first: the last entry is the past 24 hours.
	RequestTrend []int `json:"request_trend"`
}

// AgentDetail is the agent page: the identity, who created it, its
// instances with their credential state, its provisioner tokens, and its
// request counts.
type AgentDetail struct {
	Agent
	ProvisionerCount         int                   `json:"provisioner_count"`
	MatchingProvisionerCount int                   `json:"matching_provisioner_count"`
	InstanceCount            int                   `json:"instance_count"`
	LiveInstanceCount        int                   `json:"live_instance_count"`
	MatchingInstanceCount    int                   `json:"matching_instance_count"`
	RequestCount             int                   `json:"request_count"`
	CreatedByActorType       ActorType             `json:"created_by_actor_type"`
	CreatedByDisplayName     string                `json:"created_by_display_name"`
	Instances                []AgentInstanceDetail `json:"instances"`
	Provisioners             []AgentProvisioner    `json:"provisioners"`
	RecentRequestCount       int                   `json:"recent_request_count"`
	PendingRequestCount      int                   `json:"pending_request_count"`
}

type IssuedCredential struct {
	ID        ID        `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AgentProvisioner is an agent-scoped minting credential held by a fleet
// orchestrator or hosted agent platform. It creates AgentInstances and their
// credentials under its one agent and nothing else: it cannot create approval
// requests, and instance credentials cannot mint. The agent is fixed at
// issuance, never chosen per request.
type AgentProvisioner struct {
	ID               ID         `json:"id"`
	OrganizationID   ID         `json:"organization_id"`
	AgentID          ID         `json:"-"`
	AgentSlug        AgentSlug  `json:"agent_slug"`
	CreatedByActorID ID         `json:"created_by_actor_id"`
	IssuedAt         time.Time  `json:"issued_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

type AgentProvisionerCredential struct {
	Provisioner AgentProvisioner `json:"provisioner"`
	Credential  IssuedCredential `json:"credential"`
}

type AgentInstanceCredential struct {
	AgentInstance AgentInstance    `json:"agent_instance"`
	Credential    IssuedCredential `json:"credential"`
}

// AgentEnrollmentIdentity keeps the selected parent separate from the runtime.
type AgentEnrollmentIdentity struct {
	Agent         Agent         `json:"agent"`
	AgentInstance AgentInstance `json:"agent_instance"`
}

type AgentEnrollmentCredential struct {
	AgentEnrollmentIdentity
	Credential IssuedCredential `json:"credential"`
}

type EnrollmentAgents struct {
	Agents     []Agent `json:"agents"`
	NextOffset *int    `json:"next_offset,omitempty"`
}

// AgentEnrollment is short-lived bootstrap state for registering an
// AgentInstance through a browser-assisted authorization flow.
type AgentEnrollment struct {
	ID                 ID              `json:"id"`
	EnrollmentCodeHash []byte          `json:"-"`
	UserCode           string          `json:"user_code"`
	Runtime            string          `json:"runtime"`
	RequestedAgentSlug string          `json:"requested_agent_slug,omitempty"`
	InstanceName       string          `json:"instance_name"`
	InstanceMetadata   json.RawMessage `json:"instance_metadata"`
	OrganizationID     ID              `json:"organization_id,omitempty"`
	MembershipID       ID              `json:"membership_id,omitempty"`
	AgentID            ID              `json:"agent_id,omitempty"`
	AgentInstanceID    ID              `json:"agent_instance_id,omitempty"`
	Status             string          `json:"status"`
	ExpiresAt          time.Time       `json:"expires_at"`
	CreatedAt          time.Time       `json:"created_at"`
}
