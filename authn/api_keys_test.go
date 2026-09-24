package authn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
)

// apiKeyRepositoryStub records the inputs the service hands the store
// and answers with a key shaped from them.
type apiKeyRepositoryStub struct {
	created  []application.CreateAPIKeyInput
	listed   []application.ListAPIKeysInput
	revoked  []application.RevokeAPIKeyInput
	resolved []application.ResolveAPIKeyInput
}

func (r *apiKeyRepositoryStub) CreateAPIKey(_ context.Context, input application.CreateAPIKeyInput) (domain.APIKey, error) {
	if err := input.Validate(); err != nil {
		return domain.APIKey{}, err
	}
	r.created = append(r.created, input)
	return domain.APIKey{
		ID: input.KeyID, OrganizationID: input.OrganizationID, Kind: domain.APIKeyPersonal, MembershipID: input.MembershipID,
		Name: input.Name, Permissions: input.Permissions, AssuranceAtIssue: input.AssuranceAtIssue,
		CreatedAt: input.Now, ExpiresAt: input.ExpiresAt,
	}, nil
}

func (r *apiKeyRepositoryStub) ListAPIKeys(_ context.Context, input application.ListAPIKeysInput) ([]domain.APIKey, error) {
	r.listed = append(r.listed, input)
	return []domain.APIKey{}, nil
}

func (r *apiKeyRepositoryStub) GetAPIKey(_ context.Context, input application.GetAPIKeyInput) (domain.APIKey, error) {
	return domain.APIKey{ID: input.KeyID, OrganizationID: input.OrganizationID, Kind: domain.APIKeyPersonal}, nil
}

func (r *apiKeyRepositoryStub) RevokeAPIKey(_ context.Context, input application.RevokeAPIKeyInput) error {
	r.revoked = append(r.revoked, input)
	return nil
}

func (r *apiKeyRepositoryStub) ResolveAPIKey(_ context.Context, input application.ResolveAPIKeyInput) (domain.Principal, error) {
	r.resolved = append(r.resolved, input)
	return domain.Principal{ActorType: domain.ActorHuman, Auth: domain.AuthContext{Method: domain.AuthMethodAPIKey, CredentialID: input.KeyID}}, nil
}

func apiKeyTestService(t *testing.T, config ServiceConfig, plan application.Entitlements, policy domain.OrganizationPolicy) (*Service, *apiKeyRepositoryStub, time.Time) {
	t.Helper()
	now := time.Unix(1_000_000, 0).UTC()
	if plan == nil {
		plan = entitlements.NewUnlimited()
	}
	repository := &identityRepositoryStub{policy: policy}
	service := NewService(repository, nil, nil, nil, plan, passwordHasherStub{}, serviceClock{now}, config)
	keys := &apiKeyRepositoryStub{}
	service.RegisterAPIKeys(keys)
	return service, keys, now
}

func apiKeyPrincipal(role domain.Role, auth domain.AuthContext) domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1", Auth: auth,
	}.WithGrants(domain.GrantsForRoles([]domain.Role{role}))
}

func freshSession(now time.Time) domain.AuthContext {
	return domain.AuthContext{Method: "password", Assurance: "strong", AuthenticatedAt: now.Add(-time.Minute), SessionID: "session-1"}
}

func openPolicy() domain.OrganizationPolicy {
	return domain.OrganizationPolicy{
		PersonalAPIKeysAllowed: true, MFAMode: domain.MFAOptional, ApprovalReauthWindow: time.Hour,
	}
}

