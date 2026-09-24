package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// NotificationRuleRepository owns each person's ladders. A ladder is
// replaced whole per urgency, which is how the page saves it and what
// keeps positions dense.
type NotificationRuleRepository interface {
	ListNotificationRules(ctx context.Context, input ListNotificationRulesInput) ([]domain.NotificationRule, error)
	ReplaceNotificationRules(ctx context.Context, input ReplaceNotificationRulesInput) ([]domain.NotificationRule, error)
	// ListRecipientRules loads the ladders of several people at once, for
	// building a request's delivery plan.
	ListRecipientRules(ctx context.Context, input ListRecipientRulesInput) (map[domain.ID][]domain.NotificationRule, error)
}

type ListNotificationRulesInput struct {
	UserID domain.ID
}

func (input ListNotificationRulesInput) Validate() error {
	return requiredID("user ID", input.UserID)
}

// NotificationRung is one rule as the page submits it: a delay and a
// method, where an empty method id means the account email.
type NotificationRung struct {
	Delay           time.Duration
	ContactMethodID domain.ID
}

type ReplaceNotificationRulesInput struct {
	UserID  domain.ID
	Urgency domain.NotificationUrgency
	Rungs   []NotificationRung
	Now     time.Time
}

func (input ReplaceNotificationRulesInput) Validate() error {
	if err := firstError([]error{
		requiredID("user ID", input.UserID),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if !input.Urgency.Valid() {
		return invalidField("notification urgency", "must be standard or interrupt")
	}
	if len(input.Rungs) == 0 {
		return invalidField("notification rules", "must keep at least one rung")
	}
	if len(input.Rungs) > 10 {
		return invalidField("notification rules", "must have at most 10 rungs")
	}
	for index, rung := range input.Rungs {
		if rung.Delay < 0 || rung.Delay > domain.MaximumNotificationDelay {
			return invalidField("notification rule delay", "must be between 0 and 24 hours")
		}
		if index > 0 && rung.Delay < input.Rungs[index-1].Delay {
			return invalidField("notification rules", "must be ordered by delay")
		}
	}
	return nil
}

type ListRecipientRulesInput struct {
	UserIDs []domain.ID
}

func (input ListRecipientRulesInput) Validate() error {
	for _, id := range input.UserIDs {
		if err := requiredID("user ID", id); err != nil {
			return err
		}
	}
	return nil
}

// RecipientMethod is one way a resolved recipient can be reached, keyed
// so a rule can name it. An empty ID is the account email.
type RecipientMethod struct {
	SlackInstallationID domain.ID
	Unavailable         bool
	ID                  domain.ID
	Kind                domain.ContactMethodKind
	Address             string
}

// NotificationStep is one moment in a request's delivery plan: after
// Delay, send to these recipients on exactly the methods due then. The
// recipients' Emails and SlackUserID carry only that step's targets, so
// channels need no knowledge of ladders.
type NotificationStep struct {
	Delay time.Duration
	// Outcome is how the recipient set was chosen, carried so channel
	// logs can say whether a step came from a match or the fallback.
	Outcome    domain.RoutingOutcome
	Recipients []NotificationRecipient
}
