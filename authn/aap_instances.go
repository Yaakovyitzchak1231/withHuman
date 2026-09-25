package authn

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// aapKeyLease bounds how long one attempt holds an idempotency key. It
// outlasts a receiver verification (ten seconds) and the commit after it.
const aapKeyLease = time.Minute

// MintInstanceRequest is what a provisioner submits to create an instance.
type MintInstanceRequest struct {
	Token          string
	IdempotencyKey string
	InstanceName   string
	Metadata       json.RawMessage
	TTLSeconds     int64
	// Delivery configures AAP asynchronous mode. The receiver must pass
	// verification before the instance is created.
	Delivery *application.ProposedInstanceDelivery
}

// MintResult is a provisioned instance and its credential. Replayed means
// the idempotency key had already created it and nothing new was issued.
type MintResult struct {
	domain.AgentInstanceCredential
	Replayed bool
}

// MintAgentInstance registers a runtime and issues its credential using a
// provisioner token instead of a human authorization. The instance name is
// optional and the credential TTL is the smaller of the requested TTL and
// the configured credential TTL.
//
// The call is idempotent on the provisioner and IdempotencyKey. The first
// attempt reserves the instance and credential IDs; a receiver, when
// configured, is verified against that instance before anything is stored.
// The credential secret is derived from the provisioner's own secret and a
// per-key salt rather than stored, so a replay presenting the same token
// returns the original credential while only its hash is persisted.
func (s *Service) MintAgentInstance(ctx context.Context, request MintInstanceRequest) (MintResult, error) {
	provisioner, provisionerSecret, err := s.authenticateProvisioner(ctx, request.Token)
	if err != nil {
		return MintResult{}, err
	}
	if request.IdempotencyKey == "" {
		return MintResult{}, fmt.Errorf("Idempotency-Key header is required: %w", domain.ErrInvalid)
	}
	if request.TTLSeconds < 0 {
		return MintResult{}, fmt.Errorf("requested TTL must not be negative: %w", domain.ErrInvalid)
	}
	metadata, err := normalizeMetadata(request.Metadata)
	if err != nil {
		return MintResult{}, err
	}
	name := strings.TrimSpace(request.InstanceName)
	intent, err := intentHash(map[string]any{
		"instance_name": name, "instance_metadata": metadata, "ttl_seconds": request.TTLSeconds,
		"delivery": deliveryIntent(request.Delivery),
	})
	if err != nil {
		return MintResult{}, err
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return MintResult{}, err
	}
	now := s.clock.Now()
	claim, err := s.agents.ClaimProvisioningKey(ctx, application.ClaimProvisioningKeyInput{
		ProvisionerID: provisioner.ID, OrganizationID: provisioner.OrganizationID, IdempotencyKey: request.IdempotencyKey,
		IntentHash: intent, InstanceID: domain.ID(uuid.NewString()), CredentialID: domain.ID(uuid.NewString()),
		CredentialSalt: salt, Now: now, LeaseUntil: now.Add(aapKeyLease),
	})
	if err != nil {
		return MintResult{}, err
	}
	secret := provisionedCredentialSecret(provisionerSecret, claim.CredentialID, claim.CredentialSalt)
	token := "whc_" + string(claim.CredentialID) + "_" + secret
	if claim.Completed {
		view, err := s.agents.GetProvisionedInstance(ctx, application.GetProvisionedInstanceInput{
			OrganizationID: provisioner.OrganizationID, AgentID: provisioner.AgentID, InstanceID: claim.InstanceID, CredentialID: claim.CredentialID,
		})
		if err != nil {
			return MintResult{}, err
		}
		if view.Deleted {
			return MintResult{}, domain.ErrInstanceDeleted
		}
		return MintResult{Replayed: true, AgentInstanceCredential: domain.AgentInstanceCredential{
			AgentInstance: view.Instance,
			Credential:    domain.IssuedCredential{ID: claim.CredentialID, Token: token, ExpiresAt: view.CredentialExpiresAt},
		}}, nil
	}
	release := func(cause error) (MintResult, error) {
		if err := s.agents.ReleaseProvisioningKey(ctx, application.ReleaseProvisioningKeyInput{
			ProvisionerID: provisioner.ID, IdempotencyKey: request.IdempotencyKey,
		}); err != nil {
			return MintResult{}, fmt.Errorf("%w (and releasing the idempotency key failed: %v)", cause, err)
		}
		return MintResult{}, cause
	}
	var delivery *application.AgentInstanceDeliveryRecord
	if request.Delivery != nil {
		verified, err := s.verifyDelivery(ctx, provisioner.OrganizationID, claim.InstanceID, *request.Delivery)
		if err != nil {
			return release(err)
		}
		delivery = &verified
	}
	ttl := s.config.CredentialTTL
	if requested := time.Duration(request.TTLSeconds) * time.Second; requested > 0 && requested < ttl {
		ttl = requested
	}
	issuedAt := s.clock.Now()
	record := application.AgentCredentialRecord{ID: claim.CredentialID, SecretHash: HashToken(secret), IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(ttl)}
	instance, err := s.agents.MintAgentInstance(ctx, application.MintAgentInstanceInput{
		ProvisionerID: provisioner.ID, SecretHash: HashToken(provisionerSecret), IdempotencyKey: request.IdempotencyKey,
		InstanceID: claim.InstanceID, InstanceName: name, Metadata: metadata, Credential: record, Delivery: delivery, Now: issuedAt,
	})
	if err != nil {
		return release(err)
	}
	return MintResult{AgentInstanceCredential: domain.AgentInstanceCredential{
		AgentInstance: instance,
		Credential:    domain.IssuedCredential{ID: claim.CredentialID, Token: token, ExpiresAt: record.ExpiresAt},
	}}, nil
}

