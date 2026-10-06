package orchestration

import (
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// The edition kinds the planner tests register, shaped like the hosted
// edition's: two phone kinds a rung names one method of, and the
// installed app, told at once and at every device.
const (
	kindSMS      domain.ContactMethodKind = "sms"
	kindWhatsApp domain.ContactMethodKind = "whatsapp"
	kindPush     domain.ContactMethodKind = "push"
)

func init() {
	if err := domain.RegisterContactKinds(
		domain.ContactKind{Kind: kindSMS, Label: "Text message", Challengeable: true, Consent: true, DigestWindows: []time.Duration{time.Minute}},
		domain.ContactKind{Kind: kindWhatsApp, Label: "WhatsApp", Challengeable: true, Consent: true, DigestWindows: []time.Duration{time.Minute}},
		domain.ContactKind{Kind: kindPush, Label: "Phone app", Immediate: true, Multiple: true, DigestWindows: []time.Duration{time.Minute}},
	); err != nil && !strings.Contains(err.Error(), "already registered") {
		panic(err)
	}
}

func targetOf(recipient application.NotificationRecipient, kind domain.ContactMethodKind) (application.RecipientTarget, bool) {
	targets := recipient.TargetsOf(kind)
	if len(targets) != 1 {
		return application.RecipientTarget{}, false
	}
	return targets[0], true
}

func planRecipient(membership, user string, slack string, extraEmail string) application.NotificationRecipient {
	recipient := application.NotificationRecipient{
		MembershipID: domain.ID(membership), UserID: domain.ID(user), DisplayName: user,
		Methods: []application.RecipientMethod{{ID: "", Kind: domain.ContactEmail, Address: user + "@example.com"}},
	}
	if slack != "" {
		recipient.Methods = append(recipient.Methods, application.RecipientMethod{ID: domain.ID("slack-" + user), Kind: domain.ContactSlack, Address: slack})
	}
	if extraEmail != "" {
		recipient.Methods = append(recipient.Methods, application.RecipientMethod{ID: domain.ID("extra-" + user), Kind: domain.ContactEmail, Address: extraEmail})
	}
	return recipient
}

func withDiscord(recipient application.NotificationRecipient, discord string) application.NotificationRecipient {
	recipient.Methods = append(recipient.Methods, application.RecipientMethod{ID: domain.ID("discord-" + string(recipient.UserID)), Kind: domain.ContactDiscord, Address: discord})
	return recipient
}

func TestBuildNotificationPlanDefaults(t *testing.T) {
	linked := planRecipient("m1", "ada", "U0ADA", "")
	unlinked := planRecipient("m2", "bob", "", "")

	t.Run("standard goes to the account email at once for everyone", func(t *testing.T) {
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: domain.NotificationStandard, Recipients: []application.NotificationRecipient{linked, unlinked},
		}, nil, time.Hour)
		if len(plan) != 1 || plan[0].Delay != 0 || len(plan[0].Recipients) != 2 {
			t.Fatalf("plan = %+v", plan)
		}
		for _, recipient := range plan[0].Recipients {
			if recipient.SlackUserID != "" || len(recipient.Emails) != 1 {
				t.Fatalf("standard default reached beyond the account email: %+v", recipient)
			}
		}
	})

	t.Run("interrupt pages Slack first and adds email later, but never delays the unlinked", func(t *testing.T) {
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: domain.NotificationInterrupt, Recipients: []application.NotificationRecipient{linked, unlinked},
		}, nil, time.Hour)
		if len(plan) != 2 || plan[0].Delay != 0 || plan[1].Delay != DefaultInterruptEmailDelay {
			t.Fatalf("plan = %+v", plan)
		}
		first := map[domain.ID]application.NotificationRecipient{}
		for _, recipient := range plan[0].Recipients {
			first[recipient.MembershipID] = recipient
		}
		if first["m1"].SlackUserID != "U0ADA" || len(first["m1"].Emails) != 0 {
			t.Fatalf("linked person's first step = %+v, want Slack only", first["m1"])
		}
		if first["m2"].SlackUserID != "" || len(first["m2"].Emails) != 1 {
			t.Fatalf("unlinked person's first step = %+v, want email at once", first["m2"])
		}
		if len(plan[1].Recipients) != 1 || plan[1].Recipients[0].MembershipID != "m1" || len(plan[1].Recipients[0].Emails) != 1 {
			t.Fatalf("second step = %+v, want the linked person's email only", plan[1].Recipients)
		}
	})

	t.Run("interrupt pages Discord first for someone linked only there", func(t *testing.T) {
		discordOnly := withDiscord(planRecipient("m3", "cid", "", ""), "100200300")
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: domain.NotificationInterrupt, Recipients: []application.NotificationRecipient{discordOnly},
		}, nil, time.Hour)
		if len(plan) != 2 || plan[0].Delay != 0 || plan[1].Delay != DefaultInterruptEmailDelay {
			t.Fatalf("plan = %+v", plan)
		}
		first := plan[0].Recipients[0]
		if first.DiscordUserID != "100200300" || first.SlackUserID != "" || len(first.Emails) != 0 {
			t.Fatalf("first step = %+v, want Discord only", first)
		}
		if second := plan[1].Recipients[0]; second.DiscordUserID != "" || len(second.Emails) != 1 {
			t.Fatalf("second step = %+v, want email only", second)
		}
	})

	t.Run("Slack wins over Discord when both are linked", func(t *testing.T) {
		both := withDiscord(planRecipient("m4", "dee", "U0DEE", ""), "400500600")
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: domain.NotificationInterrupt, Recipients: []application.NotificationRecipient{both},
		}, nil, time.Hour)
		if len(plan) != 2 {
			t.Fatalf("plan = %+v", plan)
		}
		if first := plan[0].Recipients[0]; first.SlackUserID != "U0DEE" || first.DiscordUserID != "" {
			t.Fatalf("first step = %+v, want Slack only", first)
		}
	})
}

