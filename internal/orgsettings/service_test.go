package orgsettings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type clockStub struct{ now time.Time }

func (c clockStub) Now() time.Time { return c.now }

// identityStub holds one organization's policy and records the update it
// was asked for; every other identity method is unreachable here.
type identityStub struct {
	application.IdentityRepository
	policy  domain.OrganizationPolicy
	updated *application.UpdateOrganizationPolicyInput
}

func (s *identityStub) GetOrganizationPolicy(_ context.Context, input application.GetOrganizationPolicyInput) (domain.OrganizationPolicy, error) {
	policy := s.policy
	policy.OrganizationID = input.OrganizationID
	return policy, nil
}

func (s *identityStub) UpdateOrganizationPolicy(_ context.Context, input application.UpdateOrganizationPolicyInput) (domain.OrganizationPolicy, error) {
	s.updated = &input
	return domain.OrganizationPolicy{
		OrganizationID: input.OrganizationID, AdmissionMode: s.policy.AdmissionMode, SessionMaxAge: s.policy.SessionMaxAge,
		MFAMode:              input.MFAMode,
		ApprovalReauthWindow: input.ApprovalReauthWindow, ClaimRetriggerWindow: input.ClaimRetriggerWindow,
		UnroutedRequests: input.UnroutedRequests, RequestVisibility: input.RequestVisibility,
	}, nil
}

func storedPolicy() domain.OrganizationPolicy {
	return domain.OrganizationPolicy{
		AdmissionMode: domain.AdmissionInviteOnly, MFAMode: domain.MFAOptional,
		ApprovalReauthWindow: 24 * time.Hour, SessionMaxAge: 12 * time.Hour, ClaimRetriggerWindow: 15 * time.Minute,
		UnroutedRequests: domain.UnroutedAnyReviewer, RequestVisibility: domain.VisibilityAllReaders,
	}
}

func principalWith(permissions ...domain.Permission) domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants(permissions))
}

func TestPolicyRequiresOrganizationRead(t *testing.T) {
	identities := &identityStub{policy: storedPolicy()}
	service := NewService(identities, clockStub{time.Unix(60_000, 0).UTC()}, nil)

	policy, err := service.Policy(context.Background(), principalWith(domain.PermissionOrganizationRead))
	if err != nil {
		t.Fatal(err)
	}
	if policy.OrganizationID != "org-1" || policy.UnroutedRequests != domain.UnroutedAnyReviewer {
		t.Fatalf("policy = %#v", policy)
	}
	if _, err := service.Policy(context.Background(), principalWith(domain.PermissionRequestRead)); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("without organization.read: %v, want forbidden", err)
	}
	agent := domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", OrganizationID: "org-1"}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionOrganizationRead}))
	if _, err := service.Policy(context.Background(), agent); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("by an agent: %v, want forbidden", err)
	}
}

func TestUpdatePolicyAppliesOnlyTheSetFields(t *testing.T) {
	now := time.Unix(60_000, 0).UTC()
	identities := &identityStub{policy: storedPolicy()}
	service := NewService(identities, clockStub{now}, nil)
	admin := principalWith(domain.PermissionOrganizationRead, domain.PermissionOrganizationSettingsWrite)

	visibility := domain.VisibilityRoutedOnly
	window := 30 * time.Minute
	updated, err := service.UpdatePolicy(context.Background(), admin, PolicyPatch{RequestVisibility: &visibility, ClaimRetriggerWindow: &window})
	if err != nil {
		t.Fatal(err)
	}
	input := identities.updated
	if input == nil || input.OrganizationID != "org-1" || input.ActorMembershipID != "membership-1" || !input.Now.Equal(now) {
		t.Fatalf("update input = %#v", input)
	}
	if input.RequestVisibility != domain.VisibilityRoutedOnly || input.ClaimRetriggerWindow != 30*time.Minute {
		t.Fatalf("set fields were not applied: %#v", input)
	}
	if input.MFAMode != domain.MFAOptional || input.ApprovalReauthWindow != 24*time.Hour || input.UnroutedRequests != domain.UnroutedAnyReviewer {
		t.Fatalf("unset fields did not keep their stored values: %#v", input)
	}
	if updated.RequestVisibility != domain.VisibilityRoutedOnly || updated.SessionMaxAge != 12*time.Hour {
		t.Fatalf("updated policy = %#v", updated)
	}

	// An empty patch is a no-op write of the stored values.
	identities.updated = nil
	if _, err := service.UpdatePolicy(context.Background(), admin, PolicyPatch{}); err != nil {
		t.Fatal(err)
	}
	if identities.updated == nil || identities.updated.ClaimRetriggerWindow != 15*time.Minute {
		t.Fatalf("empty patch input = %#v", identities.updated)
	}
}

func TestUpdatePolicyRequiresSettingsWrite(t *testing.T) {
	identities := &identityStub{policy: storedPolicy()}
	service := NewService(identities, clockStub{time.Unix(60_000, 0).UTC()}, nil)
	mode := domain.MFAReviewersRequired

	if _, err := service.UpdatePolicy(context.Background(), principalWith(domain.PermissionOrganizationRead), PolicyPatch{MFAMode: &mode}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("with organization.read only: %v, want forbidden", err)
	}
	if identities.updated != nil {
		t.Fatal("a refused update reached the repository")
	}
}

func TestUpdatePolicyRefusesInvalidValues(t *testing.T) {
	identities := &identityStub{policy: storedPolicy()}
	service := NewService(identities, clockStub{time.Unix(60_000, 0).UTC()}, nil)
	admin := principalWith(domain.PermissionOrganizationSettingsWrite)

	badMode := domain.MFAMode("sometimes")
	badUnrouted := domain.UnroutedRequestsPolicy("nobody")
	badVisibility := domain.RequestVisibilityPolicy("secret")
	zero := time.Duration(0)
	negative := -time.Minute
	tooLong := 25 * time.Hour
	for name, patch := range map[string]PolicyPatch{
		"mfa mode":                {MFAMode: &badMode},
		"unrouted requests":       {UnroutedRequests: &badUnrouted},
		"request visibility":      {RequestVisibility: &badVisibility},
		"zero reauth window":      {ApprovalReauthWindow: &zero},
		"negative claim window":   {ClaimRetriggerWindow: &negative},
		"claim window over a day": {ClaimRetriggerWindow: &tooLong},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.UpdatePolicy(context.Background(), admin, patch); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("%v, want invalid", err)
			}
			if identities.updated != nil {
				t.Fatal("an invalid update reached the repository")
			}
		})
	}
}
