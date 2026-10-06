package authn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type dispatcherStub struct {
	sent []application.SendTransactionalInput
	err  error
}

func (d *dispatcherStub) SendApprovalPrompt(context.Context, application.SendApprovalPromptInput) error {
	return nil
}

func (d *dispatcherStub) SendTransactional(_ context.Context, input application.SendTransactionalInput) error {
	d.sent = append(d.sent, input)
	return d.err
}

func passwordTestService(repository *identityRepositoryStub, now time.Time) (*Service, *dispatcherStub) {
	repository.user = &domain.User{ID: "user-1", PrimaryEmail: "owner@example.com", Status: domain.UserActive}
	service := NewService(repository, nil, nil, nil, nil, passwordHasherStub{}, serviceClock{now}, ServiceConfig{
		SessionTTL: 24 * time.Hour, PublicOrigin: "https://app.example.com/",
	})
	dispatcher := &dispatcherStub{}
	service.RegisterNotifications(dispatcher)
	return service, dispatcher
}

func humanPrincipal(sessionID domain.ID) domain.Principal {
	return domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "organization-1",
		Auth: domain.AuthContext{SessionID: sessionID}}
}

func TestChangePasswordReplacesCredentialAndRevokesOtherSessions(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := loginTestRepository()
	service, _ := passwordTestService(repository, now)
	err := service.ChangePassword(context.Background(), humanPrincipal("session-keep"), ChangePasswordInput{
		CurrentPassword: "correct horse battery staple", NewPassword: "a brand new passphrase",
	})
	if err != nil {
		t.Fatalf("change password: %v", err)
	}
	if len(repository.setCredentials) != 1 || repository.setCredentials[0].UserID != "user-1" ||
		repository.setCredentials[0].PasswordHash != "hashed:a brand new passphrase" {
		t.Fatalf("credential not replaced: %+v", repository.setCredentials)
	}
	if len(repository.revokedUserSessions) != 1 || repository.revokedUserSessions[0].ExceptSessionID != "session-keep" {
		t.Fatalf("other sessions not revoked with the acting one kept: %+v", repository.revokedUserSessions)
	}
}

func TestChangePasswordRefusesWrongCurrentPassword(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := loginTestRepository()
	service, _ := passwordTestService(repository, now)
	err := service.ChangePassword(context.Background(), humanPrincipal("s"), ChangePasswordInput{
		CurrentPassword: "not the password", NewPassword: "a brand new passphrase",
	})
	if !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("expected ErrPasswordMismatch, got %v", err)
	}
	if len(repository.setCredentials) != 0 {
		t.Fatal("credential must not change on a wrong current password")
	}
}

func TestChangePasswordValidatesTheNewOne(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := loginTestRepository()
	service, _ := passwordTestService(repository, now)
	for _, password := range []string{"short", "correct horse battery staple"} {
		err := service.ChangePassword(context.Background(), humanPrincipal("s"), ChangePasswordInput{
			CurrentPassword: "correct horse battery staple", NewPassword: password,
		})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("password %q: expected ErrInvalid, got %v", password, err)
		}
	}
}

func TestRequestPasswordResetMailsALinkForAnAccountWithAPassword(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := loginTestRepository()
	service, dispatcher := passwordTestService(repository, now)
	if err := service.RequestPasswordReset(context.Background(), "  Owner@Example.com "); err != nil {
		t.Fatalf("request reset: %v", err)
	}
	if len(repository.passwordResets) != 1 {
		t.Fatalf("expected one reset row, got %d", len(repository.passwordResets))
	}
	reset := repository.passwordResets[0]
	if reset.UserID != "user-1" || !reset.ExpiresAt.Equal(now.Add(PasswordResetTTL)) {
		t.Fatalf("unexpected reset row: %+v", reset)
	}
	if len(dispatcher.sent) != 1 {
		t.Fatalf("expected one mail, got %d", len(dispatcher.sent))
	}
	message := dispatcher.sent[0]
	if message.Address != "owner@example.com" || message.Message.Kind != application.TransactionalPasswordReset {
		t.Fatalf("unexpected message: %+v", message)
	}
	if !strings.HasPrefix(message.Message.ResetURL, "https://app.example.com/reset/whr_"+string(reset.ResetID)+"_") {
		t.Fatalf("unexpected reset URL %q", message.Message.ResetURL)
	}
}

