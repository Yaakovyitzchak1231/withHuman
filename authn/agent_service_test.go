package authn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type agentRepositoryStub struct {
	agentSlug           domain.AgentSlug
	requestedAgent      *domain.Agent
	getEnrollmentInput  application.GetAgentEnrollmentInput
	listEnrollmentInput application.ListEnrollmentAgentsInput
	lookupInput         application.LookupAgentSlugInput
	createAgentInput    application.CreateAgentInput
	listInput           application.ListAgentSummariesInput
	authorizeInput      application.AuthorizeAgentEnrollmentInput
	enrollment          domain.AgentEnrollment
	exchangedRecord     application.AgentCredentialRecord
	instanceRecord      application.AgentCredentialRecord
	instanceActorID     domain.ID
	instanceMetadata    json.RawMessage
	provisionerRecord   application.AgentProvisionerRecord
	provisionerAgentID  domain.ID
	mintInput           application.MintAgentInstanceInput
	mintCalls           int
	provisionerErr      error
	statusInput         application.SetAgentStatusInput
	renameInput         application.RenameAgentInput
	archiveInput        application.ArchiveAgentInput
	restoreInput        application.RestoreAgentInput
	getBySlugInput      application.GetAgentBySlugInput
	instanceStatusInput application.SetAgentInstanceStatusInput
	instanceRenameInput application.RenameAgentInstanceInput
}

func (r *agentRepositoryStub) ResolveAgentCredential(context.Context, application.ResolveAgentCredentialInput) (domain.Principal, error) {
	return domain.Principal{}, nil
}
func (r *agentRepositoryStub) CreateAgent(_ context.Context, input application.CreateAgentInput) (domain.Agent, error) {
	r.createAgentInput = input
	return domain.Agent{ID: "agent-1", OrganizationID: input.OrganizationID, Name: input.Name, AgentSlug: input.AgentSlug}, nil
}
func (r *agentRepositoryStub) GetAgentBySlug(_ context.Context, input application.GetAgentBySlugInput) (domain.Agent, error) {
	r.getBySlugInput = input
	if r.requestedAgent != nil {
		return *r.requestedAgent, nil
	}
	return domain.Agent{}, domain.ErrNotFound
}

func (r *agentRepositoryStub) GetAgent(_ context.Context, input application.GetAgentInput) (domain.Agent, error) {
	return domain.Agent{ID: input.AgentID, OrganizationID: input.OrganizationID, AgentSlug: r.agentSlug, Status: domain.AgentActive}, nil
}
func (r *agentRepositoryStub) ListAgentSummaries(_ context.Context, input application.ListAgentSummariesInput) ([]domain.AgentSummary, error) {
	r.listInput = input
	return nil, nil
}
func (r *agentRepositoryStub) GetAgentDetail(_ context.Context, input application.GetAgentDetailInput) (domain.AgentDetail, error) {
	return domain.AgentDetail{Agent: domain.Agent{ID: input.AgentID, OrganizationID: input.OrganizationID, AgentSlug: r.agentSlug, Status: domain.AgentActive}}, nil
}

