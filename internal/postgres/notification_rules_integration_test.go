package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

func TestNotificationRulesAgainstRealPostgres(t *testing.T) {
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
	orgID, userID, membershipID := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "acme", OrganizationName: "Acme",
		UserID: userID, MembershipID: membershipID, DisplayName: "Owner", Email: "owner@example.com",
		PasswordHash: "argon2id$test", AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	generation := installSlackFixture(t, store, orgID, now)
	slackLink, err := store.LinkOrganizationContactMethod(ctx, application.LinkOrganizationContactMethodInput{
		MethodID: domain.ID(uuid.NewString()), UserID: userID, OrganizationID: orgID,
		SlackInstallationID: generation, Kind: domain.ContactSlack, ExternalID: "U0OWNER", Label: "@owner", Now: now,
	})
	if err != nil {
		t.Fatalf("link slack: %v", err)
	}

	t.Run("a ladder is saved whole and read back in order", func(t *testing.T) {
		rules, err := store.ReplaceNotificationRules(ctx, application.ReplaceNotificationRulesInput{
			UserID: userID, Urgency: domain.NotificationInterrupt, Now: now,
			Rungs: []application.NotificationRung{
				{Delay: 0, ContactMethodID: slackLink.ID},
				{Delay: 5 * time.Minute},
			},
		})
		if err != nil || len(rules) != 2 {
			t.Fatalf("ReplaceNotificationRules = %+v, %v", rules, err)
		}
		if rules[0].ContactMethodID != slackLink.ID || rules[1].ContactMethodID != "" || rules[1].Delay != 5*time.Minute {
			t.Fatalf("rules = %+v", rules)
		}
	})

	t.Run("a rung cannot name a method the person does not hold", func(t *testing.T) {
		if _, err := store.ReplaceNotificationRules(ctx, application.ReplaceNotificationRulesInput{
			UserID: userID, Urgency: domain.NotificationStandard, Now: now,
			Rungs: []application.NotificationRung{{ContactMethodID: domain.ID(uuid.NewString())}},
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("foreign method = %v, want not found", err)
		}
	})

	t.Run("replacing one urgency leaves the other alone", func(t *testing.T) {
		if _, err := store.ReplaceNotificationRules(ctx, application.ReplaceNotificationRulesInput{
			UserID: userID, Urgency: domain.NotificationStandard, Now: now,
			Rungs: []application.NotificationRung{{Delay: 0}},
		}); err != nil {
			t.Fatalf("replace standard: %v", err)
		}
		byUser, err := store.ListRecipientRules(ctx, application.ListRecipientRulesInput{UserIDs: []domain.ID{userID}})
		if err != nil || len(byUser[userID]) != 3 {
			t.Fatalf("ListRecipientRules = %+v, %v", byUser, err)
		}
	})

	t.Run("removing a method removes the rungs that used it", func(t *testing.T) {
		if err := store.DeleteContactMethod(ctx, application.DeleteContactMethodInput{MethodID: slackLink.ID, UserID: userID}); err != nil {
			t.Fatalf("delete slack link: %v", err)
		}
		rules, err := store.ListNotificationRules(ctx, application.ListNotificationRulesInput{UserID: userID})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, rule := range rules {
			if rule.ContactMethodID == slackLink.ID {
				t.Fatalf("a rung still points at the removed method: %+v", rule)
			}
		}
		if len(rules) != 2 {
			t.Fatalf("rules after removal = %+v", rules)
		}
	})
}
