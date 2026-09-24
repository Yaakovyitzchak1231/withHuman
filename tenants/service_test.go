package tenants

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type repoStub struct {
	takenSlugs map[string]bool
	activated  []application.ActivateOrganizationInput
	status     []application.SetOrganizationStatusInput
	statusErr  error
}

func (stub *repoStub) ActivateOrganization(_ context.Context, input application.ActivateOrganizationInput) (application.ActivationResult, error) {
	if err := input.Validate(); err != nil {
		return application.ActivationResult{}, err
	}
	if stub.takenSlugs[input.OrganizationSlug] {
		return application.ActivationResult{}, domain.ErrConflict
	}
	stub.activated = append(stub.activated, input)
	return application.ActivationResult{
		Organization: domain.Organization{ID: input.OrganizationID, Slug: input.OrganizationSlug, Status: domain.OrganizationActive},
		Principal:    domain.Principal{ActorType: domain.ActorHuman, ActorID: input.UserID, OrganizationID: input.OrganizationID, MembershipID: input.MembershipID},
	}, nil
}

func (stub *repoStub) SetOrganizationStatus(_ context.Context, input application.SetOrganizationStatusInput) (domain.Organization, error) {
	if err := input.Validate(); err != nil {
		return domain.Organization{}, err
	}
	if stub.statusErr != nil {
		return domain.Organization{}, stub.statusErr
	}
	stub.status = append(stub.status, input)
	return domain.Organization{ID: input.OrganizationID, Status: input.Status, StatusReason: input.Reason}, nil
}

func (stub *repoStub) PurgeOrganization(context.Context, application.PurgeOrganizationInput) (bool, error) {
	return false, nil
}

func (stub *repoStub) GetOrganization(context.Context, application.GetOrganizationInput) (domain.Organization, error) {
	return domain.Organization{}, domain.ErrNotFound
}

type orchestratorStub struct {
	started  []application.StartOrganizationDeletionInput
	canceled []application.CancelOrganizationDeletionInput
	startErr error
}

func (stub *orchestratorStub) StartOrganizationDeletion(_ context.Context, input application.StartOrganizationDeletionInput) error {
	if stub.startErr != nil {
		return stub.startErr
	}
	stub.started = append(stub.started, input)
	return nil
}

func (stub *orchestratorStub) CancelOrganizationDeletion(_ context.Context, input application.CancelOrganizationDeletionInput) error {
	stub.canceled = append(stub.canceled, input)
	return nil
}

type hooksStub struct {
	activated int
	inputs    []OrganizationActivatedInput
	fail      bool
}

func (stub *hooksStub) OrganizationActivated(_ context.Context, input OrganizationActivatedInput) error {
	stub.activated++
	stub.inputs = append(stub.inputs, input)
	if stub.fail {
		return errors.New("billing is down")
	}
	return nil
}
func (stub *hooksStub) OrganizationSuspended(context.Context, OrganizationSuspendedInput) error {
	return nil
}
func (stub *hooksStub) OrganizationResumed(context.Context, OrganizationResumedInput) error {
	return nil
}
func (stub *hooksStub) DeletionScheduled(context.Context, DeletionScheduledInput) error { return nil }

func newTestService(repo *repoStub, orchestrator *orchestratorStub, hooks TenantHooks) *Service {
	return NewService(repo, orchestrator, hooks, fixedClock{time.Unix(1_000, 0).UTC()}, slog.Default(), Config{})
}

func TestActivateGeneratesSlugAndSuffixesOnCollision(t *testing.T) {
	repo := &repoStub{takenSlugs: map[string]bool{"acme-rockets": true}}
	hooks := &hooksStub{}
	service := newTestService(repo, &orchestratorStub{}, hooks)

	result, err := service.ActivateOrganization(context.Background(), ActivateOrganizationInput{
		OrganizationName: "  Acme Rockets!  ", DisplayName: "  Ada Lovelace ", Email: "ADA@Example.com",
		PasswordHash: "hash", AdmissionSource: domain.AdmissionSignup,
	})
	if err != nil {
		t.Fatal(err)
	}
	slug := result.Organization.Slug
	if !strings.HasPrefix(slug, "acme-rockets-") || len(slug) != len("acme-rockets-")+slugSuffixLength {
		t.Fatalf("collision did not produce a suffixed slug: %q", slug)
	}
	if repo.activated[0].Email != "ada@example.com" {
		t.Fatalf("email was not normalized: %q", repo.activated[0].Email)
	}
	// The hook sees the committed organization and the founder as
	// normalized, so an adapter never re-trims or re-lowercases.
	if len(hooks.inputs) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(hooks.inputs))
	}
	hooked := hooks.inputs[0]
	if hooked.Organization.ID != result.Organization.ID || hooked.OwnerUserID != result.Principal.ActorID {
		t.Fatalf("hook input = %+v, want the activated organization and owner", hooked)
	}
	if hooked.OwnerEmail != "ada@example.com" || hooked.OwnerDisplayName != "Ada Lovelace" {
		t.Fatalf("hook owner = (%q, %q), want normalized email and trimmed name", hooked.OwnerEmail, hooked.OwnerDisplayName)
	}
}