// LookupAgentSlug answers with the stub's one agent slug; every agent,
// instance, credential, and provisioner the stub knows belongs to it.
func (r *agentRepositoryStub) LookupAgentSlug(_ context.Context, input application.LookupAgentSlugInput) (domain.AgentSlug, error) {
	r.lookupInput = input
	return r.agentSlug, nil
}
func (r *agentRepositoryStub) SetAgentStatus(_ context.Context, input application.SetAgentStatusInput) (domain.Agent, error) {
	r.statusInput = input
	return domain.Agent{ID: input.AgentID, OrganizationID: input.OrganizationID, Status: input.Status}, nil
}
func (r *agentRepositoryStub) RenameAgent(_ context.Context, input application.RenameAgentInput) (domain.Agent, error) {
	r.renameInput = input
	return domain.Agent{ID: input.AgentID, OrganizationID: input.OrganizationID, AgentSlug: r.agentSlug, Name: input.Name, Status: domain.AgentActive}, nil
}
func (r *agentRepositoryStub) ArchiveAgent(_ context.Context, input application.ArchiveAgentInput) (domain.Agent, error) {
	r.archiveInput = input
	archivedAt := input.Now
	return domain.Agent{ID: input.AgentID, OrganizationID: input.OrganizationID, Status: domain.AgentArchived, ArchivedAt: &archivedAt}, nil
}
func (r *agentRepositoryStub) RestoreAgent(_ context.Context, input application.RestoreAgentInput) (domain.Agent, error) {
	r.restoreInput = input
	return domain.Agent{ID: input.AgentID, OrganizationID: input.OrganizationID, AgentSlug: r.agentSlug, Status: domain.AgentActive}, nil
}
func (r *agentRepositoryStub) SetAgentInstanceStatus(_ context.Context, input application.SetAgentInstanceStatusInput) (domain.AgentInstance, error) {
	r.instanceStatusInput = input
	return domain.AgentInstance{ID: input.InstanceID, OrganizationID: input.OrganizationID, Status: input.Status}, nil
}
func (r *agentRepositoryStub) RenameAgentInstance(_ context.Context, input application.RenameAgentInstanceInput) (domain.AgentInstance, error) {
	r.instanceRenameInput = input
	return domain.AgentInstance{ID: input.InstanceID, OrganizationID: input.OrganizationID, AgentSlug: r.agentSlug, Name: input.Name, Status: domain.AgentActive}, nil
}
func (r *agentRepositoryStub) CreateAgentInstanceWithCredential(_ context.Context, input application.CreateAgentInstanceWithCredentialInput) (domain.AgentInstance, error) {
	r.instanceRecord, r.instanceActorID, r.instanceMetadata = input.Credential, input.ActorID, input.Metadata
	return domain.AgentInstance{ID: "instance-1", OrganizationID: input.OrganizationID, AgentID: input.AgentID, Name: input.Name, Status: domain.AgentActive, Metadata: input.Metadata, CreatedAt: input.Now, LastSeenAt: input.Now}, nil
}
func (r *agentRepositoryStub) RevokeAgentCredential(context.Context, application.RevokeAgentCredentialInput) error {
	return nil
}
func (r *agentRepositoryStub) CreateAgentEnrollment(_ context.Context, input application.CreateAgentEnrollmentInput) error {
	r.enrollment = input.Enrollment
	return nil
}
func (r *agentRepositoryStub) AuthorizeAgentEnrollment(_ context.Context, input application.AuthorizeAgentEnrollmentInput) (domain.AgentInstance, error) {
	r.authorizeInput = input
	return domain.AgentInstance{}, nil
}
func (r *agentRepositoryStub) ExchangeAgentEnrollment(_ context.Context, input application.ExchangeAgentEnrollmentInput) (domain.AgentEnrollmentIdentity, error) {
	r.exchangedRecord = input.Credential
	return domain.AgentEnrollmentIdentity{Agent: domain.Agent{ID: "agent-1", Name: "coding-agents", AgentSlug: "coding-agents"}, AgentInstance: domain.AgentInstance{ID: "instance-1", AgentID: "agent-1", Status: domain.AgentActive, LastSeenAt: input.Now}}, nil
}
func (r *agentRepositoryStub) CreateAgentProvisioner(_ context.Context, input application.CreateAgentProvisionerInput) (domain.AgentProvisioner, error) {
	r.provisionerRecord, r.provisionerAgentID = input.Provisioner, input.AgentID
	return domain.AgentProvisioner{
		ID: input.Provisioner.ID, OrganizationID: input.OrganizationID, AgentID: input.AgentID,
		CreatedByActorID: input.ActorID, IssuedAt: input.Provisioner.IssuedAt, ExpiresAt: input.Provisioner.ExpiresAt,
	}, nil
}
func (r *agentRepositoryStub) MintAgentInstance(_ context.Context, input application.MintAgentInstanceInput) (domain.AgentInstance, error) {
	r.mintInput = input
	r.mintCalls++
	name := input.InstanceName
	if name == "" {
		name = "instance-generated"
	}
	return domain.AgentInstance{ID: "instance-1", AgentID: "agent-1", Name: name, Status: domain.AgentActive, Metadata: input.Metadata, CreatedAt: input.Now, LastSeenAt: input.Now}, nil
}
func (r *agentRepositoryStub) AuthenticateAgentProvisioner(_ context.Context, input application.AuthenticateAgentProvisionerInput) (domain.AgentProvisioner, error) {
	if r.provisionerErr != nil {
		return domain.AgentProvisioner{}, r.provisionerErr
	}
	return domain.AgentProvisioner{ID: input.ProvisionerID, OrganizationID: "org-1", AgentID: "agent-1"}, nil
}
func (r *agentRepositoryStub) RevokeAgentProvisioner(context.Context, application.RevokeAgentProvisionerInput) error {
	return nil
}
func (r *agentRepositoryStub) Health(context.Context) error { return nil }

