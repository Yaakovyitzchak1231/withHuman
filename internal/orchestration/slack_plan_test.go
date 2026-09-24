package orchestration

import (
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

func TestSlackUnavailableFallbackPreservesOtherScheduledChannels(t *testing.T) {
	for _, test := range []struct {
		name               string
		unavailable, email bool
		emailDelay         time.Duration
		fallback           bool
	}{
		{"unavailable only", true, false, 0, true},
		{"available only", false, false, 0, false},
		{"later email still scheduled", true, true, time.Minute, false},
		{"email past deadline", true, true, time.Hour, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recipients := application.ApprovalRecipients{Urgency: domain.NotificationInterrupt, Recipients: []application.NotificationRecipient{{UserID: "user", MembershipID: "member", Methods: []application.RecipientMethod{
				{ID: "", Kind: domain.ContactEmail, Address: "owner@example.com"},
				{ID: "slack", Kind: domain.ContactSlack, Address: "U1", SlackInstallationID: "generation", Unavailable: test.unavailable},
			}}}}
			rules := []domain.NotificationRule{{UserID: "user", Urgency: domain.NotificationInterrupt, ContactMethodID: "slack", Delay: time.Minute}}
			if test.email {
				rules = append(rules, domain.NotificationRule{UserID: "user", Urgency: domain.NotificationInterrupt, Delay: test.emailDelay})
			}
			plan := BuildNotificationPlan(recipients, map[domain.ID][]domain.NotificationRule{"user": rules}, 10*time.Minute)
			if len(plan) != 1 || len(plan[0].Recipients) != 1 {
				t.Fatalf("plan=%+v", plan)
			}
			recipient := plan[0].Recipients[0]
			if recipient.SlackFallback != test.fallback {
				t.Fatalf("recipient=%+v", recipient)
			}
			if test.fallback && (plan[0].Delay != 0 || recipient.SlackFallbackEmail != "owner@example.com" || len(recipient.Emails) != 0) {
				t.Fatalf("fallback=%+v", plan)
			}
			if !test.unavailable && (recipient.SlackUserID != "U1" || recipient.SlackInstallationID != "generation" || recipient.SlackFallbackEmail == "") {
				t.Fatalf("live Slack=%+v", recipient)
			}
			if test.email && !test.fallback && len(recipient.Emails) != 1 {
				t.Fatalf("remaining email=%+v", recipient)
			}
		})
	}
}