func TestActivateWithExplicitSlugDoesNotRetry(t *testing.T) {
	repo := &repoStub{takenSlugs: map[string]bool{"taken": true}}
	service := newTestService(repo, &orchestratorStub{}, nil)
	_, err := service.ActivateOrganization(context.Background(), ActivateOrganizationInput{
		OrganizationName: "Acme", OrganizationSlug: "taken", DisplayName: "Ada",
		Email: "ada@example.com", AdmissionSource: domain.AdmissionManual,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("explicit slug collision = %v, want conflict", err)
	}
	if len(repo.activated) != 0 {
		t.Fatal("a conflicting explicit slug still activated")
	}
}

func TestHookFailureDoesNotFailActivation(t *testing.T) {
	repo := &repoStub{}
	hooks := &hooksStub{fail: true}
	service := newTestService(repo, &orchestratorStub{}, hooks)
	if _, err := service.ActivateOrganization(context.Background(), ActivateOrganizationInput{
		OrganizationName: "Acme", DisplayName: "Ada", Email: "ada@example.com",
		AdmissionSource: domain.AdmissionSignup,
	}); err != nil {
		t.Fatalf("hook failure surfaced as activation failure: %v", err)
	}
	if hooks.activated != 1 {
		t.Fatalf("hook fired %d times, want 1", hooks.activated)
	}
}

func TestScheduleDeletionStartsWorkflowBeforeStatusWrite(t *testing.T) {
	repo := &repoStub{}
	orchestrator := &orchestratorStub{}
	service := newTestService(repo, orchestrator, nil)
	organization, err := service.ScheduleDeletion(context.Background(), LifecycleActionInput{
		OrganizationID: "org-1", Reason: "offboarding", ActorType: "human", ActorID: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if organization.Status != domain.OrganizationPendingDeletion {
		t.Fatalf("status = %s", organization.Status)
	}
	if len(orchestrator.started) != 1 || len(repo.status) != 1 {
		t.Fatalf("workflow starts=%d status writes=%d", len(orchestrator.started), len(repo.status))
	}
	wantPurge := time.Unix(1_000, 0).UTC().Add(defaultDeletionGracePeriod)
	if !orchestrator.started[0].PurgeAt.Equal(wantPurge) || !repo.status[0].DeletionScheduledFor.Equal(wantPurge) {
		t.Fatalf("purge schedule mismatch: workflow=%v status=%v", orchestrator.started[0].PurgeAt, repo.status[0].DeletionScheduledFor)
	}
}

func TestScheduleDeletionRefusesWhenWorkflowCannotStart(t *testing.T) {
	repo := &repoStub{}
	orchestrator := &orchestratorStub{startErr: errors.New("temporal is down")}
	service := newTestService(repo, orchestrator, nil)
	if _, err := service.ScheduleDeletion(context.Background(), LifecycleActionInput{
		OrganizationID: "org-1", ActorType: "human",
	}); err == nil {
		t.Fatal("deletion was scheduled without a purge workflow")
	}
	if len(repo.status) != 0 {
		t.Fatal("status changed although the workflow never started")
	}
}

func TestResumeCancelsDeletionWorkflow(t *testing.T) {
	repo := &repoStub{}
	orchestrator := &orchestratorStub{}
	service := newTestService(repo, orchestrator, nil)
	if _, err := service.CancelDeletion(context.Background(), LifecycleActionInput{
		OrganizationID: "org-1", ActorType: "human",
	}); err != nil {
		t.Fatal(err)
	}
	if len(orchestrator.canceled) != 1 {
		t.Fatalf("workflow cancellations = %d, want 1", len(orchestrator.canceled))
	}
}

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Acme Rockets":          "acme-rockets",
		"  --Weird__ (Name)  ":  "weird-name",
		"日本語":                   "org",
		strings.Repeat("a", 60): strings.Repeat("a", 40),
	}
	for name, want := range tests {
		if got := slugify(name); got != want {
			t.Fatalf("slugify(%q) = %q, want %q", name, got, want)
		}
	}
}
