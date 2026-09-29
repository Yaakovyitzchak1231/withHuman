package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

func TestManagementPreviewCompilesExecutesAndDoesNotPersist(t *testing.T) {
	repository := newMemoryPipelineRepository()
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(100, 0).UTC()})
	principal := pipelineManagerPrincipal("organization-1")
	definitions := []approvalblock.Definition{
		pipelineDefinition("approve-refund", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("stripe.refund")),
		pipelineDefinition("deny-later", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("stripe.refund")),
	}

	result, err := service.Preview(context.Background(), principal, "billing", definitions, Route{}, PreviewSample{
		Request: BlockRequest{Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount_cents":900}`)},
		Agent:   BlockAgent{Slug: "billing", Name: "billing-agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BlockApprove || result.ReasonCode != "block_outcome" || result.TerminalBlockKey != "approve-refund" {
		t.Fatalf("unexpected preview result: %+v", result)
	}
	if len(result.Blocks) != 1 || result.Blocks[0].Position != 0 {
		t.Fatalf("preview did not stop at the first terminal block: %+v", result.Blocks)
	}
	if repository.createCalls != 0 || repository.activateCalls != 0 || len(repository.revisions) != 0 || len(repository.blocks) != 0 {
		t.Fatalf("preview persisted state: %+v", repository)
	}

	invalid := definitions[:1]
	invalid[0].CEL.When.Field = "/request/context"
	if _, err := service.Preview(context.Background(), principal, "billing", invalid, Route{}, PreviewSample{
		Request: BlockRequest{Tool: "stripe.refund", Arguments: json.RawMessage(`{}`)},
		Agent:   BlockAgent{Slug: "billing", Name: "billing-agent"},
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid CEL preview returned %v, want invalid", err)
	}
}

// The sample names the agent by slug like a real request does: every
// pipeline applies to every agent, so any well-formed slug previews, and
// conditions read it as agent.slug.
func TestManagementPreviewValidatesTheSampleAgentSlug(t *testing.T) {
	repository := newMemoryPipelineRepository()
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(150, 0).UTC()})
	principal := pipelineManagerPrincipal("organization-1")
	definitions := []approvalblock.Definition{
		pipelineDefinition("deny-billing", approvalblock.Deny, approvalblock.Field("/agent/slug").Equals("billing")),
	}
	sample := func(slug string) PreviewSample {
		return PreviewSample{
			Request: BlockRequest{Tool: "stripe.refund", Arguments: json.RawMessage(`{}`)},
			Agent:   BlockAgent{Slug: slug, Name: "Billing agent"},
		}
	}

	if _, err := service.Preview(context.Background(), principal, "entry", definitions, Route{}, sample("Billing Team")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("preview with a malformed slug returned %v, want invalid", err)
	}
	if _, err := service.Preview(context.Background(), principal, "billing", definitions, Route{}, PreviewSample{
		Request: BlockRequest{Tool: "stripe.refund", Arguments: json.RawMessage(`{}`)},
		Agent:   BlockAgent{Slug: "billing"},
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("preview without an agent name returned %v, want invalid", err)
	}

	result, err := service.Preview(context.Background(), principal, "billing", definitions, Route{}, sample("billing"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BlockDeny || result.TerminalBlockKey != "deny-billing" {
		t.Fatalf("condition on agent.slug did not match the sample: %+v", result)
	}
	result, err = service.Preview(context.Background(), principal, "entry", definitions, Route{}, sample("support"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BlockHuman || result.ReasonCode != "end_of_pipeline" || len(result.Blocks) != 1 || result.Blocks[0].Outcome != BlockNext {
		t.Fatalf("preview for another agent matched the billing condition: %+v", result)
	}
}

func TestManagementCreatesInactiveRevisionAndValidatesBeforeWriting(t *testing.T) {
	repository := newMemoryPipelineRepository()
	now := time.Unix(200, 0).UTC()
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
	principal := pipelineManagerPrincipal("organization-1")
	definitions := []approvalblock.Definition{
		pipelineDefinition("first", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("deploy")),
		pipelineDefinition("second", approvalblock.Human, approvalblock.Field("/request/tool").NotEquals("deploy")),
	}

	created, err := service.CreateRevision(context.Background(), principal, "release", pipelineName("release"), definitions, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision.Revision != 2 || created.Revision.IsActive || created.Revision.OrganizationID != principal.OrganizationID {
		t.Fatalf("unexpected created revision: %+v", created.Revision)
	}
	if len(created.Blocks) != 2 || created.Blocks[0].Definition.ID != "first" || created.Blocks[1].Definition.ID != "second" {
		t.Fatalf("ordered definitions were not preserved: %+v", created.Blocks)
	}
	if repository.createCalls != 1 {
		t.Fatalf("got %d create calls, want 1", repository.createCalls)
	}

	definitions[0].Enabled = false
	if _, err := service.CreateRevision(context.Background(), principal, "release", pipelineName("release"), definitions, Route{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("disabled stored block returned %v, want invalid", err)
	}
	if repository.createCalls != 1 {
		t.Fatalf("invalid document reached persistence: %d calls", repository.createCalls)
	}
}

func TestManagementActivationCompilesThenUsesExpectedActiveRevision(t *testing.T) {
	repository := newMemoryPipelineRepository()
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(300, 0).UTC()})
	principal := pipelineManagerPrincipal("organization-1")
	created, err := service.CreateRevision(context.Background(), principal, "billing", pipelineName("billing"), []approvalblock.Definition{
		pipelineDefinition("small-refund", approvalblock.Approve, approvalblock.Field("/request/arguments/amount").LessThan(1000)),
	}, Route{})
	if err != nil {
		t.Fatal(err)
	}

	activated, err := service.Activate(context.Background(), principal, "billing", created.Revision.Revision, 1)
	if err != nil || !activated.Revision.IsActive {
		t.Fatalf("activate = %+v, %v", activated, err)
	}
	if _, err := service.Activate(context.Background(), principal, "billing", created.Revision.Revision, 99); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("stale activate returned %v, want precondition failed", err)
	}

	corruptTarget, err := service.CreateRevision(context.Background(), principal, "billing", pipelineName("billing"), []approvalblock.Definition{
		pipelineDefinition("large-refund", approvalblock.Human, approvalblock.Field("/request/arguments/amount").GreaterThanOrEqual(1000)),
	}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	blockID := corruptTarget.Revision.BlockIDs[0]
	corrupt := repository.blocks[blockID]
	corrupt.Config = json.RawMessage(`{"unknown":true}`)
	repository.blocks[blockID] = corrupt
	activationCalls := repository.activateCalls
	expected := created.Revision.Revision
	if _, err := service.Activate(context.Background(), principal, "billing", corruptTarget.Revision.Revision, expected); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("corrupt target returned %v, want invalid", err)
	}
	if repository.activateCalls != activationCalls {
		t.Fatal("activation mutation ran before target compilation")
	}
}

func TestManagementRequiresDedicatedPermissionBeforeRepositoryAccess(t *testing.T) {
	repository := newMemoryPipelineRepository()
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(400, 0).UTC()})
	principal := pipelineManagerPrincipal("organization-1")
	principal = principal.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionEscalationPathRead}))

	if _, err := service.List(context.Background(), principal, "", 50); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("list returned %v, want forbidden", err)
	}
	if repository.listCalls != 0 {
		t.Fatal("unauthorized request reached the repository")
	}
}

