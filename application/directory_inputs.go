package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// DirectoryCommandRecord is the persisted, provider-neutral form of one
// normalized directory command. The JSON schemas of Data by command type:
//
//	upsert_user / deactivate_user: {"email","display_name","active","removed"}
//	upsert_group: {"name"}
//	remove_group: {}
//	replace_group_membership: {"member_external_id","member_email","operation"}
type DirectoryCommandRecord struct {
	Type       string          `json:"type"`
	ExternalID string          `json:"external_id"`
	Data       json.RawMessage `json:"data"`
}

// DirectoryRepository is core's generic directory-managed membership:
// the connection records an edition's directory sync keeps per tenant,
// and the transactional application of normalized directory commands to
// users, memberships, teams and their links. The provider event inbox,
// the connector and the workflows that feed commands here are the
// edition's (the hosted SCIM sync).
type DirectoryRepository interface {
	UpsertDirectoryConnection(ctx context.Context, input UpsertDirectoryConnectionInput) (domain.ID, error)
	ListDirectoryOrganizations(ctx context.Context) ([]domain.ID, error)
	// ApplyDirectoryCommands applies one batch in a single transaction,
	// every command an idempotent upsert, so redelivery and retries cannot
	// duplicate state. A batch that would remove the last owner answers
	// ErrDirectoryLastOwner, one that would provision past the seat quota
	// domain.ErrQuotaExceeded (nothing of it is applied), and one for an
	// organization that is not active ErrDirectoryOrganizationInactive.
	ApplyDirectoryCommands(ctx context.Context, input ApplyDirectoryCommandsInput) error
	UpdateDirectoryReconciliation(ctx context.Context, input UpdateDirectoryReconciliationInput) error
	// GetDirectorySyncState reports the reconciliation sweep's view of the
	// organization's active directory connection. No connection is the
	// zero value, not an error.
	GetDirectorySyncState(ctx context.Context, input GetDirectorySyncStateInput) (DirectorySyncState, error)
}

type UpsertDirectoryConnectionInput struct {
	OrganizationID     domain.ID
	AdapterKey         string
	ProviderResourceID string
	State              string
	Now                time.Time
}

func (input UpsertDirectoryConnectionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("adapter key", input.AdapterKey),
		requiredString("provider resource ID", input.ProviderResourceID),
		requiredString("state", input.State),
		requiredTime("current time", input.Now),
	})
}

// ErrDirectoryOrganizationInactive is the apply answer for a suspended or
// pending-deletion organization: nothing is applied, and the edition keeps
// the batch to re-sync after a resume.
var ErrDirectoryOrganizationInactive = errors.New("organization is not active; directory commands were not applied")

// ErrDirectoryLastOwner is the apply answer for a batch that would leave
// the organization without an owner; it is permanently unappliable.
var ErrDirectoryLastOwner = errors.New("directory command would remove the organization's last owner")

// ApplyDirectoryCommandsInput is one batch of an edition's normalized
// directory commands for one organization's directory connection.
type ApplyDirectoryCommandsInput struct {
	OrganizationID domain.ID
	// AdapterKey names the edition's connector; ConnectionExternalID the
	// provider-side directory (empty for the connection's default).
	AdapterKey           string
	ConnectionExternalID string
	Commands             []DirectoryCommandRecord
	// SeatQuota is enforced for every membership the batch creates. A
	// directory create past quota fails the batch rather than
	// provisioning silently.
	SeatQuota AllocationQuota
	Now       time.Time
}

func (input ApplyDirectoryCommandsInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("adapter key", input.AdapterKey),
		input.SeatQuota.Validate(),
		requiredTime("current time", input.Now),
	})
}

type UpdateDirectoryReconciliationInput struct {
	OrganizationID domain.ID
	AdapterKey     string
	Status         string
	Now            time.Time
}

func (input UpdateDirectoryReconciliationInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("adapter key", input.AdapterKey),
		requiredString("status", input.Status),
		requiredTime("current time", input.Now),
	})
}

type GetDirectorySyncStateInput struct {
	OrganizationID domain.ID
	AdapterKey     string
}

func (input GetDirectorySyncStateInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("adapter key", input.AdapterKey),
	})
}

// DirectorySyncState is the local record of the last reconciliation sweep
// over a directory connection: the sweep sets ReconciliationStatus to
// healthy or degraded and stamps LastSuccessfulSyncAt on success.
type DirectorySyncState struct {
	ReconciliationStatus string
	LastSuccessfulSyncAt *time.Time
}
