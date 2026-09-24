package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

func codeHashOf(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}

// Exercises the contact method lifecycle and, most importantly, that an
// unverified address is never delivered to.
// The phone kinds the hosted edition registers, registered here the same
// way so the store treats the rows as it would there.
const (
	kindSMS      domain.ContactMethodKind = "sms"
	kindWhatsApp domain.ContactMethodKind = "whatsapp"
)

func init() {
	if err := domain.RegisterContactKinds(
		domain.ContactKind{Kind: kindSMS, Label: "Text message", Challengeable: true, Consent: true},
		domain.ContactKind{Kind: kindWhatsApp, Label: "WhatsApp", Challengeable: true, Consent: true},
	); err != nil && !strings.Contains(err.Error(), "already registered") {
		panic(err)
	}
}

func TestContactMethodsAgainstRealPostgres(t *testing.T) {
	server := embeddedpgtest.Start(t)
	ctx := context.Background()
	store, err := Open(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	recordAudit(t, store)

	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID := domain.ID(uuid.NewString())
	ownerID := domain.ID(uuid.NewString())
	ownerMembershipID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "acme", OrganizationName: "Acme",
		UserID: ownerID, MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}

	create := func(value, code string, expires time.Time) (domain.ContactMethod, error) {
		return store.CreateContactMethod(ctx, application.CreateContactMethodInput{
			MethodID: domain.ID(uuid.NewString()), UserID: ownerID,
			Kind: domain.ContactEmail, Value: value, CodeHash: codeHashOf(code),
			ExpiresAt: expires, Now: now,
		})
	}

	method, err := create("personal@example.com", "123456", now.Add(15*time.Minute))
	if err != nil || method.Verified() {
		t.Fatalf("CreateContactMethod = %#v, %v", method, err)
	}

	t.Run("the same address twice is a conflict", func(t *testing.T) {
		if _, err := create("personal@example.com", "654321", now.Add(15*time.Minute)); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate address error = %v", err)
		}
	})

	// An address nobody proved must never receive an approval, which is
	// the whole point of the challenge.
	t.Run("an unverified address is not a recipient address", func(t *testing.T) {
		recipients, err := store.eligibleApprovalRecipients(ctx, orgID)
		if err != nil {
			t.Fatalf("eligibleApprovalRecipients: %v", err)
		}
		if len(recipients) != 1 {
			t.Fatalf("recipients = %#v", recipients)
		}
		for _, address := range recipients[0].Emails {
			if address == "personal@example.com" {
				t.Fatalf("unverified address was delivered to: %#v", recipients[0].Emails)
			}
		}
	})

	t.Run("a wrong code costs an attempt and keeps the challenge", func(t *testing.T) {
		if _, err := store.VerifyContactMethod(ctx, application.VerifyContactMethodInput{
			MethodID: method.ID, UserID: ownerID, CodeHash: codeHashOf("000000"),
			MaxAttempts: 5, Now: now,
		}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("wrong code error = %v", err)
		}
		var attempts int64
		if err := store.Pool().QueryRow(ctx, `SELECT verification_attempts FROM contact_methods WHERE id = $1`,
			method.ID).Scan(&attempts); err != nil || attempts != 1 {
			t.Fatalf("attempts = %d, %v", attempts, err)
		}
	})

	t.Run("the attempt budget closes the challenge", func(t *testing.T) {
		spent, err := create("burner@example.com", "111111", now.Add(15*time.Minute))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if _, err := store.VerifyContactMethod(ctx, application.VerifyContactMethodInput{
				MethodID: spent.ID, UserID: ownerID, CodeHash: codeHashOf("999999"),
				MaxAttempts: 2, Now: now,
			}); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("attempt %d error = %v", attempt, err)
			}
		}
		// Even the right code is refused once the budget is spent.
		if _, err := store.VerifyContactMethod(ctx, application.VerifyContactMethodInput{
			MethodID: spent.ID, UserID: ownerID, CodeHash: codeHashOf("111111"),
			MaxAttempts: 2, Now: now,
		}); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("exhausted budget error = %v", err)
		}
		if err := store.DeleteContactMethod(ctx, application.DeleteContactMethodInput{
			MethodID: spent.ID, UserID: ownerID,
		}); err != nil {
			t.Fatalf("DeleteContactMethod: %v", err)
		}
	})

	t.Run("an expired code is refused", func(t *testing.T) {
		stale, err := create("stale@example.com", "222222", now.Add(time.Minute))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := store.VerifyContactMethod(ctx, application.VerifyContactMethodInput{
			MethodID: stale.ID, UserID: ownerID, CodeHash: codeHashOf("222222"),
			MaxAttempts: 5, Now: now.Add(2 * time.Minute),
		}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("expired code error = %v", err)
		}
		// A fresh challenge revives it, and restores the attempt budget.
		if _, err := store.SetContactMethodChallenge(ctx, application.SetContactMethodChallengeInput{
			MethodID: stale.ID, UserID: ownerID, CodeHash: codeHashOf("333333"),
			ExpiresAt: now.Add(30 * time.Minute), Now: now.Add(2 * time.Minute),
		}); err != nil {
			t.Fatalf("SetContactMethodChallenge: %v", err)
		}
		verified, err := store.VerifyContactMethod(ctx, application.VerifyContactMethodInput{
			MethodID: stale.ID, UserID: ownerID, CodeHash: codeHashOf("333333"),
			MaxAttempts: 5, Now: now.Add(3 * time.Minute),
		})
		if err != nil || !verified.Verified() {
			t.Fatalf("rotated challenge = %#v, %v", verified, err)
		}
		if err := store.DeleteContactMethod(ctx, application.DeleteContactMethodInput{
			MethodID: stale.ID, UserID: ownerID,
		}); err != nil {
			t.Fatalf("DeleteContactMethod: %v", err)
		}
	})

	t.Run("the right code verifies, and the address becomes a recipient", func(t *testing.T) {
		verified, err := store.VerifyContactMethod(ctx, application.VerifyContactMethodInput{
			MethodID: method.ID, UserID: ownerID, CodeHash: codeHashOf("123456"),
			MaxAttempts: 5, Now: now,
		})
		if err != nil || !verified.Verified() {
			t.Fatalf("VerifyContactMethod = %#v, %v", verified, err)
		}
		// The challenge is spent, so the same code cannot be replayed.
		if _, err := store.VerifyContactMethod(ctx, application.VerifyContactMethodInput{
			MethodID: method.ID, UserID: ownerID, CodeHash: codeHashOf("123456"),
			MaxAttempts: 5, Now: now,
		}); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("replayed code error = %v", err)
		}
		recipients, err := store.eligibleApprovalRecipients(ctx, orgID)
		if err != nil || len(recipients) != 1 {
			t.Fatalf("eligibleApprovalRecipients = %#v, %v", recipients, err)
		}
		if len(recipients[0].Emails) != 2 {
			t.Fatalf("expected the account address and the confirmed one: %#v", recipients[0].Emails)
		}
	})

	t.Run("methods belong to their owner", func(t *testing.T) {
		stranger := domain.ID(uuid.NewString())
		if err := store.DeleteContactMethod(ctx, application.DeleteContactMethodInput{
			MethodID: method.ID, UserID: stranger,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-user delete error = %v", err)
		}
		if _, err := store.VerifyContactMethod(ctx, application.VerifyContactMethodInput{
			MethodID: method.ID, UserID: stranger, CodeHash: codeHashOf("123456"),
			MaxAttempts: 5, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-user verify error = %v", err)
		}
	})

	// A phone number is a different consent under each kind, so the same
	// number may be held as sms and as whatsapp, but only once per kind.
	// A verified number is a method the ladder can name; it is never an
	// email address, whatever the recipient query unions.
	t.Run("phone numbers are held per kind and never become email addresses", func(t *testing.T) {
		createPhone := func(kind domain.ContactMethodKind, value string) (domain.ContactMethod, error) {
			return store.CreateContactMethod(ctx, application.CreateContactMethodInput{
				MethodID: domain.ID(uuid.NewString()), UserID: ownerID,
				Kind: kind, Value: value, CodeHash: codeHashOf("424242"),
				ExpiresAt: now.Add(15 * time.Minute), Now: now,
			})
		}
		sms, err := createPhone(kindSMS, "+447700900123")
		if err != nil {
			t.Fatalf("create sms method: %v", err)
		}
		if _, err := createPhone(kindWhatsApp, "+447700900123"); err != nil {
			t.Fatalf("create whatsapp method with the same number: %v", err)
		}
		if _, err := createPhone(kindSMS, "+447700900123"); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate sms number error = %v", err)
		}
		if _, err := store.VerifyContactMethod(ctx, application.VerifyContactMethodInput{
			MethodID: sms.ID, UserID: ownerID, CodeHash: codeHashOf("424242"),
			MaxAttempts: 5, Now: now,
		}); err != nil {
			t.Fatalf("verify sms method: %v", err)
		}
		recipients, err := store.eligibleApprovalRecipients(ctx, orgID)
		if err != nil || len(recipients) != 1 {
			t.Fatalf("eligibleApprovalRecipients = %#v, %v", recipients, err)
		}
		for _, address := range recipients[0].Emails {
			if address == "+447700900123" {
				t.Fatalf("a phone number was listed as an email address: %#v", recipients[0].Emails)
			}
		}
		if err := store.attachRecipientMethods(ctx, orgID, recipients); err != nil {
			t.Fatalf("attachRecipientMethods: %v", err)
		}
		var found bool
		for _, method := range recipients[0].Methods {
			if method.ID == sms.ID && method.Kind == kindSMS && method.Address == "+447700900123" {
				found = true
			}
			if method.Kind == kindWhatsApp {
				t.Fatalf("an unverified whatsapp method is a recipient method: %#v", method)
			}
		}
		if !found {
			t.Fatalf("the verified sms method is not a recipient method: %#v", recipients[0].Methods)
		}
	})

	// A WhatsApp number is confirmed from the message side: the pending
	// method whose code matches is verified, every other live challenge
	// for the number spends an attempt, and a number nobody added is
	// absence rather than a wrong answer.
	t.Run("a number is confirmed by the code that comes back from it", func(t *testing.T) {
		stranger := domain.ID(uuid.NewString())
		if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
			OrganizationID: domain.ID(uuid.NewString()), OrganizationSlug: "globex", OrganizationName: "Globex",
			UserID: stranger, MembershipID: domain.ID(uuid.NewString()), DisplayName: "Stranger",
			Email: "stranger@example.com", PasswordHash: "argon2id$test",
			AdmissionSource: domain.AdmissionManual, Now: now,
		}); err != nil {
			t.Fatalf("ActivateOrganization for the stranger: %v", err)
		}
		mine, err := store.CreateContactMethod(ctx, application.CreateContactMethodInput{
			MethodID: domain.ID(uuid.NewString()), UserID: ownerID, Kind: kindWhatsApp,
			Value: "+447911123456", CodeHash: codeHashOf("111222"), ExpiresAt: now.Add(15 * time.Minute), Now: now,
		})
		if err != nil {
			t.Fatalf("create my method: %v", err)
		}
		theirs, err := store.CreateContactMethod(ctx, application.CreateContactMethodInput{
			MethodID: domain.ID(uuid.NewString()), UserID: stranger, Kind: kindWhatsApp,
			Value: "+447911123456", CodeHash: codeHashOf("333444"), ExpiresAt: now.Add(15 * time.Minute), Now: now,
		})
		if err != nil {
			t.Fatalf("create their method: %v", err)
		}
		confirm := func(value, code string) (domain.ContactMethod, error) {
			return store.VerifyContactMethodByValue(ctx, application.VerifyContactMethodByValueInput{
				Kind: kindWhatsApp, Value: value, CodeHash: codeHashOf(code), MaxAttempts: 5, Now: now,
			})
		}
		if _, err := confirm("+447911999999", "111222"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown number = %v, want domain.ErrNotFound", err)
		}
		if _, err := confirm("+447911123456", "999999"); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("wrong code = %v, want domain.ErrInvalid", err)
		}
		var attempts int64
		if err := store.Pool().QueryRow(ctx, `SELECT sum(verification_attempts) FROM contact_methods WHERE id = ANY($1::uuid[])`,
			[]string{string(mine.ID), string(theirs.ID)}).Scan(&attempts); err != nil || attempts != 2 {
			t.Fatalf("attempts after a wrong code = %d, %v, want one on each pending method", attempts, err)
		}
		confirmed, err := confirm("+447911123456", "111222")
		if err != nil || confirmed.ID != mine.ID || !confirmed.Verified() {
			t.Fatalf("right code = %#v, %v", confirmed, err)
		}
		if _, err := confirm("+447911123456", "111222"); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("replayed code = %v, want domain.ErrInvalid (the other challenge is still pending)", err)
		}
		if err := store.DeleteContactMethod(ctx, application.DeleteContactMethodInput{MethodID: theirs.ID, UserID: stranger}); err != nil {
			t.Fatalf("delete their method: %v", err)
		}
		if _, err := confirm("+447911123456", "111222"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("nothing pending = %v, want domain.ErrNotFound", err)
		}
		if err := store.DeleteContactMethod(ctx, application.DeleteContactMethodInput{MethodID: mine.ID, UserID: ownerID}); err != nil {
			t.Fatalf("delete my method: %v", err)
		}
	})

	t.Run("removing an address stops delivery to it", func(t *testing.T) {
		if err := store.DeleteContactMethod(ctx, application.DeleteContactMethodInput{
			MethodID: method.ID, UserID: ownerID,
		}); err != nil {
			t.Fatalf("DeleteContactMethod: %v", err)
		}
		recipients, err := store.eligibleApprovalRecipients(ctx, orgID)
		if err != nil || len(recipients) != 1 || len(recipients[0].Emails) != 1 {
			t.Fatalf("recipients after removal = %#v, %v", recipients, err)
		}
	})
}
