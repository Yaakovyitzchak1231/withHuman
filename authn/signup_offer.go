package authn

import (
	"context"
	"log/slog"
	"strings"

	"github.com/withHumanAI/withHuman/application"
)

// SignupOffer is an edition's answer to a password login for an email
// that has no account: the hosted edition mails the address a link to
// create one. The login is refused the same way whatever the edition
// does, so the sign-in form never says whether an address has an
// account; only the mailbox learns it. OfferSignup runs inside the
// refused login, so it must return quickly and report its own failures.
type SignupOffer interface {
	OfferSignup(ctx context.Context, email string)
}

// RegisterSignupOffer wires the edition's signup offer.
func (s *Service) RegisterSignupOffer(offer SignupOffer) { s.signupOffer = offer }

// OffersSignup reports whether a refused password login for an email
// without an account is offered a signup, so the sign-in form can point
// the person to their inbox.
func (s *Service) OffersSignup() bool { return s.signupOffer != nil }

// offerSignup hands an email without an account to the edition's offer.
// An active user with the address has an account, with a password or
// without one, and is offered nothing.
func (s *Service) offerSignup(ctx context.Context, email string) {
	if s.signupOffer == nil {
		return
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return
	}
	login, err := s.identities.ListLoginOrganizations(ctx, application.ListLoginOrganizationsInput{Email: email})
	if err != nil {
		slog.WarnContext(ctx, "could not check an email for a signup offer", "error", err)
		return
	}
	if login.UserID != "" {
		return
	}
	s.signupOffer.OfferSignup(ctx, email)
}
