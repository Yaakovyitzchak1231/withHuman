package notifications

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// kindPager is an edition kind shaped like the hosted text-message kind:
// typed in and proved by a code that its own channel delivers.
const kindPager domain.ContactMethodKind = "pager"

func init() {
	if err := domain.RegisterContactKinds(domain.ContactKind{Kind: kindPager, Label: "Pager", Challengeable: true}); err != nil && !strings.Contains(err.Error(), "already registered") {
		panic(err)
	}
}

// The SMTP channel leaves a code bound for an edition kind alone: the
// kind's own channel delivers it, and a pager number is no mailbox.
func TestSMTPIgnoresEditionKindVerifications(t *testing.T) {
	channel, sent := testSMTPChannel(t, nil)
	if err := channel.SendTransactional(context.Background(), application.SendTransactionalInput{
		Message: application.TransactionalMessage{Kind: application.TransactionalContactVerification, VerificationCode: "123456", ExpiresAt: time.Unix(1_700_000_000, 0).Add(15 * time.Minute)},
		Address: "555-0100", Kind: kindPager, ContactMethodID: "cm-pager",
		Recipient: application.NotificationRecipient{Targets: map[domain.ContactMethodKind][]application.RecipientTarget{kindPager: {{MethodID: "cm-pager", Address: "555-0100"}}}},
	}); err != nil || len(*sent) != 0 {
		t.Fatalf("SMTP handled an edition kind's code: %v, %d mails", err, len(*sent))
	}
}