func TestBeginAgentEnrollmentPersistsAgentAndInstanceIntent(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	repository := &agentRepositoryStub{}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{
		PublicOrigin: "https://withhuman.example", EnrollmentCodeTTL: 10 * time.Minute,
	})

	result, err := service.BeginAgentEnrollment(context.Background(), "claude-code", "release-agent", "production-runner", json.RawMessage(`{"region":"eu-west-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.UserCode == "" || result.VerificationURI != "https://withhuman.example/connect/authorize" {
		t.Fatalf("unexpected enrollment result: %#v", result)
	}
	_, secret, err := ParseOpaqueToken(result.EnrollmentCode, "whe")
	if err != nil {
		t.Fatal(err)
	}
	if repository.enrollment.RequestedAgentSlug != "release-agent" || repository.enrollment.InstanceName != "production-runner" {
		t.Fatalf("enrollment lost identity intent: %#v", repository.enrollment)
	}
	if !bytes.Equal(repository.enrollment.EnrollmentCodeHash, HashToken(secret)) {
		t.Fatal("repository did not receive the enrollment secret hash")
	}
}

func TestAdministratorsAssignAgentSlug(t *testing.T) {
	now := time.Unix(15_000, 0).UTC()
	repository := &agentRepositoryStub{}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{})
	principal := domain.Principal{
		ActorType: domain.ActorHuman, OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionAgentWrite, domain.PermissionAgentCredentialIssue}))

	agent, err := service.CreateAgent(context.Background(), principal, "release-agent", "release")
	if err != nil {
		t.Fatal(err)
	}
	if agent.AgentSlug != "release" || repository.createAgentInput.AgentSlug != "release" {
		t.Fatalf("agent slug was not assigned by the administrator: %#v", repository.createAgentInput)
	}
	if _, err := service.CreateAgent(context.Background(), principal, "release-agent", "Release Team"); err == nil {
		t.Fatal("invalid agent slug was accepted")
	}

	if _, err := service.AuthorizeAgentEnrollment(context.Background(), principal, "ABC-12", "agent-1"); err != nil {
		t.Fatal(err)
	}
	if repository.authorizeInput.AgentID != "agent-1" {
		t.Fatalf("enrollment authorization lost the agent: %#v", repository.authorizeInput)
	}
}

func TestCreateAgentInstanceBindsCredentialToRegisteredRuntime(t *testing.T) {
	now := time.Unix(20_000, 0).UTC()
	repository := &agentRepositoryStub{}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{CredentialTTL: time.Hour})
	principal := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionAgentCredentialIssue}))

	result, err := service.CreateAgentInstanceCredential(context.Background(), principal, "agent-1", "runner-1", json.RawMessage(`{"environment":"production"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentInstance.AgentID != "agent-1" || repository.instanceActorID != "user-1" {
		t.Fatalf("instance attribution is wrong: %#v", result.AgentInstance)
	}
	_, secret, err := ParseOpaqueToken(result.Credential.Token, "whc")
	if err != nil {
		t.Fatal(err)
	}
	if result.Credential.ID != repository.instanceRecord.ID || !bytes.Equal(repository.instanceRecord.SecretHash, HashToken(secret)) {
		t.Fatal("issued credential does not match the atomically persisted record")
	}
}

func TestCreateAgentProvisionerScopesTokenToOneAgent(t *testing.T) {
	now := time.Unix(40_000, 0).UTC()
	repository := &agentRepositoryStub{}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{ProvisionerTTL: 365 * 24 * time.Hour})
	principal := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionAgentCredentialIssue}))

	result, err := service.CreateAgentProvisioner(context.Background(), principal, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if repository.provisionerAgentID != "agent-1" || result.Provisioner.AgentID != "agent-1" {
		t.Fatalf("provisioner is not scoped to the agent: %#v", result.Provisioner)
	}
	_, secret, err := ParseOpaqueToken(result.Credential.Token, "whp")
	if err != nil {
		t.Fatal(err)
	}
	if result.Credential.ID != repository.provisionerRecord.ID || !bytes.Equal(repository.provisionerRecord.SecretHash, HashToken(secret)) {
		t.Fatal("issued provisioner token does not match the persisted record")
	}
	if result.Credential.ExpiresAt != now.Add(365*24*time.Hour) {
		t.Fatalf("unexpected provisioner expiry: %s", result.Credential.ExpiresAt)
	}

	if _, err := service.CreateAgentProvisioner(context.Background(), domain.Principal{
		ActorType: domain.ActorHuman, OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleReviewer})), "agent-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected forbidden without agent.credential.issue, got %v", err)
	}
}