// GetProvisionedInstance reads an instance of the provisioner's agent,
// with its receiver. A deleted instance reads as absent.
func (s *Service) GetProvisionedInstance(ctx context.Context, token string, instanceID domain.ID) (domain.AgentInstance, error) {
	provisioner, _, err := s.authenticateProvisioner(ctx, token)
	if err != nil {
		return domain.AgentInstance{}, err
	}
	view, err := s.agents.GetProvisionedInstance(ctx, application.GetProvisionedInstanceInput{
		OrganizationID: provisioner.OrganizationID, AgentID: provisioner.AgentID, InstanceID: instanceID,
	})
	if err != nil {
		return domain.AgentInstance{}, err
	}
	if view.Deleted {
		return domain.AgentInstance{}, domain.ErrNotFound
	}
	return view.Instance, nil
}

// UpdateInstanceRequest renames an instance and replaces or removes its
// receiver. Delivery is the replacement when DeliveryChange replaces it.
type UpdateInstanceRequest struct {
	Token          string
	IdempotencyKey string
	InstanceID     domain.ID
	Name           *string
	DeliveryChange application.InstanceDeliveryChange
	Delivery       *application.ProposedInstanceDelivery
}

// UpdateProvisionedInstance applies a provisioner's update, idempotently on
// the provisioner, instance and key: a replay answers the stored response
// without applying anything again. A replacement receiver is verified
// before any part of the update commits, so a failure leaves the old
// configuration and name in place.
func (s *Service) UpdateProvisionedInstance(ctx context.Context, request UpdateInstanceRequest) (domain.AgentInstance, error) {
	provisioner, _, err := s.authenticateProvisioner(ctx, request.Token)
	if err != nil {
		return domain.AgentInstance{}, err
	}
	if request.IdempotencyKey == "" {
		return domain.AgentInstance{}, fmt.Errorf("Idempotency-Key header is required: %w", domain.ErrInvalid)
	}
	if request.Name != nil {
		trimmed := strings.TrimSpace(*request.Name)
		request.Name = &trimmed
	}
	view, err := s.agents.GetProvisionedInstance(ctx, application.GetProvisionedInstanceInput{
		OrganizationID: provisioner.OrganizationID, AgentID: provisioner.AgentID, InstanceID: request.InstanceID,
	})
	if err != nil {
		return domain.AgentInstance{}, err
	}
	if view.Deleted {
		return domain.AgentInstance{}, domain.ErrNotFound
	}
	intent, err := intentHash(map[string]any{
		"instance_name": request.Name, "delivery_change": request.DeliveryChange, "delivery": deliveryIntent(request.Delivery),
	})
	if err != nil {
		return domain.AgentInstance{}, err
	}
	now := s.clock.Now()
	claim, err := s.agents.ClaimUpdateKey(ctx, application.ClaimUpdateKeyInput{
		ProvisionerID: provisioner.ID, OrganizationID: provisioner.OrganizationID, InstanceID: request.InstanceID,
		IdempotencyKey: request.IdempotencyKey, IntentHash: intent, Now: now, LeaseUntil: now.Add(aapKeyLease),
	})
	if err != nil {
		return domain.AgentInstance{}, err
	}
	if claim.Completed {
		var instance domain.AgentInstance
		if err := json.Unmarshal(claim.Response, &instance); err != nil {
			return domain.AgentInstance{}, err
		}
		return instance, nil
	}
	release := func(cause error) (domain.AgentInstance, error) {
		if err := s.agents.ReleaseUpdateKey(ctx, application.ReleaseUpdateKeyInput{
			ProvisionerID: provisioner.ID, InstanceID: request.InstanceID, IdempotencyKey: request.IdempotencyKey,
		}); err != nil {
			return domain.AgentInstance{}, fmt.Errorf("%w (and releasing the idempotency key failed: %v)", cause, err)
		}
		return domain.AgentInstance{}, cause
	}
	var delivery *application.AgentInstanceDeliveryRecord
	if request.DeliveryChange == application.DeliveryReplaced {
		if request.Delivery == nil {
			return release(fmt.Errorf("delivery is required to replace the receiver: %w", domain.ErrInvalid))
		}
		verified, err := s.verifyDelivery(ctx, provisioner.OrganizationID, request.InstanceID, *request.Delivery)
		if err != nil {
			return release(err)
		}
		delivery = &verified
	}
	instance, err := s.agents.UpdateProvisionedInstance(ctx, application.UpdateProvisionedInstanceInput{
		ProvisionerID: provisioner.ID, OrganizationID: provisioner.OrganizationID, AgentID: provisioner.AgentID,
		InstanceID: request.InstanceID, IdempotencyKey: request.IdempotencyKey, Name: request.Name,
		DeliveryChange: request.DeliveryChange, Delivery: delivery, Now: s.clock.Now(),
	})
	if err != nil {
		return release(err)
	}
	return instance, nil
}

