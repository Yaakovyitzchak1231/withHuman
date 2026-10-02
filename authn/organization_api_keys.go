package authn

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

type OrganizationAPIKeyRequest struct {
	Name      string                        `json:"name"`
	Policies  []domain.PermissionPolicySpec `json:"permission_policies"`
	ExpiresAt *time.Time                    `json:"expires_at"`
}

type OrganizationAPIKeyOptions struct {
	Allowed     bool                          `json:"organization_api_keys_allowed"`
	Resources   []domain.PermissionResource   `json:"resources"`
	Permissions []domain.PermissionDefinition `json:"-"`
	MaxTTL      string                        `json:"max_ttl"`
}

func (s *Service) OrganizationAPIKeyOptions(ctx context.Context, principal domain.Principal) (OrganizationAPIKeyOptions, error) {
	if s.apiKeys == nil {
		return OrganizationAPIKeyOptions{}, domain.ErrNotFound
	}
	if principal.Auth.Method == domain.AuthMethodAPIKey {
		return OrganizationAPIKeyOptions{}, domain.ErrAPIKeyNotAllowed
	}
	if err := requireHuman(principal, domain.PermissionAPIKeyOrganizationIssue, domain.OrganizationResource()); err != nil {
		return OrganizationAPIKeyOptions{}, err
	}
	policy, err := s.identities.GetOrganizationPolicy(ctx, application.GetOrganizationPolicyInput{OrganizationID: principal.OrganizationID})
	if err != nil {
		return OrganizationAPIKeyOptions{}, err
	}
	options := OrganizationAPIKeyOptions{Allowed: policy.OrganizationAPIKeysAllowed, Resources: []domain.PermissionResource{}, MaxTTL: s.config.APIKeyMaxTTL.String()}

	scopes, err := s.apiKeys.ListAPIKeyScopes(ctx, application.ListAPIKeyScopesInput{OrganizationID: principal.OrganizationID})
	if err != nil {
		return OrganizationAPIKeyOptions{}, err
	}
	for _, resource := range scopes {
		if authorization.Check(principal, domain.PermissionPolicyWrite, (domain.Scope{Kind: resource.Kind, ID: resource.ID}).Resource()) == nil {
			options.Resources = append(options.Resources, resource)
		}
	}

	for _, definition := range domain.Permissions() {
		if definition.HeldBy(domain.HolderOrganizationAPIKey) && authorization.HoldsAnywhere(principal, definition.Key) {
			options.Permissions = append(options.Permissions, definition)
		}
	}
	return options, nil
}

func (s *Service) CreateOrganizationAPIKey(ctx context.Context, principal domain.Principal, request OrganizationAPIKeyRequest, idempotencyKey string) (domain.APIKey, string, error) {
	if s.apiKeys == nil {
		return domain.APIKey{}, "", domain.ErrNotFound
	}
	if principal.Auth.Method == domain.AuthMethodAPIKey {
		return domain.APIKey{}, "", domain.ErrAPIKeyNotAllowed
	}
	if err := requireHuman(principal, domain.PermissionAPIKeyOrganizationIssue, domain.OrganizationResource()); err != nil {
		return domain.APIKey{}, "", err
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > maxAPIKeyNameLength {
		return domain.APIKey{}, "", fmt.Errorf("name must contain 1 to 100 characters: %w", domain.ErrInvalid)
	}
	if err := domain.ValidateOrganizationKeyPolicies(principal, request.Policies); err != nil {
		return domain.APIKey{}, "", err
	}
	request.Policies = slices.Clone(request.Policies)
	for i, policy := range request.Policies {
		policy.Permissions = slices.Clone(policy.Permissions)
		slices.Sort(policy.Permissions)
		policy.ResourceIDs = slices.Clone(policy.ResourceIDs)
		slices.Sort(policy.ResourceIDs)
		request.Policies[i] = policy
	}
	slices.SortFunc(request.Policies, func(a, b domain.PermissionPolicySpec) int {
		left, _ := json.Marshal(a)
		right, _ := json.Marshal(b)
		return strings.Compare(string(left), string(right))
	})
	payload, err := json.Marshal(request)
	if err != nil {
		return domain.APIKey{}, "", err
	}
	fingerprint := sha256.Sum256(payload)
	now := s.clock.Now()
	expiresAt, err := s.apiKeyExpiry(request.ExpiresAt, now)
	if err != nil {
		return domain.APIKey{}, "", err
	}
	policy, err := s.identities.GetOrganizationPolicy(ctx, application.GetOrganizationPolicyInput{OrganizationID: principal.OrganizationID})
	if err != nil {
		return domain.APIKey{}, "", err
	}
	if !policy.OrganizationAPIKeysAllowed {
		return domain.APIKey{}, "", domain.ErrAPIKeysDisabled
	}
	if rejection := authorization.RequireFresh(principal.Auth, policy.ApprovalReauthWindow, policy.MFAMode, now); rejection != nil {
		return domain.APIKey{}, "", rejection.Err
	}
	keyID := domain.ID(uuid.NewString())
	token, hash, err := MintOpaqueToken(domain.OrganizationAPIKeyPrefix, keyID)
	if err != nil {
		return domain.APIKey{}, "", err
	}
	key, err := s.apiKeys.CreateAPIKey(ctx, application.CreateAPIKeyInput{Kind: domain.APIKeyOrganization, Actor: principal, KeyID: keyID, OrganizationID: principal.OrganizationID, MembershipID: principal.MembershipID, Name: request.Name, Policies: request.Policies, SecretHash: hash, AssuranceAtIssue: principal.Auth.Assurance, Now: now, ExpiresAt: expiresAt, IdempotencyKey: idempotencyKey, Fingerprint: fingerprint[:]})
	if err != nil {
		return domain.APIKey{}, "", err
	}
	if key.ID != keyID {
		token = ""
	}
	return key, token, nil
}

func (s *Service) GetAPIKey(ctx context.Context, principal domain.Principal, id domain.ID) (domain.APIKey, error) {
	if s.apiKeys == nil {
		return domain.APIKey{}, domain.ErrNotFound
	}
	if principal.Auth.Method == domain.AuthMethodAPIKey {
		return domain.APIKey{}, domain.ErrAPIKeyNotAllowed
	}
	if err := requireHuman(principal, domain.PermissionAPIKeyRead, domain.OrganizationResource()); err != nil {
		return domain.APIKey{}, err
	}
	return s.apiKeys.GetAPIKey(ctx, application.GetAPIKeyInput{OrganizationID: principal.OrganizationID, KeyID: id})
}

// RecordAPIKeyUse audits one authenticated call; the service stamps the time.
func (s *Service) RecordAPIKeyUse(ctx context.Context, input application.RecordAPIKeyUseInput) error {
	if s.apiKeys == nil {
		return domain.ErrNotFound
	}
	input.Now = s.clock.Now()
	return s.apiKeys.RecordAPIKeyUse(ctx, input)
}