// The open edition's key inherits everything: no permission list is
// stored, the token carries the whk_ prefix and the key id, and the
// session's assurance is remembered.
func TestCreateAPIKeyInheritsTheMembersRole(t *testing.T) {
	service, keys, now := apiKeyTestService(t, ServiceConfig{}, nil, openPolicy())
	principal := apiKeyPrincipal(domain.RoleAuditor, freshSession(now))

	key, token, err := service.CreateAPIKey(context.Background(), principal, CreateAPIKeyRequest{Name: "  audit export  "})
	if err != nil {
		t.Fatal(err)
	}
	if key.Name != "audit export" || key.Permissions != nil || key.ExpiresAt != nil || key.AssuranceAtIssue != "strong" {
		t.Fatalf("key = %+v", key)
	}
	id, secret, err := ParseOpaqueToken(token, domain.APIKeyPrefix)
	if err != nil || domain.ID(id) != key.ID || secret == "" {
		t.Fatalf("token %q did not parse back to the key: %v", token, err)
	}
	if string(keys.created[0].SecretHash) != string(HashToken(secret)) {
		t.Fatal("stored hash is not the hash of the token's secret")
	}
	if !IsAPIKeyToken(token) || IsAPIKeyToken("whc_x_y") {
		t.Fatal("IsAPIKeyToken misclassifies")
	}
}

func TestCreateAPIKeyRefusesWhenTheOrganizationTurnedKeysOff(t *testing.T) {
	policy := openPolicy()
	policy.PersonalAPIKeysAllowed = false
	service, _, now := apiKeyTestService(t, ServiceConfig{}, nil, policy)
	_, _, err := service.CreateAPIKey(context.Background(), apiKeyPrincipal(domain.RoleOwner, freshSession(now)), CreateAPIKeyRequest{Name: "x"})
	if !errors.Is(err, domain.ErrAPIKeysDisabled) {
		t.Fatalf("err = %v, want ErrAPIKeysDisabled", err)
	}
}

