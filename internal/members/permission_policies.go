package members

import (
	"context"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// BuildPolicyAssignments compiles explicit admission permissions into private,
// immutable bundles. The adapter validates current authority and every resource.
func (s *Service) BuildPolicyAssignments(ctx context.Context, actor domain.Principal, policies []domain.PermissionPolicySpec, unattended bool) ([]domain.AssignmentSpec, error) {
	repository, ok := s.roles.(application.PermissionPolicyRepository)
	if !ok {
		return nil, domain.ErrForbidden
	}
	return repository.CompilePermissionPolicies(ctx, application.CompilePermissionPoliciesInput{Actor: actor, Policies: policies, Unattended: unattended, Now: s.clock.Now()})
}