func TestRequestPasswordResetIsSilentForUnknownOrPasswordlessAccounts(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for name, login := range map[string]application.LoginOrganizations{
		"unknown":      {},
		"passwordless": {UserID: "user-1", HasPassword: false},
	} {
		repository := loginTestRepository()
		repository.loginOrganizations = login
		service, dispatcher := passwordTestService(repository, now)
		if err := service.RequestPasswordReset(context.Background(), "owner@example.com"); err != nil {
			t.Fatalf("%s: expected no error, got %v", name, err)
		}
		if len(repository.passwordResets) != 0 || len(dispatcher.sent) != 0 {
			t.Fatalf("%s: nothing should be stored or sent", name)
		}
	}
}

func TestRequestPasswordResetSwallowsMailFailures(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := loginTestRepository()
	service, dispatcher := passwordTestService(repository, now)
	dispatcher.err = errors.New("smtp down")
	if err := service.RequestPasswordReset(context.Background(), "owner@example.com"); err != nil {
		t.Fatalf("a mail failure must not surface: %v", err)
	}
}

func TestCompletePasswordResetIsSingleUseAndRevokesEverySession(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := loginTestRepository()
	service, dispatcher := passwordTestService(repository, now)
	if err := service.RequestPasswordReset(context.Background(), "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(dispatcher.sent[0].Message.ResetURL, "https://app.example.com/reset/")

	preview, err := service.PreviewPasswordReset(context.Background(), token)
	if err != nil || !preview.ExpiresAt.Equal(now.Add(PasswordResetTTL)) {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	// A rejected password leaves the link usable.
	if err := service.CompletePasswordReset(context.Background(), token, "short"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for a short password, got %v", err)
	}
	if err := service.CompletePasswordReset(context.Background(), token, "a brand new passphrase"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(repository.setCredentials) != 1 || repository.setCredentials[0].PasswordHash != "hashed:a brand new passphrase" {
		t.Fatalf("credential not set: %+v", repository.setCredentials)
	}
	if len(repository.revokedUserSessions) != 1 || repository.revokedUserSessions[0].ExceptSessionID != "" {
		t.Fatalf("every session must be revoked: %+v", repository.revokedUserSessions)
	}
	if err := service.CompletePasswordReset(context.Background(), token, "another new passphrase"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a consumed link must be refused, got %v", err)
	}
	if _, err := service.PreviewPasswordReset(context.Background(), token); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a consumed link must not preview, got %v", err)
	}
}

func TestPasswordResetRefusesExpiredAndMalformedTokens(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := loginTestRepository()
	service, dispatcher := passwordTestService(repository, now)
	if err := service.RequestPasswordReset(context.Background(), "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(dispatcher.sent[0].Message.ResetURL, "https://app.example.com/reset/")
	later := NewService(repository, nil, nil, nil, nil, passwordHasherStub{}, serviceClock{now.Add(PasswordResetTTL + time.Second)}, ServiceConfig{})
	if err := later.CompletePasswordReset(context.Background(), token, "a brand new passphrase"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired link: expected ErrNotFound, got %v", err)
	}
	for _, bad := range []string{"", "whr_x", "whi_" + token[4:], "not a token"} {
		if _, err := service.PreviewPasswordReset(context.Background(), bad); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("token %q: expected ErrNotFound, got %v", bad, err)
		}
	}
}

func TestLoginLocalWrongPasswordIsInvalidCredentials(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	service := loginTestService(loginTestRepository(), now)
	_, err := service.LoginLocal(context.Background(), LocalLoginInput{Email: "owner@example.com", Password: "wrong"})
	if !errors.Is(err, ErrInvalidCredentials) || !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrInvalidCredentials (an ErrUnauthenticated), got %v", err)
	}
}

func (*dispatcherStub) SendApprovalDigest(context.Context, application.SendApprovalDigestInput) error {
	return nil
}