func TestBuildNotificationPlanOwnRules(t *testing.T) {
	ada := withDiscord(planRecipient("m1", "ada", "U0ADA", "ada@home.example"), "700800900")
	rules := map[domain.ID][]domain.NotificationRule{
		"ada": {
			{Urgency: domain.NotificationInterrupt, Position: 0, Delay: 0, ContactMethodID: "extra-ada"},
			{Urgency: domain.NotificationInterrupt, Position: 1, Delay: 2 * time.Minute, ContactMethodID: "slack-ada"},
			{Urgency: domain.NotificationInterrupt, Position: 2, Delay: 2 * time.Minute},
			{Urgency: domain.NotificationInterrupt, Position: 3, Delay: 50 * time.Minute, ContactMethodID: "gone"},
			{Urgency: domain.NotificationStandard, Position: 0, Delay: 45 * time.Minute, ContactMethodID: "discord-ada"},
		},
	}

	t.Run("rungs group by delay and merge methods for the same person", func(t *testing.T) {
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: domain.NotificationInterrupt, Recipients: []application.NotificationRecipient{ada},
		}, rules, time.Hour)
		if len(plan) != 2 {
			t.Fatalf("plan = %+v, want two steps (the rung naming a lost method is skipped)", plan)
		}
		if plan[0].Delay != 0 || plan[0].Recipients[0].Emails[0] != "ada@home.example" || plan[0].Recipients[0].SlackUserID != "" || plan[0].Recipients[0].DiscordUserID != "" {
			t.Fatalf("first step = %+v", plan[0])
		}
		second := plan[1].Recipients[0]
		if plan[1].Delay != 2*time.Minute || second.SlackUserID != "U0ADA" || second.DiscordUserID != "" || len(second.Emails) != 1 || second.Emails[0] != "ada@example.com" {
			t.Fatalf("second step = %+v, want Slack and the account email together", second)
		}
	})

	t.Run("the other urgency's ladder is not consulted, and a Discord rung reaches Discord only", func(t *testing.T) {
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: domain.NotificationStandard, Recipients: []application.NotificationRecipient{ada},
		}, rules, time.Hour)
		if len(plan) != 1 || plan[0].Delay != 45*time.Minute {
			t.Fatalf("standard plan = %+v", plan)
		}
		if only := plan[0].Recipients[0]; only.DiscordUserID != "700800900" || only.SlackUserID != "" || len(only.Emails) != 0 {
			t.Fatalf("standard step = %+v, want Discord only", only)
		}
	})

	t.Run("rungs at or past the deadline are dropped", func(t *testing.T) {
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: domain.NotificationStandard, Recipients: []application.NotificationRecipient{ada},
		}, rules, 30*time.Minute)
		if len(plan) != 0 {
			t.Fatalf("plan = %+v, want nothing before a 30 minute deadline", plan)
		}
	})

	t.Run("an unknown urgency falls back to standard", func(t *testing.T) {
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: "", Recipients: []application.NotificationRecipient{ada},
		}, rules, time.Hour)
		if len(plan) != 1 || plan[0].Delay != 45*time.Minute {
			t.Fatalf("plan = %+v", plan)
		}
	})
}