func TestMintAgentInstanceCapsRequestedTTLAndAllowsAnonymousName(t *testing.T) {
	now := time.Unix(50_000, 0).UTC()
	repository := &agentRepositoryStub{}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{CredentialTTL: 90 * 24 * time.Hour})
	token := "whp_1b4e28ba-2fa1-11d2-883f-0016d3cca427_provisioner-secret"

	result, err := service.MintAgentInstance(context.Background(), MintInstanceRequest{Token: token, TTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	if repository.mintInput.InstanceName != "" || result.AgentInstance.Name == "" {
		t.Fatalf("provider-generated name was not applied: %#v", result.AgentInstance)
	}
	if !bytes.Equal(repository.mintInput.SecretHash, HashToken("provisioner-secret")) {
		t.Fatal("repository did not receive the provisioner secret hash")
	}
	if repository.mintInput.Credential.ExpiresAt != now.Add(time.Hour) {
		t.Fatalf("requested TTL was not honored: %s", repository.mintInput.Credential.ExpiresAt)
	}
	_, _, err = ParseOpaqueToken(result.Credential.Token, "whc")
	if err != nil {
		t.Fatal("minted credential is not an instance-class whc token")
	}

	if _, err := service.MintAgentInstance(context.Background(), MintInstanceRequest{Token: token, InstanceName: "runner-1", TTLSeconds: 400 * 24 * 60 * 60}); err != nil {
		t.Fatal(err)
	}
	if repository.mintInput.Credential.ExpiresAt != now.Add(90*24*time.Hour) {
		t.Fatalf("requested TTL was not capped by policy: %s", repository.mintInput.Credential.ExpiresAt)
	}

	if _, err := service.MintAgentInstance(context.Background(), MintInstanceRequest{Token: token}); err != nil {
		t.Fatal(err)
	}
	if repository.mintInput.Credential.ExpiresAt != now.Add(90*24*time.Hour) {
		t.Fatalf("default TTL was not applied: %s", repository.mintInput.Credential.ExpiresAt)
	}
}

type deliveryVerifierStub struct {
	input application.VerifyInstanceDeliveryInput
	err   error
}

func (v *deliveryVerifierStub) VerifyInstanceDelivery(_ context.Context, input application.VerifyInstanceDeliveryInput) (application.AgentInstanceDeliveryRecord, error) {
	v.input = input
	if v.err != nil {
		return application.AgentInstanceDeliveryRecord{}, v.err
	}
	return application.AgentInstanceDeliveryRecord{URL: input.Delivery.URL, RegistrationID: input.Delivery.RegistrationID, SealedSigningSecret: []byte("sealed"), VerifiedAt: time.Unix(1, 0)}, nil
}

func TestMintAgentInstanceVerifiesDeliveryAgainstTheNewInstance(t *testing.T) {
	repository := &agentRepositoryStub{}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{time.Unix(50_000, 0).UTC()}, ServiceConfig{CredentialTTL: time.Hour})
	token := "whp_1b4e28ba-2fa1-11d2-883f-0016d3cca427_provisioner-secret"
	delivery := &application.ProposedInstanceDelivery{Type: "webhook", URL: "https://harness.example.com/aap", RegistrationID: "registration-1", SigningSecret: "whsec_x"}

	if _, err := service.MintAgentInstance(context.Background(), MintInstanceRequest{Token: token, Delivery: delivery}); !errors.Is(err, domain.ErrWebhookUnconfigured) {
		t.Fatalf("delivery without a verifier must be unconfigured, got %v", err)
	}
	verifier := &deliveryVerifierStub{}
	service.RegisterInstanceDeliveries(verifier)
	repository.provisionerErr = domain.ErrUnauthenticated
	if _, err := service.MintAgentInstance(context.Background(), MintInstanceRequest{Token: token, Delivery: delivery}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("a dead provisioner must fail before verification, got %v", err)
	}
	if verifier.input.InstanceID != "" {
		t.Fatal("the receiver was contacted for an unauthenticated provisioner")
	}
	repository.provisionerErr = nil
	verifier.err = domain.ErrWebhookVerificationFailed
	if _, err := service.MintAgentInstance(context.Background(), MintInstanceRequest{Token: token, Delivery: delivery}); !errors.Is(err, domain.ErrWebhookVerificationFailed) {
		t.Fatalf("expected verification failure, got %v", err)
	}
	if repository.mintCalls != 0 {
		t.Fatal("an instance was minted although verification failed")
	}
	verifier.err = nil
	if _, err := service.MintAgentInstance(context.Background(), MintInstanceRequest{Token: token, Delivery: delivery}); err != nil {
		t.Fatal(err)
	}
	if verifier.input.OrganizationID != "org-1" || verifier.input.InstanceID == "" || verifier.input.InstanceID != repository.mintInput.InstanceID {
		t.Fatalf("verification must bind the minted instance: verified %+v, minted %s", verifier.input, repository.mintInput.InstanceID)
	}
	if repository.mintInput.Delivery == nil || repository.mintInput.Delivery.URL != delivery.URL {
		t.Fatalf("the verified receiver was not stored: %+v", repository.mintInput.Delivery)
	}
}

func TestCredentialClassesAreDisjoint(t *testing.T) {
	now := time.Unix(60_000, 0).UTC()
	repository := &agentRepositoryStub{}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{CredentialTTL: time.Hour})

	if _, err := service.MintAgentInstance(context.Background(), MintInstanceRequest{Token: "whc_1b4e28ba-2fa1-11d2-883f-0016d3cca427_secret"}); err != domain.ErrUnauthenticated {
		t.Fatalf("an instance credential must not mint, got %v", err)
	}
	if _, err := service.ResolveCredential(context.Background(), "whp_1b4e28ba-2fa1-11d2-883f-0016d3cca427_secret"); err != domain.ErrUnauthenticated {
		t.Fatalf("a provisioner token must not authenticate AAP calls, got %v", err)
	}
}

func TestExchangeAgentEnrollmentReturnsInstanceBoundCredential(t *testing.T) {
	now := time.Unix(30_000, 0).UTC()
	repository := &agentRepositoryStub{}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{CredentialTTL: 24 * time.Hour})
	secret := "enrollment-secret"

	result, err := service.ExchangeAgentEnrollment(context.Background(), "whe_1b4e28ba-2fa1-11d2-883f-0016d3cca427_"+secret)
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentInstance.ID != "instance-1" || result.Credential.ID != repository.exchangedRecord.ID {
		t.Fatalf("unexpected exchange result: %#v", result)
	}
	if repository.exchangedRecord.ExpiresAt != now.Add(24*time.Hour) {
		t.Fatalf("unexpected credential expiry: %s", repository.exchangedRecord.ExpiresAt)
	}
}

