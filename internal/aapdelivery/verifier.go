// Package aapdelivery is the provider side of AAP asynchronous mode: it
// verifies an instance's proposed webhook receiver before the instance
// exists, and delivers the signed request.resolved events the decision
// transactions queue. An event only says that a request is terminal; the
// harness reads the decision through the ordinary authenticated API.
package aapdelivery

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/secrets"
	"github.com/withHumanAI/withHuman/internal/webhookdelivery"
	"github.com/withHumanAI/withHuman/loggermdc"
)

// AttemptTimeout bounds every verification and notification attempt, the
// protocol's ten-second limit.
const AttemptTimeout = 10 * time.Second

// Verifier implements application.InstanceDeliveryVerifier.
type Verifier struct {
	client *webhookdelivery.Client
	policy webhookdelivery.Policy
	sealer *secrets.Sealer
	clock  application.Clock
	logger *slog.Logger
}

// NewVerifier composes a verifier. A nil sealer (no secrets key) refuses
// every delivery as unconfigured, since the secret could not be stored.
func NewVerifier(client *webhookdelivery.Client, policy webhookdelivery.Policy, sealer *secrets.Sealer, clock application.Clock, logger *slog.Logger) *Verifier {
	return &Verifier{client: client, policy: policy, sealer: sealer, clock: clock, logger: logger}
}

type verificationMessage struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	RegistrationID string `json:"registration_id"`
	InstanceID     string `json:"instance_id"`
	CreatedAt      string `json:"created_at"`
	Challenge      string `json:"challenge"`
}

// VerifyInstanceDelivery checks the proposed configuration, sends one
// signed webhook.verification to the URL, and requires a 200 whose body
// echoes the challenge. Only then is the secret sealed for storage.
func (v *Verifier) VerifyInstanceDelivery(ctx context.Context, input application.VerifyInstanceDeliveryInput) (application.AgentInstanceDeliveryRecord, error) {
	if err := input.Validate(); err != nil {
		return application.AgentInstanceDeliveryRecord{}, err
	}
	if v.sealer == nil {
		return application.AgentInstanceDeliveryRecord{}, domain.ErrWebhookUnconfigured
	}
	proposed := input.Delivery
	if proposed.Type != application.InstanceDeliveryWebhook {
		return application.AgentInstanceDeliveryRecord{}, fmt.Errorf("delivery type %q: %w", proposed.Type, domain.ErrUnsupportedDeliveryType)
	}
	if _, err := v.policy.ValidateURL(proposed.URL); err != nil {
		return application.AgentInstanceDeliveryRecord{}, fmt.Errorf("delivery URL is not allowed (%v): %w", err, domain.ErrInvalidWebhookURL)
	}
	if _, err := uuid.Parse(string(proposed.RegistrationID)); err != nil {
		return application.AgentInstanceDeliveryRecord{}, fmt.Errorf("delivery registration_id must be a UUID: %w", domain.ErrInvalid)
	}
	if _, err := webhookdelivery.StandardSigningKey(proposed.SigningSecret); err != nil {
		return application.AgentInstanceDeliveryRecord{}, fmt.Errorf("delivery signing_secret: %v: %w", err, domain.ErrInvalid)
	}
	ctx = loggermdc.With(ctx, loggermdc.Fields{
		loggermdc.KeyOrganizationID:  string(input.OrganizationID),
		loggermdc.KeyAgentInstanceID: string(input.InstanceID),
	})
	now := v.clock.Now().UTC()
	message := verificationMessage{
		ID: uuid.NewString(), Type: "webhook.verification", RegistrationID: string(proposed.RegistrationID),
		InstanceID: string(input.InstanceID), CreatedAt: now.Format(time.RFC3339Nano), Challenge: uuid.NewString(),
	}
	body, err := json.Marshal(message)
	if err != nil {
		return application.AgentInstanceDeliveryRecord{}, err
	}
	headers, err := webhookdelivery.StandardHeaders(proposed.SigningSecret, message.ID, now, body)
	if err != nil {
		return application.AgentInstanceDeliveryRecord{}, err
	}
	response, err := v.client.Deliver(ctx, webhookdelivery.Delivery{URL: proposed.URL, Headers: headers, Body: body, Timeout: AttemptTimeout})
	if err != nil {
		v.logger.WarnContext(ctx, "AAP receiver verification failed; the receiver could not be reached, so no instance was created", "error", err)
		return application.AgentInstanceDeliveryRecord{}, fmt.Errorf("the receiver could not be reached: %w", domain.ErrWebhookVerificationFailed)
	}
	if response.StatusCode != http.StatusOK {
		v.logger.WarnContext(ctx, "AAP receiver verification failed; the receiver did not answer 200, so no instance was created", "status_code", response.StatusCode)
		return application.AgentInstanceDeliveryRecord{}, fmt.Errorf("the receiver answered %d instead of 200: %w", response.StatusCode, domain.ErrWebhookVerificationFailed)
	}
	var answer struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(response.Body, &answer); err != nil ||
		subtle.ConstantTimeCompare([]byte(answer.Challenge), []byte(message.Challenge)) != 1 {
		v.logger.WarnContext(ctx, "AAP receiver verification failed; the receiver did not echo the challenge, so no instance was created")
		return application.AgentInstanceDeliveryRecord{}, fmt.Errorf("the receiver did not echo the challenge: %w", domain.ErrWebhookVerificationFailed)
	}
	sealed, err := v.sealer.Seal(application.InstanceDeliverySigningSecretLabel(input.OrganizationID, input.InstanceID), proposed.SigningSecret)
	if err != nil {
		return application.AgentInstanceDeliveryRecord{}, err
	}
	v.logger.InfoContext(ctx, "AAP receiver verification succeeded")
	return application.AgentInstanceDeliveryRecord{
		URL: proposed.URL, RegistrationID: proposed.RegistrationID, SealedSigningSecret: sealed, VerifiedAt: now,
	}, nil
}

// IssueRegistrationSecret generates the signing secret of a registration
// started in the app and seals it under the registration's label. The
// plaintext goes back to the person once and is never stored.
func (v *Verifier) IssueRegistrationSecret(organizationID, registrationID domain.ID) (string, []byte, error) {
	if v.sealer == nil {
		return "", nil, domain.ErrWebhookUnconfigured
	}
	secret, err := webhookdelivery.NewStandardSecret()
	if err != nil {
		return "", nil, err
	}
	sealed, err := v.sealer.Seal(application.InstanceRegistrationSigningSecretLabel(organizationID, registrationID), secret)
	if err != nil {
		return "", nil, err
	}
	return secret, sealed, nil
}

// OpenRegistrationSecret opens a pending registration's signing secret
// for its verification attempt.
func (v *Verifier) OpenRegistrationSecret(organizationID, registrationID domain.ID, sealed []byte) (string, error) {
	if v.sealer == nil {
		return "", domain.ErrWebhookUnconfigured
	}
	return v.sealer.Open(application.InstanceRegistrationSigningSecretLabel(organizationID, registrationID), sealed)
}
