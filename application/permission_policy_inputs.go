package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

type PermissionPolicyRepository interface {
	CompilePermissionPolicies(context.Context, CompilePermissionPoliciesInput) ([]domain.AssignmentSpec, error)
	ReadPermissionPolicies(context.Context, ReadPermissionPoliciesInput) (PermissionPoliciesPage, error)
	MutatePermissionPolicy(context.Context, MutatePermissionPolicyInput) (domain.PermissionPolicy, error)
}

type PermissionPoliciesPage struct {
	Name           string                      `json:"name"`
	Policies       []domain.PermissionPolicy   `json:"policies"`
	Resources      []domain.PermissionResource `json:"resources"`
	CanCreate      bool                        `json:"can_create"`
	IsOwner        bool                        `json:"is_owner"`
	ReadOnlyReason string                      `json:"read_only_reason,omitempty"`
}

type ReadPermissionPoliciesInput struct {
	Actor         domain.Principal
	PrincipalKind domain.PrincipalKind
	PrincipalID   domain.ID
}

func (input ReadPermissionPoliciesInput) Validate() error {
	if input.Actor.ActorType != domain.ActorHuman || input.Actor.MembershipID == "" || (input.PrincipalKind == domain.PrincipalAPIKey && input.Actor.Auth.Method == domain.AuthMethodAPIKey) {
		return domain.ErrForbidden
	}
	if input.PrincipalKind != domain.PrincipalMembership && input.PrincipalKind != domain.PrincipalTeam && input.PrincipalKind != domain.PrincipalAPIKey {
		return invalidField("principal kind", "must be membership, team, or api_key")
	}
	return firstError([]error{requiredID("organization ID", input.Actor.OrganizationID), requiredID("principal ID", input.PrincipalID)})
}

type MutatePermissionPolicyInput struct {
	ReadPermissionPoliciesInput
	PolicyID       domain.ID
	Spec           domain.PermissionPolicySpec
	Revision       int64
	Delete         bool
	IdempotencyKey string
	Now            time.Time
}

func (input MutatePermissionPolicyInput) Validate() error {
	if err := input.ReadPermissionPoliciesInput.Validate(); err != nil {
		return err
	}
	// Handing out authority is a human act: a session, never a key, so
	// the freshness ceremony (which keys are exempt from) always applies.
	if input.Actor.Auth.Method == domain.AuthMethodAPIKey {
		return domain.ErrForbidden
	}
	if len(input.IdempotencyKey) == 0 || len(input.IdempotencyKey) > 200 {
		return invalidField("idempotency key", "must contain 1 to 200 characters")
	}
	if input.PolicyID != "" && input.Revision < 1 {
		return invalidField("revision", "must be the revision you read")
	}
	if input.Delete && input.PolicyID == "" {
		return invalidField("policy ID", "is required")
	}
	if err := requiredTime("current time", input.Now); err != nil {
		return err
	}
	if !input.Delete {
		if input.PrincipalKind == domain.PrincipalAPIKey {
			return input.Spec.ValidateFor(domain.HolderOrganizationAPIKey)
		}
		return input.Spec.Validate()
	}
	return nil
}

// CompilePermissionPoliciesInput prepares private immutable bundles for admission.
type CompilePermissionPoliciesInput struct {
	Actor      domain.Principal
	Policies   []domain.PermissionPolicySpec
	Unattended bool
	Now        time.Time
}

func (input CompilePermissionPoliciesInput) Validate() error {
	if err := (ReadPermissionPoliciesInput{Actor: input.Actor, PrincipalKind: domain.PrincipalMembership, PrincipalID: input.Actor.MembershipID}).Validate(); err != nil {
		return err
	}
	if len(input.Policies) == 0 || len(input.Policies) > 20 {
		return invalidField("permission policies", "select between 1 and 20 policies")
	}
	for _, policy := range input.Policies {
		if err := policy.Validate(); err != nil {
			return err
		}
	}
	return requiredTime("current time", input.Now)
}