func TestCreateAgentResolvesTheAgentQuotaForTheRepository(t *testing.T) {
	now := time.Unix(21_000, 0).UTC()
	repository := &agentRepositoryStub{}
	service := NewService(nil, repository, nil, nil, limitedEntitlements("org-1", application.DimensionAgents, 2), nil, fixedClock{now}, ServiceConfig{})
	principal := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionAgentWrite}))

	if _, err := service.CreateAgent(context.Background(), principal, "release-agent", "release"); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	// The service resolves the limit; the repository enforces it inside
	// the creating transaction, so the input must carry the quota.
	quota := repository.createAgentInput.AgentQuota
	if err := quota.Check(application.DimensionAgents, 1); err != nil {
		t.Fatalf("one agent under a limit of two should be allowed: %v", err)
	}
	if err := quota.Check(application.DimensionAgents, 2); !errors.Is(err, domain.ErrQuotaExceeded) {
		t.Fatalf("two agents at a limit of two = %v, want domain.ErrQuotaExceeded", err)
	}
}

// limitedEntitlements fixes one dimension's limit for one organization
// and leaves everything else unlimited.
func limitedEntitlements(organizationID string, dimension application.Dimension, value int64) *entitlements.Fixed {
	fixed := entitlements.NewFixed()
	fixed.SetLimit(organizationID, dimension, application.LimitValue{Value: value})
	return fixed
}

func TestAgentInventoryIsReadableByReviewersAndManagedByAgentWriters(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	repository := &agentRepositoryStub{agentSlug: "release"}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{})
	reviewer := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleReviewer}))
	admin := reviewer.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionAgentWrite}))
	agent := domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", OrganizationID: "org-1"}

	if _, err := service.ListAgents(context.Background(), reviewer, "", 50); err != nil {
		t.Fatalf("a reviewer must read the inventory: %v", err)
	}
	if _, err := service.GetAgent(context.Background(), reviewer, application.GetAgentDetailInput{AgentID: "agent-1"}); err != nil {
		t.Fatalf("a reviewer must read an agent: %v", err)
	}
	if _, err := service.ListAgents(context.Background(), agent, "", 50); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an agent must not read the inventory, got %v", err)
	}
	if _, err := service.SetAgentStatus(context.Background(), reviewer, "agent-1", domain.AgentDisabled); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a reviewer must not disable an agent, got %v", err)
	}
	if _, err := service.SetAgentStatus(context.Background(), admin, "agent-1", "paused"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an unknown status must be refused, got %v", err)
	}
	if _, err := service.SetAgentStatus(context.Background(), admin, "agent-1", domain.AgentDisabled); err != nil {
		t.Fatalf("SetAgentStatus: %v", err)
	}
	if repository.statusInput.Status != domain.AgentDisabled || repository.statusInput.ActorID != "membership-1" || !repository.statusInput.Now.Equal(now) {
		t.Fatalf("status change lost its attribution: %#v", repository.statusInput)
	}
	if _, err := service.SetAgentInstanceStatus(context.Background(), admin, "instance-1", domain.AgentActive); err != nil {
		t.Fatalf("SetAgentInstanceStatus: %v", err)
	}
	if repository.instanceStatusInput.InstanceID != "instance-1" || repository.instanceStatusInput.Status != domain.AgentActive {
		t.Fatalf("instance status change lost its target: %#v", repository.instanceStatusInput)
	}
	if _, err := service.ArchiveAgent(context.Background(), reviewer, "agent-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a reviewer must not archive an agent, got %v", err)
	}
	archived, err := service.ArchiveAgent(context.Background(), admin, "agent-1")
	if err != nil || archived.Status != domain.AgentArchived {
		t.Fatalf("ArchiveAgent = %#v, %v", archived, err)
	}
	if repository.archiveInput.AgentID != "agent-1" || repository.archiveInput.ActorID != "membership-1" || !repository.archiveInput.Now.Equal(now) {
		t.Fatalf("archive lost its attribution: %#v", repository.archiveInput)
	}
	if repository.lookupInput.AgentID != "agent-1" {
		t.Fatalf("archive did not resolve the agent's slug first: %#v", repository.lookupInput)
	}
}