func TestCreateAPIKeyRefusesKeysAndNonIssuers(t *testing.T) {
	service, _, now := apiKeyTestService(t, ServiceConfig{}, nil, openPolicy())
	viaKey := apiKeyPrincipal(domain.RoleOwner, domain.AuthContext{Method: domain.AuthMethodAPIKey, Assurance: "strong", AuthenticatedAt: now, CredentialID: "key-1"})
	if _, _, err := service.CreateAPIKey(context.Background(), viaKey, CreateAPIKeyRequest{Name: "x"}); !errors.Is(err, domain.ErrAPIKeyNotAllowed) {
		t.Fatalf("via key: err = %v, want ErrAPIKeyNotAllowed", err)
	}
	noIssue := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1", Auth: freshSession(now)}.
		WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead}))
	if _, _, err := service.CreateAPIKey(context.Background(), noIssue, CreateAPIKeyRequest{Name: "x"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("without api_key.issue: err = %v, want ErrForbidden", err)
	}
	agent := domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", OrganizationID: "org-1"}
	if _, _, err := service.CreateAPIKey(context.Background(), agent, CreateAPIKeyRequest{Name: "x"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("agent: err = %v, want ErrForbidden", err)
	}
}

// Scoping is the hosted edition's: the open edition refuses a list
// outright, and a plan without custom roles refuses it as plan_required.
func TestCreateAPIKeyScopingFollowsTheCustomRolesGate(t *testing.T) {
	service, _, now := apiKeyTestService(t, ServiceConfig{}, nil, openPolicy())
	principal := apiKeyPrincipal(domain.RoleOwner, freshSession(now))
	request := CreateAPIKeyRequest{Name: "narrow", Permissions: []domain.Permission{domain.PermissionPipelineRead}}
	if _, _, err := service.CreateAPIKey(context.Background(), principal, request); !errors.Is(err, domain.ErrAPIKeyScopingUnavailable) {
		t.Fatalf("open edition: err = %v, want ErrAPIKeyScopingUnavailable", err)
	}

	plan := entitlements.NewFixed()
	plan.SetCapability("org-1", application.CapabilityAPIKeyScoping, false, "plan")
	service, _, now = apiKeyTestService(t, ServiceConfig{APIKeyScopingEnabled: true}, plan, openPolicy())
	principal = apiKeyPrincipal(domain.RoleOwner, freshSession(now))
	if _, _, err := service.CreateAPIKey(context.Background(), principal, request); !errors.Is(err, domain.ErrPlanRequired) {
		t.Fatalf("hosted without the capability: err = %v, want ErrPlanRequired", err)
	}
}

// A hosted key's list is validated entry by entry: registered,
// key-bearable, and held by the caller; duplicates collapse and the list
// is stored sorted.
func TestCreateAPIKeyValidatesTheNarrowingList(t *testing.T) {
	service, keys, now := apiKeyTestService(t, ServiceConfig{APIKeyScopingEnabled: true}, nil, openPolicy())
	principal := apiKeyPrincipal(domain.RoleAuditor, freshSession(now))
	for name, permissions := range map[string][]domain.Permission{
		"empty list":   {},
		"unknown key":  {"nope.nope"},
		"not bearable": {domain.PermissionAPIKeyIssue},
		"not held":     {domain.PermissionPipelineWrite},
	} {
		_, _, err := service.CreateAPIKey(context.Background(), principal, CreateAPIKeyRequest{Name: "k", Permissions: permissions})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	key, _, err := service.CreateAPIKey(context.Background(), principal, CreateAPIKeyRequest{
		Name: "k", Permissions: []domain.Permission{domain.PermissionRequestReadUnrouted, domain.PermissionRequestRead, domain.PermissionRequestReadUnrouted},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Permission{domain.PermissionRequestRead, domain.PermissionRequestReadUnrouted}
	if len(key.Permissions) != 2 || key.Permissions[0] != want[0] || key.Permissions[1] != want[1] {
		t.Fatalf("stored permissions = %v, want %v", key.Permissions, want)
	}
	if keys.created[0].Permissions == nil {
		t.Fatal("repository received nil permissions for a narrowed key")
	}
}

// A key that could decide requests, or carry a dangerous permission, is
// minted only from a session that passes the organization's freshness
// and MFA rules now; a read-only key needs no ceremony.
func TestCreateAPIKeyNeedsAFreshSessionForDecidingKeys(t *testing.T) {
	policy := openPolicy()
	policy.MFAMode = domain.MFAReviewersRequired
	service, _, now := apiKeyTestService(t, ServiceConfig{}, nil, policy)

	stale := domain.AuthContext{Method: "password", Assurance: "strong", AuthenticatedAt: now.Add(-2 * time.Hour)}
	if _, _, err := service.CreateAPIKey(context.Background(), apiKeyPrincipal(domain.RoleReviewer, stale), CreateAPIKeyRequest{Name: "bot"}); !errors.Is(err, domain.ErrAuthenticationStale) {
		t.Fatalf("stale reviewer: err = %v, want ErrAuthenticationStale", err)
	}
	weak := domain.AuthContext{Method: "password", Assurance: "single_factor", AuthenticatedAt: now.Add(-time.Minute)}
	if _, _, err := service.CreateAPIKey(context.Background(), apiKeyPrincipal(domain.RoleReviewer, weak), CreateAPIKeyRequest{Name: "bot"}); !errors.Is(err, domain.ErrAuthenticationWeak) {
		t.Fatalf("weak reviewer: err = %v, want ErrAuthenticationWeak", err)
	}
	if _, _, err := service.CreateAPIKey(context.Background(), apiKeyPrincipal(domain.RoleReviewer, freshSession(now)), CreateAPIKeyRequest{Name: "bot"}); err != nil {
		t.Fatalf("fresh reviewer: %v", err)
	}
	// An auditor's key carries nothing that needs the ceremony, so a stale
	// single-factor session may still mint one.
	staleWeak := domain.AuthContext{Method: "password", Assurance: "single_factor", AuthenticatedAt: now.Add(-2 * time.Hour)}
	key, _, err := service.CreateAPIKey(context.Background(), apiKeyPrincipal(domain.RoleAuditor, staleWeak), CreateAPIKeyRequest{Name: "export"})
	if err != nil {
		t.Fatalf("stale auditor: %v", err)
	}
	if key.AssuranceAtIssue != "single_factor" {
		t.Fatalf("assurance at issue = %q", key.AssuranceAtIssue)
	}
}

// Expiry is the person's choice unless the deployment caps it; a cap
// fills in a missing expiry and refuses one beyond it.
func TestCreateAPIKeyExpiry(t *testing.T) {
	service, _, now := apiKeyTestService(t, ServiceConfig{}, nil, openPolicy())
	principal := apiKeyPrincipal(domain.RoleAuditor, freshSession(now))
	past := now.Add(-time.Second)
	if _, _, err := service.CreateAPIKey(context.Background(), principal, CreateAPIKeyRequest{Name: "k", ExpiresAt: &past}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("past expiry: err = %v, want ErrInvalid", err)
	}
	later := now.Add(400 * 24 * time.Hour)
	key, _, err := service.CreateAPIKey(context.Background(), principal, CreateAPIKeyRequest{Name: "k", ExpiresAt: &later})
	if err != nil || key.ExpiresAt == nil || !key.ExpiresAt.Equal(later) {
		t.Fatalf("uncapped expiry: key %+v err %v", key, err)
	}

	service, _, now = apiKeyTestService(t, ServiceConfig{APIKeyMaxTTL: 30 * 24 * time.Hour}, nil, openPolicy())
	principal = apiKeyPrincipal(domain.RoleAuditor, freshSession(now))
	key, _, err = service.CreateAPIKey(context.Background(), principal, CreateAPIKeyRequest{Name: "k"})
	if err != nil || key.ExpiresAt == nil || !key.ExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("capped default expiry: key %+v err %v", key, err)
	}
	if _, _, err := service.CreateAPIKey(context.Background(), principal, CreateAPIKeyRequest{Name: "k", ExpiresAt: &later}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("beyond the cap: err = %v, want ErrInvalid", err)
	}
}

// Listing and revoking one's own keys needs only membership; the
// organization-wide forms need api_key.read and api_key.revoke and drop
// the membership restriction.
func TestListAndRevokeAPIKeysScopeToTheCaller(t *testing.T) {
	service, keys, now := apiKeyTestService(t, ServiceConfig{}, nil, openPolicy())
	reviewer := apiKeyPrincipal(domain.RoleReviewer, freshSession(now))
	admin := apiKeyPrincipal(domain.RoleAdmin, freshSession(now))

	if _, err := service.ListAPIKeys(context.Background(), reviewer, false); err != nil {
		t.Fatal(err)
	}
	if keys.listed[0].MembershipID != "membership-1" {
		t.Fatalf("own list input = %+v, want the caller's membership", keys.listed[0])
	}
	if _, err := service.ListAPIKeys(context.Background(), reviewer, true); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reviewer org-wide list: err = %v, want ErrForbidden", err)
	}
	if _, err := service.ListAPIKeys(context.Background(), admin, true); err != nil {
		t.Fatal(err)
	}
	if keys.listed[1].MembershipID != "" {
		t.Fatalf("org-wide list input = %+v, want no membership filter", keys.listed[1])
	}

	if err := service.RevokeAPIKey(context.Background(), reviewer, "key-1", false); err != nil {
		t.Fatal(err)
	}
	if keys.revoked[0].MembershipID != "membership-1" || keys.revoked[0].ActorMembershipID != "membership-1" {
		t.Fatalf("own revoke input = %+v", keys.revoked[0])
	}
	if err := service.RevokeAPIKey(context.Background(), reviewer, "key-1", true); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reviewer org-wide revoke: err = %v, want ErrForbidden", err)
	}
	if err := service.RevokeAPIKey(context.Background(), admin, "key-1", true); err != nil {
		t.Fatal(err)
	}
	if keys.revoked[1].MembershipID != "" {
		t.Fatalf("org-wide revoke input = %+v, want no membership filter", keys.revoked[1])
	}
}

func TestVerifyAPIKeyNeedsAKeyPrincipal(t *testing.T) {
	service, _, now := apiKeyTestService(t, ServiceConfig{}, nil, openPolicy())
	if _, err := service.VerifyAPIKey(context.Background(), apiKeyPrincipal(domain.RoleOwner, freshSession(now))); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("session: err = %v, want ErrInvalid", err)
	}
	viaKey := apiKeyPrincipal(domain.RoleOwner, domain.AuthContext{Method: domain.AuthMethodAPIKey, AuthenticatedAt: now, CredentialID: "key-1"})
	key, err := service.VerifyAPIKey(context.Background(), viaKey)
	if err != nil || key.ID != "key-1" {
		t.Fatalf("key = %+v err %v", key, err)
	}
}

