package orchestration

import (
	"sort"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// DefaultInterruptEmailDelay is how long the default interrupt ladder
// waits before adding email behind a chat message.
const DefaultInterruptEmailDelay = 5 * time.Minute

// defaultRungs is a person's ladder when they have not shaped one. The
// split follows incident.io: standard goes quietly to the account email;
// interrupt goes to the person's chat method at once (Slack before
// Discord, in domain.ChatContactKinds order) and adds email after a
// while. Someone with no chat link gets email at once for interrupts too,
// so the quiet-first rule can never delay their first contact.
func defaultRungs(urgency domain.NotificationUrgency, methods []application.RecipientMethod) []application.NotificationRung {
	// A kind the edition marked immediate (the installed phone app) is
	// always told at once: somebody who put the app on their phone did so
	// to hear about requests there.
	var rungs []application.NotificationRung
	for _, method := range methods {
		if method.Kind.Immediate() && !method.Unavailable {
			rungs = append(rungs, application.NotificationRung{Delay: 0, ContactMethodID: method.ID})
		}
	}
	if urgency == domain.NotificationInterrupt {
		for _, kind := range domain.ChatContactKinds {
			for _, method := range methods {
				if method.Kind == kind && !method.Unavailable {
					return append(rungs,
						application.NotificationRung{Delay: 0, ContactMethodID: method.ID},
						application.NotificationRung{Delay: DefaultInterruptEmailDelay},
					)
				}
			}
		}
	}
	return append(rungs, application.NotificationRung{Delay: 0})
}

// BuildNotificationPlan turns resolved recipients and their ladders into
// the moments a request will notify at. Each step's recipients carry only
// the targets due at that step; a rung naming a method the person no
// longer holds is skipped; steps past the deadline are dropped, because
// the request will already have expired.
func BuildNotificationPlan(recipients application.ApprovalRecipients, rules map[domain.ID][]domain.NotificationRule, remaining time.Duration) []application.NotificationStep {
	urgency := recipients.Urgency
	if !urgency.Valid() {
		urgency = domain.NotificationStandard
	}
	type target struct {
		recipient application.NotificationRecipient
		method    application.RecipientMethod
	}
	byDelay := make(map[time.Duration][]target)
	for _, recipient := range recipients.Recipients {
		methodsByID := make(map[domain.ID]application.RecipientMethod, len(recipient.Methods))
		for _, method := range recipient.Methods {
			methodsByID[method.ID] = method
		}
		rungs := ownRungs(rules[recipient.UserID], urgency)
		if len(rungs) == 0 {
			rungs = defaultRungs(urgency, recipient.Methods)
		}
		slackDue, usableSlack, otherDue := false, false, false
		var slackMethodID domain.ID
		for _, rung := range rungs {
			method, held := methodsByID[rung.ContactMethodID]
			if !held || (remaining > 0 && rung.Delay >= remaining) {
				continue
			}
			if method.Kind == domain.ContactSlack {
				slackDue = true
				slackMethodID = method.ID
				usableSlack = usableSlack || !method.Unavailable
			} else if !method.Unavailable {
				otherDue = true
			}
		}
		if slackDue && !otherDue {
			if email, ok := methodsByID[""]; ok {
				recipient.SlackFallbackEmail = email.Address
			}
			if !usableSlack && recipient.SlackFallbackEmail != "" {
				recipient.SlackFallback = true
				rungs = []application.NotificationRung{{ContactMethodID: slackMethodID}}
			}
		}
		for _, rung := range rungs {
			method, held := methodsByID[rung.ContactMethodID]
			if !held || (method.Unavailable && !recipient.SlackFallback) {
				continue
			}
			if remaining > 0 && rung.Delay >= remaining {
				continue
			}
			byDelay[rung.Delay] = append(byDelay[rung.Delay], target{recipient: recipient, method: method})
		}
	}
	delays := make([]time.Duration, 0, len(byDelay))
	for delay := range byDelay {
		delays = append(delays, delay)
	}
	sort.Slice(delays, func(i, j int) bool { return delays[i] < delays[j] })
	steps := make([]application.NotificationStep, 0, len(delays))
	for _, delay := range delays {
		merged := make(map[domain.ID]*application.NotificationRecipient)
		order := make([]domain.ID, 0)
		for _, item := range byDelay[delay] {
			entry, seen := merged[item.recipient.MembershipID]
			if !seen {
				copied := item.recipient
				copied.Emails, copied.SlackUserID, copied.DiscordUserID = nil, "", ""
				copied.Targets = nil
				entry = &copied
				merged[item.recipient.MembershipID] = entry
				order = append(order, item.recipient.MembershipID)
			}
			// Two methods of one single-target kind on the same rung
			// leave the last one standing, as Slack always has; a kind
			// this build does not know delivers nowhere rather than
			// landing in somebody's mailbox as an address.
			switch item.method.Kind {
			case domain.ContactEmail:
				entry.Emails = append(entry.Emails, item.method.Address)
			case domain.ContactSlack:
				if !item.recipient.SlackFallback {
					entry.SlackUserID = item.method.Address
				}
				entry.SlackInstallationID = item.method.SlackInstallationID
			case domain.ContactDiscord:
				entry.DiscordUserID = item.method.Address
			default:
				if _, registered := domain.LookupContactKind(item.method.Kind); !registered {
					continue
				}
				if entry.Targets == nil {
					entry.Targets = map[domain.ContactMethodKind][]application.RecipientTarget{}
				}
				target := application.RecipientTarget{MethodID: item.method.ID, Address: item.method.Address}
				if item.method.Kind.Multiple() {
					entry.Targets[item.method.Kind] = append(entry.Targets[item.method.Kind], target)
				} else {
					entry.Targets[item.method.Kind] = []application.RecipientTarget{target}
				}
			}
		}
		step := application.NotificationStep{Delay: delay, Outcome: recipients.Outcome}
		for _, id := range order {
			step.Recipients = append(step.Recipients, *merged[id])
		}
		steps = append(steps, step)
	}
	return steps
}

func ownRungs(rules []domain.NotificationRule, urgency domain.NotificationUrgency) []application.NotificationRung {
	var rungs []application.NotificationRung
	for _, rule := range rules {
		if rule.Urgency == urgency {
			rungs = append(rungs, application.NotificationRung{Delay: rule.Delay, ContactMethodID: rule.ContactMethodID})
		}
	}
	return rungs
}