// Renaming and restoring are agent.write at the agent, resolved through
// the repository like every other management write; the rename keeps
// the slug and refuses an empty name.
func TestRenameAndRestoreAgentNeedAgentWriteAtTheAgent(t *testing.T) {
	now := time.Unix(14_000, 0).UTC()
	repository := &agentRepositoryStub{agentSlug: "release"}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{})
	base := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1"}
	reviewer := base.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleReviewer}))
	releaseAdmin := base.WithGrants([]domain.Grant{{Permission: domain.PermissionAgentWrite, ScopeKind: domain.ScopeAgent, ScopeID: "release"}})
	billingAdmin := base.WithGrants([]domain.Grant{{Permission: domain.PermissionAgentWrite, ScopeKind: domain.ScopeAgent, ScopeID: "billing"}})

	if _, err := service.RenameAgent(context.Background(), reviewer, "agent-1", "Release"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a reviewer renaming an agent = %v, want forbidden", err)
	}
	if _, err := service.RenameAgent(context.Background(), billingAdmin, "agent-1", "Release"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an admin bound to another agent renaming = %v, want forbidden", err)
	}
	if _, err := service.RenameAgent(context.Background(), releaseAdmin, "agent-1", "  "); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an empty name = %v, want invalid", err)
	}
	renamed, err := service.RenameAgent(context.Background(), releaseAdmin, "agent-1", " Release ")
	if err != nil {
		t.Fatalf("RenameAgent: %v", err)
	}
	if renamed.Name != "Release" || renamed.AgentSlug != "release" {
		t.Fatalf("rename changed the wrong thing: %#v", renamed)
	}
	if repository.renameInput.AgentID != "agent-1" || repository.renameInput.Name != "Release" ||
		repository.renameInput.ActorID != "membership-1" || !repository.renameInput.Now.Equal(now) {
		t.Fatalf("rename lost its attribution: %#v", repository.renameInput)
	}
	if repository.lookupInput.OrganizationID != "org-1" || repository.lookupInput.AgentID != "agent-1" {
		t.Fatalf("rename did not resolve the agent's slug first: %#v", repository.lookupInput)
	}

	// An instance rename resolves the instance to its agent before the
	// same check, trims the name, and refuses an empty one.
	if _, err := service.RenameAgentInstance(context.Background(), reviewer, "instance-1", "laptop"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a reviewer renaming an instance = %v, want forbidden", err)
	}
	if _, err := service.RenameAgentInstance(context.Background(), billingAdmin, "instance-1", "laptop"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an admin bound to another agent renaming an instance = %v, want forbidden", err)
	}
	if _, err := service.RenameAgentInstance(context.Background(), releaseAdmin, "instance-1", " "); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an empty instance name = %v, want invalid", err)
	}
	renamedInstance, err := service.RenameAgentInstance(context.Background(), releaseAdmin, "instance-1", " chris laptop ")
	if err != nil || renamedInstance.Name != "chris laptop" || renamedInstance.ID != "instance-1" {
		t.Fatalf("RenameAgentInstance = %#v, %v", renamedInstance, err)
	}
	if repository.instanceRenameInput.InstanceID != "instance-1" || repository.instanceRenameInput.Name != "chris laptop" ||
		repository.instanceRenameInput.OrganizationID != "org-1" || repository.instanceRenameInput.ActorID != "membership-1" || !repository.instanceRenameInput.Now.Equal(now) {
		t.Fatalf("instance rename lost its attribution: %#v", repository.instanceRenameInput)
	}
	if repository.lookupInput.OrganizationID != "org-1" || repository.lookupInput.AgentInstanceID != "instance-1" {
		t.Fatalf("instance rename did not resolve the instance's agent first: %#v", repository.lookupInput)
	}

	if _, err := service.RestoreAgent(context.Background(), reviewer, "agent-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a reviewer restoring an agent = %v, want forbidden", err)
	}
	if _, err := service.RestoreAgent(context.Background(), billingAdmin, "agent-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an admin bound to another agent restoring = %v, want forbidden", err)
	}
	restored, err := service.RestoreAgent(context.Background(), releaseAdmin, "agent-1")
	if err != nil || restored.Status != domain.AgentActive {
		t.Fatalf("RestoreAgent = %#v, %v", restored, err)
	}
	if repository.restoreInput.AgentID != "agent-1" || repository.restoreInput.ActorID != "membership-1" || !repository.restoreInput.Now.Equal(now) {
		t.Fatalf("restore lost its attribution: %#v", repository.restoreInput)
	}
}

// Clients address agents by slug; any management principal may resolve
// one to the internal id, and the agent's own permission check follows.
// Agents and malformed slugs never reach the repository.
func TestAgentIDBySlugResolvesForManagementPrincipalsOnly(t *testing.T) {
	repository := &agentRepositoryStub{agentSlug: "release", requestedAgent: &domain.Agent{ID: "agent-1", AgentSlug: "release", Name: "Release", Status: domain.AgentActive}}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{time.Unix(16_000, 0).UTC()}, ServiceConfig{})
	base := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1"}
	reviewer := base.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleReviewer}))

	id, err := service.AgentIDBySlug(context.Background(), reviewer, "release")
	if err != nil || id != "agent-1" {
		t.Fatalf("AgentIDBySlug = %q, %v", id, err)
	}
	if repository.getBySlugInput.OrganizationID != "org-1" || repository.getBySlugInput.AgentSlug != "release" {
		t.Fatalf("lookup was not scoped to the organization: %#v", repository.getBySlugInput)
	}
	if _, err := service.AgentIDBySlug(context.Background(), reviewer, "Release Team"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a malformed slug = %v, want not found", err)
	}
	repository.requestedAgent = nil
	if _, err := service.AgentIDBySlug(context.Background(), reviewer, "billing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an unknown slug = %v, want not found", err)
	}
	agent := domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", OrganizationID: "org-1"}
	if _, err := service.AgentIDBySlug(context.Background(), agent, "release"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an agent resolving a slug = %v, want forbidden", err)
	}
}