func withPhone(recipient application.NotificationRecipient, kind domain.ContactMethodKind, number string) application.NotificationRecipient {
	recipient.Methods = append(recipient.Methods, application.RecipientMethod{ID: domain.ID(string(kind) + "-" + string(recipient.UserID)), Kind: kind, Address: number})
	return recipient
}

// A rung naming an edition kind's method lands the address and the
// method behind it on that step alone, under its kind, never among the
// emails.
func TestBuildNotificationPlanEditionKindRungs(t *testing.T) {
	ada := withPhone(withPhone(planRecipient("m1", "ada", "", ""), kindSMS, "+447700900123"), kindWhatsApp, "+447700900123")
	rules := map[domain.ID][]domain.NotificationRule{
		"ada": {
			{UserID: "ada", Urgency: domain.NotificationInterrupt, Position: 0, Delay: 0, ContactMethodID: "whatsapp-ada"},
			{UserID: "ada", Urgency: domain.NotificationInterrupt, Position: 1, Delay: 5 * time.Minute, ContactMethodID: "sms-ada"},
			{UserID: "ada", Urgency: domain.NotificationInterrupt, Position: 2, Delay: 10 * time.Minute},
		},
	}
	plan := BuildNotificationPlan(application.ApprovalRecipients{
		Urgency: domain.NotificationInterrupt, Recipients: []application.NotificationRecipient{ada},
	}, rules, time.Hour)
	if len(plan) != 3 {
		t.Fatalf("plan = %+v", plan)
	}
	first := plan[0].Recipients[0]
	if target, ok := targetOf(first, kindWhatsApp); !ok || target.Address != "+447700900123" || target.MethodID != "whatsapp-ada" || len(first.TargetsOf(kindSMS)) != 0 || len(first.Emails) != 0 {
		t.Fatalf("first step = %+v", first)
	}
	second := plan[1].Recipients[0]
	if target, ok := targetOf(second, kindSMS); !ok || target.Address != "+447700900123" || target.MethodID != "sms-ada" || len(second.TargetsOf(kindWhatsApp)) != 0 || len(second.Emails) != 0 {
		t.Fatalf("second step = %+v", second)
	}
	third := plan[2].Recipients[0]
	if len(third.Emails) != 1 || third.Emails[0] != "ada@example.com" || len(third.Targets) != 0 {
		t.Fatalf("third step = %+v", third)
	}
}

