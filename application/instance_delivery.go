package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// InstanceDeliveryWebhook is the one AAP delivery type this provider
// offers.
const InstanceDeliveryWebhook = "webhook"

// RequestResolvedRetryWindow is how long the provider keeps retrying a
// request.resolved event after the decision it announces.
const RequestResolvedRetryWindow = 7 * 24 * time.Hour

// InstanceDeliverySigningSecretLabel is the associated data a sealed AAP
// receiver secret is bound to, so a sealed value copied to another
// instance fails to open.
func InstanceDeliverySigningSecretLabel(organizationID, instanceID domain.ID) string {
	return "agent_instance_delivery:" + string(organizationID) + ":" + string(instanceID)
}

// ProposedInstanceDelivery is the delivery configuration a provisioner
// submits with a new instance, secret included. The verifier checks its
// shape, since the destination policy lives with the delivery client.
type ProposedInstanceDelivery struct {
	Type           string
	URL            string
	RegistrationID domain.ID
	SigningSecret  string
}

// AgentInstanceDeliveryRecord is a verified receiver ready to store: the
// secret is sealed under the instance's label.
type AgentInstanceDeliveryRecord struct {
	URL                 string
	RegistrationID      domain.ID
	SealedSigningSecret []byte
	VerifiedAt          time.Time
}

func (record AgentInstanceDeliveryRecord) Validate() error {
	return firstError([]error{
		requiredString("delivery URL", record.URL),
		requiredID("delivery registration ID", record.RegistrationID),
		func() error {
			if len(record.SealedSigningSecret) == 0 {
				return invalidField("sealed delivery signing secret", "is required")
			}
			return nil
		}(),
		requiredTime("delivery verification time", record.VerifiedAt),
	})
}

// VerifyInstanceDeliveryInput asks the receiver at a proposed URL to
// prove it expects this registration before the instance exists.
type VerifyInstanceDeliveryInput struct {
	OrganizationID domain.ID
	InstanceID     domain.ID
	Delivery       ProposedInstanceDelivery
}

func (input VerifyInstanceDeliveryInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("agent instance ID", input.InstanceID),
	})
}

// InstanceDeliveryVerifier validates a proposed receiver, runs the
// signed challenge against it, and seals its secret for storage. It also
// issues the signing secret of a registration started in the app, sealed
// under the registration's label until the receiver is verified.
type InstanceDeliveryVerifier interface {
	VerifyInstanceDelivery(ctx context.Context, input VerifyInstanceDeliveryInput) (AgentInstanceDeliveryRecord, error)
	IssueRegistrationSecret(organizationID, registrationID domain.ID) (secret string, sealed []byte, err error)
	OpenRegistrationSecret(organizationID, registrationID domain.ID, sealed []byte) (string, error)
}

// RequestResolvedDelivery is one due request.resolved event with the
// receiver it goes to. The secret is still sealed.
type RequestResolvedDelivery struct {
	EventID             domain.ID
	OrganizationID      domain.ID
	AgentInstanceID     domain.ID
	RequestID           domain.ID
	CreatedAt           time.Time
	RetryUntil          time.Time
	AttemptCount        int
	URL                 string
	SealedSigningSecret []byte
}

// ClaimDueRequestResolvedEventsInput leases due events until LeaseUntil so
// concurrent workers never attempt the same event at once.
type ClaimDueRequestResolvedEventsInput struct {
	Now        time.Time
	LeaseUntil time.Time
	Limit      int
}

func (input ClaimDueRequestResolvedEventsInput) Validate() error {
	return firstError([]error{
		requiredTime("current time", input.Now),
		requiredTime("lease end", input.LeaseUntil),
		func() error {
			if !input.LeaseUntil.After(input.Now) {
				return invalidField("lease end", "must be after the current time")
			}
			return nil
		}(),
	})
}

// RecordRequestResolvedAttemptInput stores one attempt's outcome: the
// event is delivered, rescheduled at NextAttemptAt, or abandoned when
// neither applies.
type RecordRequestResolvedAttemptInput struct {
	EventID        domain.ID
	OrganizationID domain.ID
	AttemptedAt    time.Time
	StatusCode     int
	Delivered      bool
	NextAttemptAt  *time.Time
}

func (input RecordRequestResolvedAttemptInput) Validate() error {
	return firstError([]error{
		requiredID("event ID", input.EventID),
		requiredID("organization ID", input.OrganizationID),
		requiredTime("attempt time", input.AttemptedAt),
		func() error {
			if input.Delivered && input.NextAttemptAt != nil {
				return invalidField("next attempt time", "must be absent for a delivered event")
			}
			return nil
		}(),
	})
}

// RequestResolvedEventRepository is the durable queue of request.resolved
// events. Events are written by the decision transactions themselves.
type RequestResolvedEventRepository interface {
	ClaimDueRequestResolvedEvents(ctx context.Context, input ClaimDueRequestResolvedEventsInput) ([]RequestResolvedDelivery, error)
	RecordRequestResolvedAttempt(ctx context.Context, input RecordRequestResolvedAttemptInput) error
	PruneAAPState(ctx context.Context, input PruneAAPStateInput) error
}