// An admin bound to one agent manages that agent and nothing else: the
// checks resolve the target's slug through the repository before any
// grant is consulted, and a missing target reads as not found.
func TestAgentManagementIsScopedToTheAgent(t *testing.T) {
	now := time.Unix(11_000, 0).UTC()
	repository := &agentRepositoryStub{agentSlug: "release"}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{CredentialTTL: time.Hour})
	releaseAdmin := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants([]domain.Grant{
		{Permission: domain.PermissionAgentRead, ScopeKind: domain.ScopeAgent, ScopeID: "release"},
		{Permission: domain.PermissionAgentWrite, ScopeKind: domain.ScopeAgent, ScopeID: "release"},
		{Permission: domain.PermissionAgentCredentialIssue, ScopeKind: domain.ScopeAgent, ScopeID: "release"},
		{Permission: domain.PermissionAgentCredentialRevoke, ScopeKind: domain.ScopeAgent, ScopeID: "release"},
	})

	if _, err := service.CreateAgent(context.Background(), releaseAdmin, "release-agent", "release"); err != nil {
		t.Fatalf("create within the bound agent: %v", err)
	}
	if _, err := service.CreateAgent(context.Background(), releaseAdmin, "billing-agent", "billing"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("create outside the bound agent = %v, want forbidden", err)
	}
	if _, err := service.SetAgentStatus(context.Background(), releaseAdmin, "agent-1", domain.AgentDisabled); err != nil {
		t.Fatalf("disable within the bound agent: %v", err)
	}
	if repository.lookupInput.OrganizationID != "org-1" || repository.lookupInput.AgentID != "agent-1" {
		t.Fatalf("status change did not resolve the agent's slug first: %#v", repository.lookupInput)
	}
	if err := service.RevokeCredential(context.Background(), releaseAdmin, "credential-1"); err != nil {
		t.Fatalf("revoke within the bound agent: %v", err)
	}
	if repository.lookupInput.CredentialID != "credential-1" {
		t.Fatalf("revocation did not resolve the credential's agent first: %#v", repository.lookupInput)
	}
	if _, err := service.GetAgent(context.Background(), releaseAdmin, application.GetAgentDetailInput{AgentID: "agent-1"}); err != nil {
		t.Fatalf("read within the bound agent: %v", err)
	}
	if _, err := service.ListAgents(context.Background(), releaseAdmin, "", 50); err != nil {
		t.Fatalf("a scoped agent.read still lists the inventory: %v", err)
	}

	repository.agentSlug = "billing"
	if _, err := service.SetAgentStatus(context.Background(), releaseAdmin, "agent-2", domain.AgentDisabled); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("disable outside the bound agent = %v, want forbidden", err)
	}
	if _, err := service.GetAgent(context.Background(), releaseAdmin, application.GetAgentDetailInput{AgentID: "agent-2"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("read outside the bound agent = %v, want forbidden", err)
	}
	if _, err := service.CreateAgentInstanceCredential(context.Background(), releaseAdmin, "agent-2", "runner", nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("issue outside the bound agent = %v, want forbidden", err)
	}
	if err := service.RevokeAgentProvisioner(context.Background(), releaseAdmin, "provisioner-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("revoke outside the bound agent = %v, want forbidden", err)
	}
}

// Creating an agent needs agent.write at the requested slug and nothing
// more: who manages the agent afterwards is a matter of role assignments
// at the agent, so the create input carries no grant of its own.
func TestCreateAgentNeedsOnlyAgentWrite(t *testing.T) {
	now := time.Unix(12_000, 0).UTC()
	repository := &agentRepositoryStub{agentSlug: "release"}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{})
	admin := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleAdmin}))

	if _, err := service.CreateAgent(context.Background(), admin, "release-agent", "release"); err != nil {
		t.Fatalf("admin creating an agent: %v", err)
	}
	input := repository.createAgentInput
	if input.OrganizationID != "org-1" || input.ActorID != "membership-1" ||
		input.Name != "release-agent" || input.AgentSlug != "release" {
		t.Fatalf("create input = %#v", input)
	}

	// A reviewer holding agent.write may create the agent; one without
	// it may not.
	reviewer := admin.WithGrants(append(domain.GrantsForRoles([]domain.Role{domain.RoleReviewer}),
		domain.Grant{Permission: domain.PermissionAgentWrite, ScopeKind: domain.ScopeOrganization}))
	if _, err := service.CreateAgent(context.Background(), reviewer, "release-agent", "release"); err != nil {
		t.Fatalf("reviewer with agent.write creating an agent: %v", err)
	}
	plainReviewer := admin.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleReviewer}))
	if _, err := service.CreateAgent(context.Background(), plainReviewer, "release-agent", "release"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reviewer without agent.write creating an agent = %v, want forbidden", err)
	}
}