type fixedPipelineClock struct{ now time.Time }

func (clock fixedPipelineClock) Now() time.Time { return clock.now }

type memoryPipelineRepository struct {
	listInput     application.ListRequestApprovalPipelineSummariesInput
	revisions     map[string][]domain.RequestApprovalPipelineRevision
	blocks        map[domain.ID]domain.RequestApprovalBlock
	createCalls   int
	activateCalls int
	listCalls     int
	blockLoads    int
}

func newMemoryPipelineRepository() *memoryPipelineRepository {
	return &memoryPipelineRepository{
		revisions: make(map[string][]domain.RequestApprovalPipelineRevision),
		blocks:    make(map[domain.ID]domain.RequestApprovalBlock),
	}
}

// seed mirrors the store: a pipeline is born with an empty active revision
// 1 (the entry pipeline with the organization, any other with its create),
// so the first authored revision is number 2. The memory repository seeds
// a key on its first revision so fixtures need not create every pipeline
// explicitly.
func (repository *memoryPipelineRepository) seed(input application.CreateRequestApprovalPipelineRevisionInput) {
	if len(repository.revisions[input.PipelineKey]) > 0 {
		return
	}
	repository.revisions[input.PipelineKey] = []domain.RequestApprovalPipelineRevision{{
		ID: domain.ID("seed-" + input.PipelineKey), OrganizationID: input.OrganizationID, PipelineKey: input.PipelineKey, Name: input.Name,
		Revision: 1, IsActive: true, BlockIDs: []domain.ID{}, CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
	}}
}

