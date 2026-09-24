package authn

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

type Capability string

const (
	CapabilityPassword         Capability = "password"
	CapabilityPasswordless     Capability = "passwordless"
	CapabilitySocial           Capability = "social"
	CapabilityPasskeys         Capability = "passkeys"
	CapabilityStepUp           Capability = "step_up"
	CapabilitySAML             Capability = "saml"
	CapabilityOIDC             Capability = "oidc"
	CapabilitySCIMUsers        Capability = "scim_users"
	CapabilitySCIMGroups       Capability = "scim_groups"
	CapabilityCustomAttributes Capability = "custom_attributes"
	CapabilityCustomDomains    Capability = "custom_domains"
)

type Assertion struct {
	ProviderKey      string
	Issuer           string
	Subject          string
	Email            string
	EmailVerified    bool
	DisplayName      string
	AuthenticatedAt  time.Time
	Methods          []string
	Assurance        string
	ProviderSession  string
	OrganizationHint string
}

type Flow struct {
	ID          string
	RedirectURL string
	ExpiresAt   time.Time
}

type Authenticator interface {
	Key() string
	Capabilities() []Capability
	Begin(context.Context, map[string]string) (Flow, error)
	Complete(context.Context, map[string]string) (Assertion, error)
}

type Session struct {
	ID              domain.ID
	UserID          domain.ID
	ExpiresAt       time.Time
	AuthenticatedAt time.Time
	Assurance       string
}

type SessionPort interface {
	CompatibleAuthenticatorKeys() []string
	Create(context.Context, domain.ID, domain.ID, Assertion) (Session, string, error)
	Authenticate(context.Context, string) (Session, error)
	Rotate(context.Context, string) (Session, string, error)
	Revoke(context.Context, domain.ID) error
}
