package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

const notificationRuleSelect = `SELECT id::text, user_id::text, urgency, position, delay_seconds,
    COALESCE(contact_method_id::text, ''), created_at FROM notification_rules `

func scanNotificationRule(row rowScanner) (domain.NotificationRule, error) {
	var rule domain.NotificationRule
	var id, userID, urgency, methodID string
	var delaySeconds int64
	if err := row.Scan(&id, &userID, &urgency, &rule.Position, &delaySeconds, &methodID, &rule.CreatedAt); err != nil {
		return domain.NotificationRule{}, err
	}
	rule.ID, rule.UserID, rule.ContactMethodID = domain.ID(id), domain.ID(userID), domain.ID(methodID)
	rule.Urgency, rule.Delay = domain.NotificationUrgency(urgency), time.Duration(delaySeconds)*time.Second
	return rule, nil
}

func (s *Store) ListNotificationRules(ctx context.Context, input application.ListNotificationRulesInput) ([]domain.NotificationRule, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, notificationRuleSelect+`WHERE user_id = $1 ORDER BY urgency, position`, input.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]domain.NotificationRule, 0)
	for rows.Next() {
		rule, err := scanNotificationRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// ReplaceNotificationRules swaps one urgency's ladder in a single
// transaction. A rung naming a method the person does not hold is
// refused, so a rule can never route through someone else's address.
func (s *Store) ReplaceNotificationRules(ctx context.Context, input application.ReplaceNotificationRulesInput) ([]domain.NotificationRule, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	for _, rung := range input.Rungs {
		if rung.ContactMethodID == "" {
			continue
		}
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM contact_methods
            WHERE id = $1 AND user_id = $2 AND verified_at IS NOT NULL)`, rung.ContactMethodID, input.UserID).Scan(&owned); err != nil {
			return nil, err
		}
		if !owned {
			return nil, domain.ErrNotFound
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM notification_rules WHERE user_id = $1 AND urgency = $2`,
		input.UserID, input.Urgency); err != nil {
		return nil, err
	}
	for position, rung := range input.Rungs {
		var methodID any
		if rung.ContactMethodID != "" {
			methodID = string(rung.ContactMethodID)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notification_rules
            (id, user_id, urgency, position, delay_seconds, contact_method_id, created_at)
            VALUES ($1, $2, $3, $4, $5, $6::uuid, $7)`,
			uuid.NewString(), input.UserID, input.Urgency, position, int64(rung.Delay/time.Second), methodID, input.Now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.ListNotificationRules(ctx, application.ListNotificationRulesInput{UserID: input.UserID})
}

func (s *Store) ListRecipientRules(ctx context.Context, input application.ListRecipientRulesInput) (map[domain.ID][]domain.NotificationRule, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	rules := make(map[domain.ID][]domain.NotificationRule, len(input.UserIDs))
	if len(input.UserIDs) == 0 {
		return rules, nil
	}
	rows, err := s.pool.Query(ctx, notificationRuleSelect+`WHERE user_id = ANY($1::uuid[]) ORDER BY user_id, urgency, position`, idStrings(input.UserIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		rule, err := scanNotificationRule(rows)
		if err != nil {
			return nil, err
		}
		rules[rule.UserID] = append(rules[rule.UserID], rule)
	}
	return rules, rows.Err()
}
