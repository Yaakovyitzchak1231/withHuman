package application

import (
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// AAPIdempotencyRetention is how long a provisioning or update key, and
// the result it recovers, is kept after it is first used.
const AAPIdempotencyRetention = 7 * 24 * time.Hour

func validIdempotencyKey(value string) error {
	if value == "" || len(value) > 255 {
		return invalidField("idempotency key", "must be 1 to 255 characters")
	}
	return nil
}

// ClaimProvisioningKeyInput takes the provisioning key for one attempt.
// The IDs and salt are used only when the key is new (or expired); a key
// already reserved keeps its own so every retry builds the same instance
// and credential.
type ClaimProvisioningKeyInput struct {
	ProvisionerID  domain.ID
	OrganizationID domain.ID
	IdempotencyKey string
	IntentHash     []byte
	InstanceID     domain.ID
	CredentialID   domain.ID
	CredentialSalt []byte
	Now            time.Time
	LeaseUntil     time.Time
}

func (input ClaimProvisioningKeyInput) Validate() error {
	return firstError([]error{
		requiredID("provisioner ID", input.ProvisionerID),
		requiredID("organization ID", input.OrganizationID),
		validIdempotencyKey(input.IdempotencyKey),
		requiredTokenHash("intent hash", input.IntentHash),
		requiredID("agent instance ID", input.InstanceID),
		requiredID("credential ID", input.CredentialID),
		func() error {
			if len(input.CredentialSalt) < 16 {
				return invalidField("credential salt", "must be at least 16 bytes")
			}
			return nil
		}(),
		requiredTime("current time", input.Now),
		requiredTime("lease end", input.LeaseUntil),
	})
}

// ProvisioningKeyClaim is the reservation an attempt proceeds with.
// Completed means the instance was already created under this key and
// the attempt is a replay.
type ProvisioningKeyClaim struct {
	InstanceID     domain.ID
	CredentialID   domain.ID
	CredentialSalt []byte
	Completed      bool
}

// ReleaseProvisioningKeyInput ends a failed attempt's lease so the key can
// be retried at once, for example after failed receiver verification.
type ReleaseProvisioningKeyInput struct {
	ProvisionerID  domain.ID
	IdempotencyKey string
}

func (input ReleaseProvisioningKeyInput) Validate() error {
	return firstError([]error{requiredID("provisioner ID", input.ProvisionerID), validIdempotencyKey(input.IdempotencyKey)})
}

// GetProvisionedInstanceInput reads an instance within a provisioner's
// agent. CredentialID, when set, also reports that credential's expiry
// for a replayed provisioning response.
type GetProvisionedInstanceInput struct {
	OrganizationID domain.ID
	AgentID        domain.ID
	InstanceID     domain.ID
	CredentialID   domain.ID
}

func (input GetProvisionedInstanceInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredID("agent instance ID", input.InstanceID),
	})
}

// ProvisionedInstance is an instance as a provisioner sees it, with its
// receiver and whether it was deleted.
type ProvisionedInstance struct {
	Instance            domain.AgentInstance
	Deleted             bool
	CredentialExpiresAt time.Time
}

// ClaimUpdateKeyInput takes an update key for one attempt against one
// instance.
type ClaimUpdateKeyInput struct {
	ProvisionerID  domain.ID
	OrganizationID domain.ID
	InstanceID     domain.ID
	IdempotencyKey string
	IntentHash     []byte
	Now            time.Time
	LeaseUntil     time.Time
}

func (input ClaimUpdateKeyInput) Validate() error {
	return firstError([]error{
		requiredID("provisioner ID", input.ProvisionerID),
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent instance ID", input.InstanceID),
		validIdempotencyKey(input.IdempotencyKey),
		requiredTokenHash("intent hash", input.IntentHash),
		requiredTime("current time", input.Now),
		requiredTime("lease end", input.LeaseUntil),
	})
}

// UpdateKeyClaim carries the stored response when the update already
// committed under this key.
type UpdateKeyClaim struct {
	Completed bool
	Response  json.RawMessage
}

type ReleaseUpdateKeyInput struct {
	ProvisionerID  domain.ID
	InstanceID     domain.ID
	IdempotencyKey string
}

func (input ReleaseUpdateKeyInput) Validate() error {
	return firstError([]error{
		requiredID("provisioner ID", input.ProvisionerID), requiredID("agent instance ID", input.InstanceID),
		validIdempotencyKey(input.IdempotencyKey),
	})
}

// InstanceDeliveryChange says what an update does to the receiver.
type InstanceDeliveryChange int

const (
	DeliveryUnchanged InstanceDeliveryChange = iota
	DeliveryReplaced
	DeliveryRemoved
)

// UpdateProvisionedInstanceInput applies a provisioner's update and
// completes its key with the response in the same transaction. Delivery
// is the verified replacement when DeliveryChange is DeliveryReplaced.
type UpdateProvisionedInstanceInput struct {
	ProvisionerID  domain.ID
	OrganizationID domain.ID
	AgentID        domain.ID
	InstanceID     domain.ID
	IdempotencyKey string
	Name           *string
	DeliveryChange InstanceDeliveryChange
	Delivery       *AgentInstanceDeliveryRecord
	Now            time.Time
}

func (input UpdateProvisionedInstanceInput) Validate() error {
	checks := []error{
		requiredID("provisioner ID", input.ProvisionerID),
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredID("agent instance ID", input.InstanceID),
		validIdempotencyKey(input.IdempotencyKey),
		requiredTime("current time", input.Now),
	}
	if input.Name != nil {
		checks = append(checks, requiredString("instance name", *input.Name))
	}
	if input.Name == nil && input.DeliveryChange == DeliveryUnchanged {
		checks = append(checks, invalidField("update", "must change the name or the delivery"))
	}
	if input.DeliveryChange == DeliveryReplaced {
		if input.Delivery == nil {
			checks = append(checks, invalidField("delivery", "is required to replace the receiver"))
		} else {
			checks = append(checks, input.Delivery.Validate())
		}
	}
	return firstError(checks)
}

// DeleteProvisionedInstanceInput retires an instance: disabled for the
// record, credentials revoked, receiver removed with its queued events,
// and every pending request cancelled without notifications.
type DeleteProvisionedInstanceInput struct {
	OrganizationID domain.ID
	AgentID        domain.ID
	InstanceID     domain.ID
	Now            time.Time
}

func (input DeleteProvisionedInstanceInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent ID", input.AgentID),
		requiredID("agent instance ID", input.InstanceID),
		requiredTime("current time", input.Now),
	})
}

// PruneAAPStateInput drops what AAP no longer requires: idempotency keys
// past retention (their credential salts with them) and undelivered
// events whose retry window closed while their instance was disabled.
type PruneAAPStateInput struct {
	Now time.Time
}

func (input PruneAAPStateInput) Validate() error { return requiredTime("current time", input.Now) }