func TestResolveAPIKeyParsesThePrefixAndHashesTheSecret(t *testing.T) {
	service, keys, _ := apiKeyTestService(t, ServiceConfig{}, nil, openPolicy())
	for _, bad := range []string{"", "whk_", "whc_" + "11111111-1111-1111-1111-111111111111" + "_secret", "whk_not-a-uuid_secret"} {
		if _, err := service.ResolveAPIKey(context.Background(), bad); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Errorf("%q: err = %v, want ErrUnauthenticated", bad, err)
		}
	}
	token := "whk_11111111-1111-1111-1111-111111111111_sec_ret_with_underscores"
	principal, err := service.ResolveAPIKey(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Auth.CredentialID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("credential id = %q", principal.Auth.CredentialID)
	}
	if string(keys.resolved[0].SecretHash) != string(HashToken("sec_ret_with_underscores")) {
		t.Fatal("secret was not hashed whole; the parser split on a payload underscore")
	}
}

// The bearable vocabulary is what the caller holds, minus the key family.
func TestAPIKeyBearableFollowsTheCallersGrants(t *testing.T) {
	service, _, now := apiKeyTestService(t, ServiceConfig{}, nil, openPolicy())
	var keys []string
	for _, definition := range service.APIKeyBearable(apiKeyPrincipal(domain.RoleAuditor, freshSession(now))) {
		keys = append(keys, string(definition.Key))
	}
	joined := strings.Join(keys, ",")
	if !strings.Contains(joined, "request.read.unrouted") || strings.Contains(joined, "api_key.") || strings.Contains(joined, "pipeline.write") {
		t.Fatalf("bearable for an auditor = %s", joined)
	}
}