func (repository *memoryPipelineRepository) CreateRequestApprovalPipeline(_ context.Context, input application.CreateRequestApprovalPipelineInput) (domain.RequestApprovalPipelineRevision, error) {
	if len(repository.revisions[input.PipelineKey]) > 0 {
		return domain.RequestApprovalPipelineRevision{}, domain.ErrRequestApprovalPipelineExists
	}
	repository.createCalls++
	revision := domain.RequestApprovalPipelineRevision{
		ID: input.ID, OrganizationID: input.OrganizationID, PipelineKey: input.PipelineKey, Name: input.Name,
		Revision: 1, IsActive: true, BlockIDs: repository.storeBlocks(input.OrganizationID, input.CreatedByActorID, input.Now, input.Blocks),
		DefaultEscalationPath: input.DefaultEscalationPath, DefaultAskAgentOwner: input.DefaultAskAgentOwner,
		CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
	}
	repository.revisions[input.PipelineKey] = []domain.RequestApprovalPipelineRevision{revision}
	return revision, nil
}

func (repository *memoryPipelineRepository) CreateRequestApprovalPipelineRevision(_ context.Context, input application.CreateRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	repository.createCalls++
	repository.seed(input)
	revisions := repository.revisions[input.PipelineKey]
	if revisions[len(revisions)-1].ArchivedAt != nil {
		return domain.RequestApprovalPipelineRevision{}, domain.ErrRequestApprovalPipelineArchived
	}
	revision := domain.RequestApprovalPipelineRevision{
		ID: input.ID, OrganizationID: input.OrganizationID, PipelineKey: input.PipelineKey, Name: input.Name,
		Revision: int64(len(revisions) + 1), BlockIDs: repository.storeBlocks(input.OrganizationID, input.CreatedByActorID, input.Now, input.Blocks),
		DefaultEscalationPath: input.DefaultEscalationPath, DefaultAskAgentOwner: input.DefaultAskAgentOwner,
		CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
	}
	repository.revisions[input.PipelineKey] = append(revisions, revision)
	return revision, nil
}

func (repository *memoryPipelineRepository) storeBlocks(organizationID, actorID domain.ID, now time.Time, inputs []application.RequestApprovalBlockSnapshotInput) []domain.ID {
	ids := make([]domain.ID, len(inputs))
	for index, inputBlock := range inputs {
		ids[index] = inputBlock.ID
		repository.blocks[inputBlock.ID] = domain.RequestApprovalBlock{
			ID: inputBlock.ID, OrganizationID: organizationID, BlockKey: inputBlock.BlockKey,
			Name: inputBlock.Name, BlockType: inputBlock.BlockType, ConfigVersion: inputBlock.ConfigVersion,
			Config: inputBlock.Config, TimeoutMS: inputBlock.TimeoutMS, MaxAttempts: inputBlock.MaxAttempts,
			ContentHash: "hash-" + inputBlock.BlockKey, CreatedByActorID: actorID, CreatedAt: now,
		}
	}
	return ids
}

func (repository *memoryPipelineRepository) GetRequestApprovalPipelineRevisionByID(_ context.Context, input application.GetRequestApprovalPipelineRevisionByIDInput) (domain.RequestApprovalPipelineRevision, error) {
	for _, revisions := range repository.revisions {
		for _, revision := range revisions {
			if revision.ID == input.RevisionID && revision.OrganizationID == input.OrganizationID {
				return revision, nil
			}
		}
	}
	return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
}

