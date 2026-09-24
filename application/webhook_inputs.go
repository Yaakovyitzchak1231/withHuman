package application

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// WebhookSigningSecretLabel is the associated data a sealed signing secret
// is bound to, so a sealed value copied under another endpoint key fails
// to open. The secret belongs to the key, not to one revision.
func WebhookSigningSecretLabel(organizationID domain.ID, endpointKey string) string {
	return "webhook_endpoint:" + string(organizationID) + ":" + endpointKey
}

func validWebhookEndpointKey(value string) error {
	return validRegistryKey("webhook endpoint key", value)
}

func validWebhookEndpointKeys(keys []string) error {
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if err := validWebhookEndpointKey(key); err != nil {
			return err
		}
		if _, duplicate := seen[key]; duplicate {
			return invalidField("webhook endpoint keys", "must be unique")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validWebhookURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return invalidField("webhook URL", "must be an absolute HTTP(S) URL")
	}
	return nil
}

// CreateWebhookEndpointRevisionInput persists the next immutable revision
// of an endpoint key. SealedSigningSecret is a freshly sealed secret the
// store keeps only when the key has none yet (its first revision); the
// result says whether it did, so the caller can show the secret once.
type CreateWebhookEndpointRevisionInput struct {
	ID                  domain.ID
	OrganizationID      domain.ID
	EndpointKey         string
	Name                string
	URL                 string
	SealedSigningSecret []byte
	CreatedByActorID    domain.ID
	Now                 time.Time
}

func (input CreateWebhookEndpointRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("webhook endpoint revision ID", input.ID),
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKey(input.EndpointKey),
		requiredString("webhook endpoint name", input.Name),
		validWebhookURL(input.URL),
		requiredBytes("sealed signing secret", input.SealedSigningSecret),
		requiredID("creator membership ID", input.CreatedByActorID),
		requiredTime("current time", input.Now),
	})
}

// CreatedWebhookEndpointRevision is the stored revision plus whether this
// creation minted the key's signing secret.
type CreatedWebhookEndpointRevision struct {
	Revision      domain.WebhookEndpointRevision
	SecretCreated bool
}

type ListWebhookEndpointSummariesInput struct {
	OrganizationID domain.ID
	Limit          int
}

func (input ListWebhookEndpointSummariesInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		nonNegative("limit", int64(input.Limit)),
	})
}

type ListWebhookEndpointRevisionsInput struct {
	OrganizationID domain.ID
	EndpointKey    string
	Limit          int
	// Cursor is the NextCursor of the previous page; empty for the first.
	Cursor string
}

func (input ListWebhookEndpointRevisionsInput) Validate() error {
	_, cursorErr := DecodeRevisionCursor(input.Cursor)
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKey(input.EndpointKey),
		nonNegative("limit", int64(input.Limit)),
		cursorErr,
	})
}

type GetWebhookEndpointRevisionInput struct {
	OrganizationID domain.ID
	EndpointKey    string
	Revision       int64
}

func (input GetWebhookEndpointRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKey(input.EndpointKey),
		positiveRevision(input.Revision),
	})
}

type GetActiveWebhookEndpointRevisionInput struct {
	OrganizationID domain.ID
	EndpointKey    string
}

func (input GetActiveWebhookEndpointRevisionInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKey(input.EndpointKey),
	})
}

// ActivateWebhookEndpointRevisionInput switches the active revision for
// one endpoint key. A nil ExpectedActiveRevision means the caller expects
// no revision to be active.
type ActivateWebhookEndpointRevisionInput struct {
	OrganizationID         domain.ID
	EndpointKey            string
	Revision               int64
	ExpectedActiveRevision *int64
	ActorID                domain.ID
	Now                    time.Time
}

func (input ActivateWebhookEndpointRevisionInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKey(input.EndpointKey),
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

type DeactivateWebhookEndpointInput struct {
	OrganizationID         domain.ID
	EndpointKey            string
	ExpectedActiveRevision int64
	ActorID                domain.ID
	Now                    time.Time
}

func (input DeactivateWebhookEndpointInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKey(input.EndpointKey),
		positiveRevision(input.ExpectedActiveRevision),
		requiredID("actor actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

// ListWebhookEndpointUsesInput asks which active pipeline revisions post
// to each of the named keys; the answer is keyed by endpoint key and
// omits keys nothing references.
type ListWebhookEndpointUsesInput struct {
	OrganizationID domain.ID
	EndpointKeys   []string
}

func (input ListWebhookEndpointUsesInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKeys(input.EndpointKeys),
	})
}

// ResolveWebhookEndpointsInput asks which of the named endpoint keys exist
// and which have an active revision. Pipeline revisions consult it:
// creation and preview need every key to exist, activation needs every
// key active.
type ResolveWebhookEndpointsInput struct {
	OrganizationID domain.ID
	EndpointKeys   []string
}

func (input ResolveWebhookEndpointsInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKeys(input.EndpointKeys),
	})
}

type ResolvedWebhookEndpoints struct {
	// Missing keys have no revision at all; Inactive keys exist without
	// an active revision.
	Missing  []string
	Inactive []string
}

type RotateWebhookSigningSecretInput struct {
	OrganizationID      domain.ID
	EndpointKey         string
	SealedSigningSecret []byte
	ActorID             domain.ID
	Now                 time.Time
}

func (input RotateWebhookSigningSecretInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKey(input.EndpointKey),
		requiredBytes("sealed signing secret", input.SealedSigningSecret),
		requiredID("acting actor ID", input.ActorID),
		requiredTime("current time", input.Now),
	})
}

// WebhookSigningSecretState is the key-level secret bookkeeping the
// product may see: when it was last minted, never the value.
type WebhookSigningSecretState struct {
	EndpointKey string
	RotatedAt   time.Time
}

type OpenWebhookSigningSecretInput struct {
	OrganizationID domain.ID
	EndpointKey    string
}

func (input OpenWebhookSigningSecretInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		validWebhookEndpointKey(input.EndpointKey),
	})
}

// RecordWebhookEndpointTestInput appends the audit record of one test
// delivery to a revision. Data carries safe fields only: status, latency,
// and whether the answer parsed.
type RecordWebhookEndpointTestInput struct {
	OrganizationID domain.ID
	RevisionID     domain.ID
	ActorID        domain.ID
	Data           json.RawMessage
	Now            time.Time
}

func (input RecordWebhookEndpointTestInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("webhook endpoint revision ID", input.RevisionID),
		requiredID("acting actor ID", input.ActorID),
		validJSON("test data", input.Data),
		requiredTime("current time", input.Now),
	})
}

func requiredBytes(field string, value []byte) error {
	if len(value) == 0 {
		return invalidField(field, "is required")
	}
	return nil
}
