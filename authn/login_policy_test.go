package authn

import (
	"context"
	"strings"

	"github.com/withHumanAI/withHuman/domain"
)

// loginPolicyStub is an edition's login policy for tests: which
// organizations sign in externally (all of them, or a named few), which
// emails are routed to an organization before any account exists, and
// which organizations vouch for an email's domain.
type loginPolicyStub struct {
	everywhere bool
	external   map[domain.ID]bool
	routed     map[string]domain.Organization
	vouches    map[domain.ID]bool
}

func externalLoginEverywhere() loginPolicyStub { return loginPolicyStub{everywhere: true} }

func externalLoginFor(ids ...domain.ID) loginPolicyStub {
	policy := loginPolicyStub{external: map[domain.ID]bool{}}
	for _, id := range ids {
		policy.external[id] = true
	}
	return policy
}

func (p loginPolicyStub) RequiresExternalLogin(_ context.Context, organizationID domain.ID) (bool, error) {
	return p.everywhere || p.external[organizationID], nil
}

func (p loginPolicyStub) RouteEmail(_ context.Context, email string) (domain.Organization, bool, error) {
	organization, ok := p.routed[strings.ToLower(email)]
	return organization, ok, nil
}

func (p loginPolicyStub) VouchesForEmail(_ context.Context, organizationID domain.ID, _ string) (bool, error) {
	return p.vouches[organizationID], nil
}