func (repository *memoryPipelineRepository) ListRequestApprovalPipelineSummaries(_ context.Context, input application.ListRequestApprovalPipelineSummariesInput) ([]domain.RequestApprovalPipelineSummary, error) {
	repository.listCalls++
	repository.listInput = input
	result := make([]domain.RequestApprovalPipelineSummary, 0, len(repository.revisions))
	for key, revisions := range repository.revisions {
		latest := revisions[len(revisions)-1]
		var active *int64
		name := latest.Name
		for _, revision := range revisions {
			if revision.IsActive {
				number := revision.Revision
				active = &number
				name = revision.Name
			}
		}
		result = append(result, domain.RequestApprovalPipelineSummary{PipelineKey: key, Name: name, ActiveRevision: active, LatestRevision: latest.Revision, RevisionCount: int64(len(revisions)), LatestCreatedAt: latest.CreatedAt, ArchivedAt: latest.ArchivedAt})
	}
	return result, nil
}

func (repository *memoryPipelineRepository) GetRequestApprovalPipelineRevision(_ context.Context, input application.GetRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	for _, revision := range repository.revisions[input.PipelineKey] {
		if revision.OrganizationID == input.OrganizationID && revision.Revision == input.Revision {
			return revision, nil
		}
	}
	return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
}

func (repository *memoryPipelineRepository) ListRequestApprovalPipelineRevisions(_ context.Context, input application.ListRequestApprovalPipelineRevisionsInput) (application.RevisionPage[domain.RequestApprovalPipelineRevision], error) {
	revisions := append([]domain.RequestApprovalPipelineRevision(nil), repository.revisions[input.PipelineKey]...)
	return application.RevisionPage[domain.RequestApprovalPipelineRevision]{Revisions: revisions, TotalCount: int64(len(revisions))}, nil
}