// A method of a kind this build does not know delivers nowhere: it must
// not fall through into the emails.
func TestBuildNotificationPlanSkipsUnknownKinds(t *testing.T) {
	bob := planRecipient("m2", "bob", "", "")
	bob.Methods = append(bob.Methods, application.RecipientMethod{ID: "fax-bob", Kind: "fax", Address: "555-0100"})
	rules := map[domain.ID][]domain.NotificationRule{
		"bob": {{UserID: "bob", Urgency: domain.NotificationStandard, Position: 0, Delay: 0, ContactMethodID: "fax-bob"}},
	}
	plan := BuildNotificationPlan(application.ApprovalRecipients{
		Urgency: domain.NotificationStandard, Recipients: []application.NotificationRecipient{bob},
	}, rules, time.Hour)
	if len(plan) != 1 || len(plan[0].Recipients) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if recipient := plan[0].Recipients[0]; len(recipient.Emails) != 0 || len(recipient.Targets) != 0 || recipient.SlackUserID != "" {
		t.Fatalf("an unknown kind was delivered somewhere: %+v", recipient)
	}
}

// Verified numbers join a ladder only when the person puts them there:
// the defaults stay chat and email.
func TestBuildNotificationPlanDefaultsIgnorePhoneMethods(t *testing.T) {
	cy := withPhone(planRecipient("m3", "cy", "", ""), kindSMS, "+447700900123")
	for _, urgency := range []domain.NotificationUrgency{domain.NotificationStandard, domain.NotificationInterrupt} {
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: urgency, Recipients: []application.NotificationRecipient{cy},
		}, nil, time.Hour)
		if len(plan) != 1 || plan[0].Delay != 0 || len(plan[0].Recipients) != 1 {
			t.Fatalf("%s plan = %+v", urgency, plan)
		}
		if recipient := plan[0].Recipients[0]; len(recipient.Targets) != 0 || len(recipient.Emails) != 1 {
			t.Fatalf("%s default reached the phone: %+v", urgency, recipient)
		}
	}
}

func TestBuildNotificationPlanTellsInstalledAppsAtOnce(t *testing.T) {
	withPhone := planRecipient("m1", "ada", "U0ADA", "")
	withPhone.Methods = append(withPhone.Methods, application.RecipientMethod{ID: "phone-ada", Kind: kindPush, Address: "device-1"}, application.RecipientMethod{ID: "tablet-ada", Kind: kindPush, Address: "device-2"})

	t.Run("standard pushes alongside the account email", func(t *testing.T) {
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: domain.NotificationStandard, Recipients: []application.NotificationRecipient{withPhone},
		}, nil, time.Hour)
		if len(plan) != 1 || plan[0].Delay != 0 || len(plan[0].Recipients) != 1 {
			t.Fatalf("plan = %+v", plan)
		}
		first := plan[0].Recipients[0]
		if pushes := first.TargetsOf(kindPush); len(pushes) != 2 || pushes[0].MethodID != "phone-ada" || pushes[1].MethodID != "tablet-ada" || len(first.Emails) != 1 || first.SlackUserID != "" {
			t.Fatalf("first step = %+v, want every device and the email together", first)
		}
	})

	t.Run("interrupt pushes and pages chat at once, email later", func(t *testing.T) {
		plan := BuildNotificationPlan(application.ApprovalRecipients{
			Urgency: domain.NotificationInterrupt, Recipients: []application.NotificationRecipient{withPhone},
		}, nil, time.Hour)
		if len(plan) != 2 || plan[0].Delay != 0 || plan[1].Delay != DefaultInterruptEmailDelay {
			t.Fatalf("plan = %+v", plan)
		}
		first := plan[0].Recipients[0]
		if len(first.TargetsOf(kindPush)) != 2 || first.SlackUserID != "U0ADA" || len(first.Emails) != 0 {
			t.Fatalf("first step = %+v, want push and Slack, no email", first)
		}
		if len(plan[1].Recipients[0].TargetsOf(kindPush)) != 0 || len(plan[1].Recipients[0].Emails) != 1 {
			t.Fatalf("second step = %+v, want email only", plan[1].Recipients[0])
		}
	})
}
