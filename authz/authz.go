// Package authz answers the two questions every service asks of a
// principal: may it do this to that (Check), and where does it hold the
// permission (Scope), over the grants the principal carries and the
// permission registry in domain. It is exported so an edition's own
// services check permissions exactly as core's do; core's internal
// authorization package builds its decision, claim and grant evaluators
// on top of it.
package authz

import (
	"fmt"

	"github.com/withHumanAI/withHuman/domain"
)

// Denied is the typed forbidden error: what was asked and against what,
// so the HTTP layer can put the permission and resource in the error
// details and logs can say more than "forbidden".
type Denied struct {
	Permission domain.Permission
	Resource   domain.Resource
}

func (d Denied) Error() string {
	if d.Resource.ID == "" {
		return fmt.Sprintf("forbidden: %s on %s", d.Permission, d.Resource.Kind)
	}
	return fmt.Sprintf("forbidden: %s on %s %s", d.Permission, d.Resource.Kind, d.Resource.ID)
}

func (d Denied) Unwrap() error { return domain.ErrForbidden }

// Check answers "may this principal do this to that". A grant satisfies
// the check when it names the permission and its scope is one the
// resource is covered by: the organization always, or the resource's own
// team or agent type. The registry decides what kind of resource the
// permission is checked against; a call with the wrong kind is a
// programming error and fails closed rather than widening access.
func Check(principal domain.Principal, permission domain.Permission, resource domain.Resource) error {
	if !domain.PermissionAllowedForPrincipal(principal, permission) || !Holds(principal.Grants, permission, resource) {
		return Denied{Permission: permission, Resource: resource}
	}
	return nil
}

// Scope answers "where does this principal hold the permission": All
// when an organization-scope grant exists, otherwise the distinct scopes
// held. Repositories turn the set into predicates.
func Scope(principal domain.Principal, permission domain.Permission) domain.ScopeSet {
	var set domain.ScopeSet
	if !domain.PermissionAllowedForPrincipal(principal, permission) {
		return set
	}
	for _, grant := range principal.Grants {
		if grant.Permission != permission {
			continue
		}
		if grant.ScopeKind == domain.ScopeOrganization {
			return domain.ScopeSet{All: true}
		}
		scope := grant.Scope()
		duplicate := false
		for _, existing := range set.Scopes {
			if existing == scope {
				duplicate = true
				break
			}
		}
		if !duplicate {
			set.Scopes = append(set.Scopes, scope)
		}
	}
	return set
}

// HoldsAnywhere is the router's coarse gate and the navigation gate: the
// principal holds the permission at some scope.
func HoldsAnywhere(principal domain.Principal, permission domain.Permission) bool {
	return !Scope(principal, permission).Empty()
}

// Holds is the shared matcher over a grant list. The organization
// resource is always an acceptable target for a team or agent-type
// permission (creating a team, editing the organization pipeline); only
// organization-scope grants cover it. Any other kind mismatch is a
// programming error and fails closed.
func Holds(grants []domain.Grant, permission domain.Permission, resource domain.Resource) bool {
	def, ok := domain.LookupPermission(permission)
	if !ok {
		return false
	}
	if def.Resource != domain.ResourceScope && def.Resource != resource.Kind && resource.Kind != domain.ResourceOrganization {
		return false
	}
	for _, grant := range grants {
		if grant.Covers(permission, resource) {
			return true
		}
	}
	return false
}