func (repository *memoryPipelineRepository) ListActiveRequestApprovalPipelineRevisions(_ context.Context, input application.ListActiveRequestApprovalPipelineRevisionsInput) ([]domain.RequestApprovalPipelineRevision, error) {
	result := make([]domain.RequestApprovalPipelineRevision, 0)
	for _, revisions := range repository.revisions {
		for _, revision := range revisions {
			if revision.IsActive && revision.ID > input.AfterID {
				result = append(result, revision)
			}
		}
	}
	slices.SortFunc(result, func(left, right domain.RequestApprovalPipelineRevision) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	if len(result) > input.Limit {
		result = result[:input.Limit]
	}
	return result, nil
}

func (repository *memoryPipelineRepository) GetActiveRequestApprovalPipelineRevision(_ context.Context, input application.GetActiveRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	for _, revision := range repository.revisions[input.PipelineKey] {
		if revision.OrganizationID == input.OrganizationID && revision.IsActive {
			return revision, nil
		}
	}
	return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
}

func (repository *memoryPipelineRepository) GetRequestApprovalBlocks(_ context.Context, input application.GetRequestApprovalBlocksInput) ([]domain.RequestApprovalBlock, error) {
	repository.blockLoads++
	result := make([]domain.RequestApprovalBlock, len(input.BlockIDs))
	for index, id := range input.BlockIDs {
		block, exists := repository.blocks[id]
		if !exists || block.OrganizationID != input.OrganizationID {
			return nil, domain.ErrNotFound
		}
		result[index] = block
	}
	return result, nil
}

func (repository *memoryPipelineRepository) ActivateRequestApprovalPipelineRevision(_ context.Context, input application.ActivateRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	repository.activateCalls++
	revisions := repository.revisions[input.PipelineKey]
	activeIndex := -1
	for index := range revisions {
		if revisions[index].IsActive {
			activeIndex = index
		}
	}
	if activeIndex < 0 || revisions[activeIndex].Revision != input.ExpectedActiveRevision {
		return domain.RequestApprovalPipelineRevision{}, domain.ErrPreconditionFailed
	}
	for index := range revisions {
		revisions[index].IsActive = revisions[index].Revision == input.Revision
	}
	repository.revisions[input.PipelineKey] = revisions
	for _, revision := range revisions {
		if revision.Revision == input.Revision {
			return revision, nil
		}
	}
	return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
}

func (repository *memoryPipelineRepository) ArchiveRequestApprovalPipeline(ctx context.Context, input application.ArchiveRequestApprovalPipelineInput) ([]domain.RequestApprovalPipelineRevision, error) {
	if input.PipelineKey == domain.EntryPipelineKey {
		return nil, domain.ErrRequestApprovalPipelineEntry
	}
	revisions := repository.revisions[input.PipelineKey]
	if len(revisions) == 0 {
		return nil, domain.ErrNotFound
	}
	if revisions[0].ArchivedAt != nil {
		return nil, domain.ErrRequestApprovalPipelineArchived
	}
	uses, _ := repository.ListRequestApprovalPipelineUses(ctx, application.ListRequestApprovalPipelineUsesInput{OrganizationID: input.OrganizationID, PipelineKey: input.PipelineKey})
	if len(uses) > 0 {
		return nil, domain.RequestApprovalPipelineInUseError{PipelineKey: input.PipelineKey, Uses: uses}
	}
	now := input.Now
	for index := range revisions {
		revisions[index].IsActive = false
		revisions[index].ArchivedAt = &now
	}
	repository.revisions[input.PipelineKey] = revisions
	return append([]domain.RequestApprovalPipelineRevision(nil), revisions...), nil
}

func (repository *memoryPipelineRepository) ListRequestApprovalPipelineUses(_ context.Context, input application.ListRequestApprovalPipelineUsesInput) ([]domain.PipelineUse, error) {
	uses := make([]domain.PipelineUse, 0)
	for key, revisions := range repository.revisions {
		for _, revision := range revisions {
			if !revision.IsActive {
				continue
			}
			for _, id := range revision.BlockIDs {
				block := repository.blocks[id]
				if block.BlockType != string(approvalblock.TypeDelegate) {
					continue
				}
				var config approvalblock.DelegateConfig
				if err := json.Unmarshal(block.Config, &config); err == nil && config.PipelineKey == input.PipelineKey {
					uses = append(uses, domain.PipelineUse{PipelineID: revision.ID, PipelineKey: key, Revision: revision.Revision, BlockKey: block.BlockKey})
				}
			}
		}
	}
	return uses, nil
}

func (repository *memoryPipelineRepository) ResolvePipelineKeys(_ context.Context, input application.ResolvePipelineKeysInput) (application.ResolvedPipelineKeys, error) {
	var resolved application.ResolvedPipelineKeys
	for _, key := range input.PipelineKeys {
		revisions := repository.revisions[key]
		if len(revisions) == 0 || revisions[0].ArchivedAt != nil {
			resolved.Missing = append(resolved.Missing, key)
			continue
		}
		active := false
		for _, revision := range revisions {
			active = active || revision.IsActive
		}
		if !active {
			resolved.Inactive = append(resolved.Inactive, key)
		}
	}
	return resolved, nil
}

// pipelineName is the display name test fixtures give a pipeline key.
func pipelineName(key string) string { return "Pipeline " + key }

func pipelineManagerPrincipal(organizationID domain.ID) domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: organizationID,
		MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionPipelineRead, domain.PermissionPipelineWrite, domain.PermissionPipelineActivate}))
}

func pipelineDefinition(id string, outcome approvalblock.Outcome, condition approvalblock.Condition) approvalblock.Definition {
	condition.ID = uuid.NewString()
	return approvalblock.Definition{
		ID: id, Name: id, Type: approvalblock.TypeCEL, Enabled: true, Timeout: "50ms", MaxAttempts: 1,
		CEL: &approvalblock.CELConfig{When: condition, OnMatch: outcome, Reason: "Configured reason."},
	}
}

// The pipelines index carries the caller's pipeline.read scope: every
// pipeline for an organization-wide grant, the bound pipelines for a
// scoped one, and forbidden when the permission is held nowhere.
func TestListNarrowsToTheReadScope(t *testing.T) {
	repository := newMemoryPipelineRepository()
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(500, 0).UTC()})
	principal := pipelineManagerPrincipal("organization-1")

	if _, err := service.List(context.Background(), principal, "", 50); err != nil {
		t.Fatalf("organization-wide read: %v", err)
	}
	if !repository.listInput.Scope.All {
		t.Fatalf("organization-wide grant must list everything: %#v", repository.listInput.Scope)
	}
	scoped := principal.WithGrants([]domain.Grant{{Permission: domain.PermissionPipelineRead, ScopeKind: domain.ScopePipeline, ScopeID: "refund-bot"}})
	if _, err := service.List(context.Background(), scoped, "", 50); err != nil {
		t.Fatalf("scoped read: %v", err)
	}
	if ids := repository.listInput.Scope.IDs(domain.ScopePipeline); repository.listInput.Scope.All || len(ids) != 1 || ids[0] != "refund-bot" {
		t.Fatalf("scoped grant must narrow the query: %#v", repository.listInput.Scope)
	}
	unscoped := principal.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionRequestRead}))
	if _, err := service.List(context.Background(), unscoped, "", 50); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("no pipeline.read anywhere = %v, want forbidden", err)
	}
}

