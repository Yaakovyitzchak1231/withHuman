package authn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// PasswordResetTTL bounds a reset link. An hour is long enough to open an
// email and short enough that a leaked link is stale by the time it is
// found.
const PasswordResetTTL = time.Hour

// passwordResetPrefix is the opaque token prefix for reset links.
const passwordResetPrefix = "whr"

// ErrPasswordMismatch is the change-password outcome when the current
// password is wrong. It is distinct from ErrUnauthenticated so a client
// does not mistake it for a dead session.
var ErrPasswordMismatch = errors.New("the current password is wrong")

// RegisterNotifications wires the channel registry that carries password
// reset mail. Without it a reset request is accepted and logged, never
// delivered.
func (s *Service) RegisterNotifications(dispatcher application.NotificationDispatcher) {
	s.notifications = dispatcher
}

type ChangePasswordInput struct {
	CurrentPassword string
	NewPassword     string
}

// ChangePassword replaces the signed-in person's password after proving
// the current one, then signs out every other session. The acting session
// stays, so the browser that made the change is not bounced to login.
func (s *Service) ChangePassword(ctx context.Context, principal domain.Principal, input ChangePasswordInput) error {
	if principal.ActorType != domain.ActorHuman || principal.ActorID == "" {
		return domain.ErrForbidden
	}
	user, err := s.identities.GetUser(ctx, application.GetUserInput{UserID: principal.ActorID})
	if err != nil {
		return err
	}
	if _, err := s.local.Complete(ctx, map[string]string{"email": user.PrimaryEmail, "password": input.CurrentPassword}); err != nil {
		if errors.Is(err, domain.ErrUnauthenticated) {
			return ErrPasswordMismatch
		}
		return err
	}
	if err := validateNewPassword(input.NewPassword); err != nil {
		return err
	}
	if input.NewPassword == input.CurrentPassword {
		return fmt.Errorf("the new password must differ from the current one: %w", domain.ErrInvalid)
	}
	return s.setPassword(ctx, user.ID, input.NewPassword, principal.Auth.SessionID)
}

// RequestPasswordReset mails a single-use reset link to the address when
// an active account with a password uses it. It answers the same way
// whatever it finds, so the form is not an oracle for which addresses
// exist. Delivery is best effort: a mail failure is logged, never
// surfaced, because the alternative is confirming the account.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return fmt.Errorf("email is required: %w", domain.ErrInvalid)
	}
	login, err := s.identities.ListLoginOrganizations(ctx, application.ListLoginOrganizationsInput{Email: email})
	if err != nil {
		return err
	}
	if login.UserID == "" || !login.HasPassword {
		return nil
	}
	resetID := domain.ID(uuid.NewString())
	token, secretHash, err := MintOpaqueToken(passwordResetPrefix, resetID)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	expiresAt := now.Add(PasswordResetTTL)
	if err := s.identities.CreatePasswordReset(ctx, application.CreatePasswordResetInput{
		ResetID: resetID, UserID: login.UserID, SecretHash: secretHash, IssuedAt: now, ExpiresAt: expiresAt,
	}); err != nil {
		return err
	}
	if s.notifications == nil {
		slog.WarnContext(ctx, "password reset requested with no notification channel registered")
		return nil
	}
	err = s.notifications.SendTransactional(ctx, application.SendTransactionalInput{
		Message: application.TransactionalMessage{
			Kind: application.TransactionalPasswordReset, ResetURL: s.resetURL(token), ExpiresAt: expiresAt,
		},
		Recipient: application.NotificationRecipient{Emails: []string{email}},
		Address:   email,
	})
	if err != nil {
		slog.WarnContext(ctx, "password reset mail failed", "error", err)
	}
	return nil
}

// PasswordResetPreview is what the reset page may learn from the token
// alone: that it is live and when it stops being.
type PasswordResetPreview struct {
	ExpiresAt time.Time
}

// PreviewPasswordReset checks a reset link without consuming it.
func (s *Service) PreviewPasswordReset(ctx context.Context, token string) (PasswordResetPreview, error) {
	resetID, secret, err := ParseOpaqueToken(token, passwordResetPrefix)
	if err != nil {
		return PasswordResetPreview{}, domain.ErrNotFound
	}
	record, err := s.identities.GetPasswordReset(ctx, application.GetPasswordResetInput{
		ResetID: domain.ID(resetID), SecretHash: HashToken(secret), Now: s.clock.Now(),
	})
	if err != nil {
		return PasswordResetPreview{}, err
	}
	return PasswordResetPreview{ExpiresAt: record.ExpiresAt}, nil
}

// CompletePasswordReset consumes the link, sets the password, and signs
// the person out everywhere. The password is validated before the token
// is consumed so a rejected password does not burn the link.
func (s *Service) CompletePasswordReset(ctx context.Context, token, password string) error {
	resetID, secret, err := ParseOpaqueToken(token, passwordResetPrefix)
	if err != nil {
		return domain.ErrNotFound
	}
	if err := validateNewPassword(password); err != nil {
		return err
	}
	record, err := s.identities.ConsumePasswordReset(ctx, application.ConsumePasswordResetInput{
		ResetID: domain.ID(resetID), SecretHash: HashToken(secret), Now: s.clock.Now(),
	})
	if err != nil {
		return err
	}
	return s.setPassword(ctx, record.UserID, password, "")
}

func (s *Service) setPassword(ctx context.Context, userID domain.ID, password string, keepSession domain.ID) error {
	hash, err := s.passwords.Hash(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	now := s.clock.Now()
	if err := s.identities.SetLocalCredential(ctx, application.SetLocalCredentialInput{
		UserID: userID, PasswordHash: hash, Now: now,
	}); err != nil {
		return err
	}
	return s.identities.RevokeUserSessions(ctx, application.RevokeUserSessionsInput{
		UserID: userID, ExceptSessionID: keepSession, Now: now,
	})
}

func (s *Service) resetURL(rawToken string) string {
	return strings.TrimRight(s.config.PublicOrigin, "/") + "/reset/" + rawToken
}