// The inventory query carries the caller's agent.read scope: every agent
// for an organization-wide grant, the bound agents for a scoped one, and
// forbidden when the permission is held nowhere.
func TestListAgentsNarrowsToTheReadScope(t *testing.T) {
	repository := &agentRepositoryStub{agentSlug: "release"}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{time.Unix(13_000, 0).UTC()}, ServiceConfig{})
	base := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1"}

	if _, err := service.ListAgents(context.Background(), base.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleReviewer})), "", 50); err != nil {
		t.Fatalf("organization-wide read: %v", err)
	}
	if !repository.listInput.Scope.All {
		t.Fatalf("organization-wide grant must list everything: %#v", repository.listInput.Scope)
	}
	scoped := base.WithGrants([]domain.Grant{{Permission: domain.PermissionAgentRead, ScopeKind: domain.ScopeAgent, ScopeID: "release"}})
	if _, err := service.ListAgents(context.Background(), scoped, "", 50); err != nil {
		t.Fatalf("scoped read: %v", err)
	}
	if ids := repository.listInput.Scope.IDs(domain.ScopeAgent); repository.listInput.Scope.All || len(ids) != 1 || ids[0] != "release" {
		t.Fatalf("scoped grant must narrow the query: %#v", repository.listInput.Scope)
	}
	unscoped := base.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead}))
	if _, err := service.ListAgents(context.Background(), unscoped, "", 50); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("no agent.read anywhere = %v, want forbidden", err)
	}
}

func (r *agentRepositoryStub) GetAgentEnrollment(_ context.Context, input application.GetAgentEnrollmentInput) (domain.AgentEnrollment, error) {
	r.getEnrollmentInput = input
	return r.enrollment, nil
}
func (r *agentRepositoryStub) ListEnrollmentAgents(_ context.Context, input application.ListEnrollmentAgentsInput) (domain.EnrollmentAgents, error) {
	r.listEnrollmentInput = input
	return domain.EnrollmentAgents{}, nil
}

func TestEnrollmentPreviewAndSelectionScope(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repository := &agentRepositoryStub{agentSlug: "coding", enrollment: domain.AgentEnrollment{Runtime: "codex", InstanceName: "laptop", Status: "pending", ExpiresAt: now.Add(time.Minute)}}
	service := NewService(nil, repository, nil, nil, entitlements.NewUnlimited(), nil, fixedClock{now}, ServiceConfig{})
	principal := domain.Principal{ActorType: domain.ActorHuman, OrganizationID: "org", MembershipID: "member"}.WithGrants([]domain.Grant{{Permission: domain.PermissionAgentCredentialIssue, ScopeKind: domain.ScopeAgent, ScopeID: "coding"}})
	preview, err := service.PreviewAgentEnrollment(ctx, principal, " abcd-1234 ")
	if err != nil || preview.Runtime != "codex" || preview.Agent != nil {
		t.Fatal(preview, err)
	}
	if repository.getEnrollmentInput.UserCode != "ABCD-1234" || repository.getEnrollmentInput.OrganizationID != "org" {
		t.Fatal(repository.getEnrollmentInput)
	}
	if _, err := service.ListEnrollmentAgents(ctx, principal, " coding ", 25, 25); err != nil {
		t.Fatal(err)
	}
	input := repository.listEnrollmentInput
	if input.Scope.All || len(input.Scope.IDs(domain.ScopeAgent)) != 1 || input.Scope.IDs(domain.ScopeAgent)[0] != "coding" || input.Query != "coding" || input.Offset != 25 {
		t.Fatal(input)
	}
	repository.enrollment.RequestedAgentSlug = "coding"
	if _, err := service.PreviewAgentEnrollment(ctx, principal, "ABCD-1234"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing target: %v", err)
	}
	if repository.getBySlugInput.OrganizationID != "org" || repository.getBySlugInput.AgentSlug != "coding" {
		t.Fatalf("requested slug was not looked up in the organization: %#v", repository.getBySlugInput)
	}
	repository.requestedAgent = &domain.Agent{ID: "agent", Name: "Coding agents", AgentSlug: "coding", Status: domain.AgentActive}
	if preview, err := service.PreviewAgentEnrollment(ctx, principal, "ABCD-1234"); err != nil || preview.Agent.ID != "agent" || preview.RequestedAgentSlug != "coding" {
		t.Fatal(preview, err)
	}
	repository.requestedAgent.AgentSlug = "operations"
	if _, err := service.PreviewAgentEnrollment(ctx, principal, "ABCD-1234"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("target outside scope: %v", err)
	}
	repository.requestedAgent.AgentSlug = "coding"
	repository.requestedAgent.Status = domain.AgentDisabled
	if _, err := service.PreviewAgentEnrollment(ctx, principal, "ABCD-1234"); !errors.Is(err, domain.ErrAgentInactive) {
		t.Fatal(err)
	}
	principal.ActorType = domain.ActorAgent
	if _, err := service.PreviewAgentEnrollment(ctx, principal, "ABCD-1234"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.ListEnrollmentAgents(ctx, principal, "", 0, 25); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.AuthorizeAgentEnrollment(ctx, principal, "ABCD-1234", "agent"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
}