// DeleteProvisionedInstance retires an instance of the provisioner's agent
// and answers the organization and the requests it cancelled, so the
// caller can stand their workflows down. Deleting again succeeds.
func (s *Service) DeleteProvisionedInstance(ctx context.Context, token string, instanceID domain.ID) (domain.ID, []domain.ID, error) {
	provisioner, _, err := s.authenticateProvisioner(ctx, token)
	if err != nil {
		return "", nil, err
	}
	cancelled, err := s.agents.DeleteProvisionedInstance(ctx, application.DeleteProvisionedInstanceInput{
		OrganizationID: provisioner.OrganizationID, AgentID: provisioner.AgentID, InstanceID: instanceID, Now: s.clock.Now(),
	})
	return provisioner.OrganizationID, cancelled, err
}

func (s *Service) authenticateProvisioner(ctx context.Context, token string) (domain.AgentProvisioner, string, error) {
	provisionerID, secret, err := ParseOpaqueToken(token, "whp")
	if err != nil {
		return domain.AgentProvisioner{}, "", domain.ErrUnauthenticated
	}
	provisioner, err := s.agents.AuthenticateAgentProvisioner(ctx, application.AuthenticateAgentProvisionerInput{
		ProvisionerID: domain.ID(provisionerID), SecretHash: HashToken(secret), Now: s.clock.Now(),
	})
	return provisioner, secret, err
}

func (s *Service) verifyDelivery(ctx context.Context, organizationID, instanceID domain.ID, delivery application.ProposedInstanceDelivery) (application.AgentInstanceDeliveryRecord, error) {
	if s.deliveries == nil {
		return application.AgentInstanceDeliveryRecord{}, domain.ErrWebhookUnconfigured
	}
	return s.deliveries.VerifyInstanceDelivery(ctx, application.VerifyInstanceDeliveryInput{
		OrganizationID: organizationID, InstanceID: instanceID, Delivery: delivery,
	})
}

// provisionedCredentialSecret derives an instance credential's secret
// from the provisioner secret that minted it. Only the caller presenting
// that token, while the key's salt is kept, can derive it again.
func provisionedCredentialSecret(provisionerSecret string, credentialID domain.ID, salt []byte) string {
	mac := hmac.New(sha256.New, []byte(provisionerSecret))
	mac.Write([]byte("withhuman:aap-instance-credential:" + string(credentialID) + ":"))
	mac.Write(salt)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// deliveryIntent is the part of a proposed receiver an idempotency key
// compares: the secret only as a hash, so it is never stored.
func deliveryIntent(delivery *application.ProposedInstanceDelivery) any {
	if delivery == nil {
		return nil
	}
	secret := sha256.Sum256([]byte(delivery.SigningSecret))
	return map[string]any{
		"type": delivery.Type, "url": delivery.URL, "registration_id": delivery.RegistrationID,
		"signing_secret_sha256": base64.StdEncoding.EncodeToString(secret[:]),
	}
}

// intentHash hashes the parsed values of a submission, so whitespace and
// object key order in the JSON never make a replay look different.
func intentHash(intent map[string]any) ([]byte, error) {
	encoded, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(canonical)
	return hash[:], nil
}