func (r *apiKeyRepositoryStub) ListAPIKeyScopes(context.Context, application.ListAPIKeyScopesInput) ([]domain.PermissionResource, error) {
	return []domain.PermissionResource{}, nil
}

func (s *apiKeyRepositoryStub) RecordAPIKeyUse(context.Context, application.RecordAPIKeyUseInput) error {
	return nil
}

func TestAPIKeyPrefixesSelectStoredKind(t *testing.T) {
	service, keys, _ := apiKeyTestService(t, ServiceConfig{}, nil, openPolicy())
	for _, tc := range []struct {
		prefix string
		kind   domain.APIKeyKind
	}{{"whk", domain.APIKeyPersonal}, {"who", domain.APIKeyOrganization}} {
		token := tc.prefix + "_11111111-1111-1111-1111-111111111111_secret_with_underscores"
		if !IsAPIKeyToken(token) {
			t.Fatal("key prefix not recognized")
		}
		if _, err := service.ResolveAPIKey(context.Background(), token); err != nil {
			t.Fatal(err)
		}
		got := keys.resolved[len(keys.resolved)-1]
		if got.Kind != tc.kind || string(got.SecretHash) != string(HashToken("secret_with_underscores")) {
			t.Fatalf("wrong kind or secret for %s", tc.prefix)
		}
	}
}