type stubEscalationPathResolver struct {
	resolved application.ResolvedEscalationPaths
	asked    []string
}

func (resolver *stubEscalationPathResolver) ResolveEscalationPaths(_ context.Context, input application.ResolveEscalationPathsInput) (application.ResolvedEscalationPaths, error) {
	resolver.asked = input.PathKeys
	return resolver.resolved, nil
}

func TestManagementReferencesReportEachDependencyState(t *testing.T) {
	repository := newMemoryPipelineRepository()
	resolver := &stubEscalationPathResolver{resolved: application.ResolvedEscalationPaths{
		Missing: []string{"gone"}, Inactive: []string{"payments"},
	}}
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(300, 0).UTC()}, WithEscalationPathResolver(resolver))
	principal := pipelineManagerPrincipal("organization-1")
	review := pipelineDefinition("review", approvalblock.Human, approvalblock.Field("/request/tool").Equals("stripe.refund"))
	review.CEL.EscalationPath = "payments"
	second := pipelineDefinition("second-look", approvalblock.Human, approvalblock.Field("/request/tool").Equals("stripe.payout"))
	second.CEL.EscalationPath = "payments"
	fine := pipelineDefinition("fine", approvalblock.Human, approvalblock.Field("/request/tool").Equals("stripe.charge"))
	fine.CEL.EscalationPath = "ops"
	// Creation only requires the paths to exist, so the missing default
	// must be introduced after the revision is stored.
	resolver.resolved = application.ResolvedEscalationPaths{}
	created, err := service.CreateRevision(context.Background(), principal, "billing", pipelineName("billing"), []approvalblock.Definition{review, second, fine}, Route{EscalationPath: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	resolver.resolved = application.ResolvedEscalationPaths{Missing: []string{"gone"}, Inactive: []string{"payments"}}

	references, err := service.References(context.Background(), principal, "billing", created.Revision.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resolver.asked, []string{"payments", "ops", "gone"}) {
		t.Fatalf("resolver asked for %v, want block paths then the default", resolver.asked)
	}
	if references.Activatable {
		t.Fatal("revision with an inactive and a missing path reported activatable")
	}
	want := []EscalationPathReference{
		{PathKey: "payments", BlockKeys: []string{"review", "second-look"}, Exists: true, Active: false},
		{PathKey: "ops", BlockKeys: []string{"fine"}, Exists: true, Active: true},
		{PathKey: "gone", BlockKeys: []string{}, Default: true, Exists: false, Active: false},
	}
	if len(references.EscalationPaths) != len(want) {
		t.Fatalf("escalation path references = %+v", references.EscalationPaths)
	}
	for index, reference := range references.EscalationPaths {
		expected := want[index]
		if reference.PathKey != expected.PathKey || !slices.Equal(reference.BlockKeys, expected.BlockKeys) || reference.Default != expected.Default || reference.Exists != expected.Exists || reference.Active != expected.Active {
			t.Fatalf("reference %d = %+v, want %+v", index, reference, expected)
		}
	}
	if len(references.WebhookEndpoints) != 0 || len(references.Blocks) != 0 {
		t.Fatalf("cel-only revision reported endpoints or block problems: %+v", references)
	}

	resolver.resolved = application.ResolvedEscalationPaths{}
	references, err = service.References(context.Background(), principal, "billing", created.Revision.Revision)
	if err != nil || !references.Activatable {
		t.Fatalf("revision with every path active = %+v, %v", references, err)
	}
	if _, err := service.References(context.Background(), domain.Principal{ActorType: domain.ActorHuman, OrganizationID: "organization-1", MembershipID: "membership-1"}, "billing", created.Revision.Revision); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("references without the permission = %v, want forbidden", err)
	}
}
