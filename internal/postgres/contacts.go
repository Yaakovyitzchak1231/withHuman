package postgres

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

const contactMethodSelect = `SELECT id::text, kind, value, COALESCE(organization_id::text, ''), label,
    verified_at, created_at, updated_at, COALESCE(slack_installation_id::text, ''),
    kind = 'slack' AND NOT EXISTS (SELECT 1 FROM slack_connections sc WHERE sc.organization_id = contact_methods.organization_id
        AND sc.status = 'connected' AND sc.installation_id = contact_methods.slack_installation_id)
    FROM contact_methods `

// ListContactMethods returns the person's own methods, oldest first: the
// person-wide ones always, and the organization-scoped ones for the given
// organization. The account's own primary email is not stored here;
// callers surface it alongside.
func (s *Store) ListContactMethods(ctx context.Context, input application.ListContactMethodsInput) ([]domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, contactMethodSelect+`WHERE user_id = $1
        AND (organization_id IS NULL OR organization_id = NULLIF($2, '')::uuid)
        ORDER BY created_at`, input.UserID, string(input.OrganizationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	methods := make([]domain.ContactMethod, 0)
	for rows.Next() {
		method, err := scanContactMethod(rows, input.UserID)
		if err != nil {
			return nil, err
		}
		methods = append(methods, method)
	}
	return methods, rows.Err()
}

func scanContactMethod(row rowScanner, userID domain.ID) (domain.ContactMethod, error) {
	var method domain.ContactMethod
	var id, kind, organizationID string
	var verifiedAt *time.Time
	if err := row.Scan(&id, &kind, &method.Value, &organizationID, &method.Label, &verifiedAt, &method.CreatedAt, &method.UpdatedAt, &method.SlackInstallationID, &method.Unavailable); err != nil {
		return domain.ContactMethod{}, err
	}
	method.ID, method.UserID, method.OrganizationID = domain.ID(id), userID, domain.ID(organizationID)
	method.Kind, method.VerifiedAt = domain.ContactMethodKind(kind), verifiedAt
	return method, nil
}

// CreateContactMethod records an unverified address with its first
// challenge. Adding the same address twice is a conflict rather than a
// second row, so a resend is always an explicit action on the existing
// method.
func (s *Store) CreateContactMethod(ctx context.Context, input application.CreateContactMethodInput) (domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return domain.ContactMethod{}, err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO contact_methods
        (id, user_id, kind, value, verification_code_hash, verification_expires_at, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
		input.MethodID, input.UserID, input.Kind, input.Value, input.CodeHash, input.ExpiresAt, input.Now)
	if isUniqueViolation(err) {
		return domain.ContactMethod{}, fmt.Errorf("that address is already on your account: %w", domain.ErrConflict)
	}
	if err != nil {
		return domain.ContactMethod{}, err
	}
	return s.getContactMethod(ctx, input.UserID, input.MethodID)
}

// CreateVouchedContactMethod records or refreshes a person-wide method
// vouched for by something other than a code, verified on insert. The
// same value again keeps its id and verification and takes the new label.
func (s *Store) CreateVouchedContactMethod(ctx context.Context, input application.CreateVouchedContactMethodInput) (domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return domain.ContactMethod{}, err
	}
	var id string
	if err := s.pool.QueryRow(ctx, `INSERT INTO contact_methods
        (id, user_id, kind, value, label, verified_at, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, $6, $6, $6)
        ON CONFLICT (user_id, kind, lower(value)) DO UPDATE SET
            label = EXCLUDED.label, verified_at = COALESCE(contact_methods.verified_at, EXCLUDED.verified_at),
            updated_at = EXCLUDED.updated_at
        RETURNING id::text`,
		input.MethodID, input.UserID, input.Kind, input.Value, input.Label, input.Now).Scan(&id); err != nil {
		return domain.ContactMethod{}, err
	}
	return s.getContactMethod(ctx, input.UserID, domain.ID(id))
}

// SetContactMethodChallenge rotates the live challenge, invalidating the
// previous code and resetting the attempt budget. A method that is already
// verified has nothing to prove.
func (s *Store) SetContactMethodChallenge(ctx context.Context, input application.SetContactMethodChallengeInput) (domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return domain.ContactMethod{}, err
	}
	command, err := s.pool.Exec(ctx, `UPDATE contact_methods
        SET verification_code_hash = $3, verification_expires_at = $4,
            verification_attempts = 0, updated_at = $5
        WHERE user_id = $1 AND id = $2 AND verified_at IS NULL`,
		input.UserID, input.MethodID, input.CodeHash, input.ExpiresAt, input.Now)
	if err != nil {
		return domain.ContactMethod{}, err
	}
	if command.RowsAffected() == 0 {
		return domain.ContactMethod{}, s.contactMethodAbsenceReason(ctx, input.UserID, input.MethodID)
	}
	return s.getContactMethod(ctx, input.UserID, input.MethodID)
}

// VerifyContactMethod answers a live challenge. The attempt count, the
// comparison, and the verification all happen inside one transaction so a
// burst of parallel guesses cannot outrun the budget.
func (s *Store) VerifyContactMethod(ctx context.Context, input application.VerifyContactMethodInput) (domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return domain.ContactMethod{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.ContactMethod{}, err
	}
	defer tx.Rollback(ctx)

	var codeHash []byte
	var expiresAt, verifiedAt *time.Time
	var attempts int64
	err = tx.QueryRow(ctx, `SELECT verification_code_hash, verification_expires_at, verified_at, verification_attempts
        FROM contact_methods WHERE user_id = $1 AND id = $2 FOR UPDATE`,
		input.UserID, input.MethodID).Scan(&codeHash, &expiresAt, &verifiedAt, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ContactMethod{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ContactMethod{}, err
	}
	if verifiedAt != nil {
		return domain.ContactMethod{}, fmt.Errorf("that address is already verified: %w", domain.ErrConflict)
	}
	if len(codeHash) == 0 || expiresAt == nil || !input.Now.Before(*expiresAt) {
		return domain.ContactMethod{}, fmt.Errorf("that code has expired, send a new one: %w", domain.ErrInvalid)
	}
	if attempts >= input.MaxAttempts {
		return domain.ContactMethod{}, fmt.Errorf("too many attempts, send a new code: %w", domain.ErrConflict)
	}
	// A wrong answer costs an attempt and keeps the challenge alive, so a
	// mistyped digit does not force a new email.
	if subtle.ConstantTimeCompare(codeHash, input.CodeHash) != 1 {
		if _, err := tx.Exec(ctx, `UPDATE contact_methods
            SET verification_attempts = verification_attempts + 1, updated_at = $3
            WHERE user_id = $1 AND id = $2`, input.UserID, input.MethodID, input.Now); err != nil {
			return domain.ContactMethod{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.ContactMethod{}, err
		}
		return domain.ContactMethod{}, fmt.Errorf("that code is not right: %w", domain.ErrInvalid)
	}
	if _, err := tx.Exec(ctx, `UPDATE contact_methods
        SET verified_at = $3, verification_code_hash = NULL, verification_expires_at = NULL,
            verification_attempts = 0, updated_at = $3
        WHERE user_id = $1 AND id = $2`, input.UserID, input.MethodID, input.Now); err != nil {
		return domain.ContactMethod{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ContactMethod{}, err
	}
	return s.getContactMethod(ctx, input.UserID, input.MethodID)
}

// VerifyContactMethodByValue answers a challenge from the address side.
// Several people may hold the same number unconfirmed at once, so every
// live challenge for the value is compared and the one whose code matches
// is verified; the others spend an attempt, exactly as a wrong answer
// through the owner's own form would. Absence of any live challenge is
// domain.ErrNotFound; a wrong or expired code is domain.ErrInvalid.
func (s *Store) VerifyContactMethodByValue(ctx context.Context, input application.VerifyContactMethodByValueInput) (domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return domain.ContactMethod{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.ContactMethod{}, err
	}
	defer tx.Rollback(ctx)

	type candidate struct {
		id, userID string
		hash       []byte
		expiresAt  *time.Time
		attempts   int64
	}
	rows, err := tx.Query(ctx, `SELECT id::text, user_id::text, verification_code_hash, verification_expires_at, verification_attempts
        FROM contact_methods WHERE kind = $1 AND lower(value) = lower($2) AND verified_at IS NULL FOR UPDATE`,
		string(input.Kind), input.Value)
	if err != nil {
		return domain.ContactMethod{}, err
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.userID, &c.hash, &c.expiresAt, &c.attempts); err != nil {
			rows.Close()
			return domain.ContactMethod{}, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.ContactMethod{}, err
	}
	if len(candidates) == 0 {
		return domain.ContactMethod{}, domain.ErrNotFound
	}
	var matched *candidate
	live := make([]string, 0, len(candidates))
	for index := range candidates {
		c := &candidates[index]
		if len(c.hash) == 0 || c.expiresAt == nil || !input.Now.Before(*c.expiresAt) || c.attempts >= input.MaxAttempts {
			continue
		}
		live = append(live, c.id)
		if matched == nil && subtle.ConstantTimeCompare(c.hash, input.CodeHash) == 1 {
			matched = c
		}
	}
	if matched == nil {
		if len(live) == 0 {
			return domain.ContactMethod{}, fmt.Errorf("that code has expired, get a new one: %w", domain.ErrInvalid)
		}
		if _, err := tx.Exec(ctx, `UPDATE contact_methods
            SET verification_attempts = verification_attempts + 1, updated_at = $2
            WHERE id = ANY($1::uuid[])`, live, input.Now); err != nil {
			return domain.ContactMethod{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.ContactMethod{}, err
		}
		return domain.ContactMethod{}, fmt.Errorf("that code is not right: %w", domain.ErrInvalid)
	}
	if _, err := tx.Exec(ctx, `UPDATE contact_methods
        SET verified_at = $2, verification_code_hash = NULL, verification_expires_at = NULL,
            verification_attempts = 0, updated_at = $2
        WHERE id = $1`, matched.id, input.Now); err != nil {
		return domain.ContactMethod{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ContactMethod{}, err
	}
	return s.getContactMethod(ctx, domain.ID(matched.userID), domain.ID(matched.id))
}

// LinkOrganizationContactMethod records one of the person's chat
// identities in one organization, verified at link time because the
// provider vouched for the account. Re-linking refreshes the id and label
// in place. The conflict target is the partial index on every
// organization-scoped kind, so Slack and Discord share this statement.
func (s *Store) LinkOrganizationContactMethod(ctx context.Context, input application.LinkOrganizationContactMethodInput) (domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return domain.ContactMethod{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ContactMethod{}, err
	}
	defer tx.Rollback(ctx)
	if input.Kind == domain.ContactSlack {
		var generation domain.ID
		if err := tx.QueryRow(ctx, `SELECT installation_id::text FROM slack_connections WHERE organization_id = $1 AND status = 'connected' FOR SHARE`, input.OrganizationID).Scan(&generation); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ContactMethod{}, domain.ErrConflict
			}
			return domain.ContactMethod{}, err
		}
		if generation != input.SlackInstallationID {
			return domain.ContactMethod{}, domain.ErrConflict
		}
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO contact_methods
        (id, user_id, kind, value, organization_id, label, verified_at, created_at, updated_at, slack_installation_id)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $7, NULLIF($8, '')::uuid)
        ON CONFLICT (user_id, organization_id, kind) WHERE organization_id IS NOT NULL DO UPDATE SET
            slack_installation_id = EXCLUDED.slack_installation_id, value = EXCLUDED.value, label = EXCLUDED.label, verified_at = EXCLUDED.verified_at, updated_at = EXCLUDED.updated_at
        RETURNING id::text`,
		input.MethodID, input.UserID, input.Kind, input.ExternalID, input.OrganizationID, input.Label, input.Now, input.SlackInstallationID).Scan(&id)
	if isUniqueViolation(err) {
		return domain.ContactMethod{}, fmt.Errorf("that %s account is already linked to another member: %w", providerName(input.Kind), domain.ErrConflict)
	}
	if err != nil {
		return domain.ContactMethod{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ContactMethod{}, err
	}
	return s.getContactMethod(ctx, input.UserID, domain.ID(id))
}

// ListMemberAddresses gathers, for every active member, the addresses a
// workspace could know them by: the account email plus confirmed contact
// emails. Unconfirmed addresses are excluded so a typo cannot link a
// stranger's Slack account to a member.
func (s *Store) ListMemberAddresses(ctx context.Context, input application.ListMemberAddressesInput) ([]application.MemberAddresses, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT u.id::text, m.id::text, u.display_name, ARRAY(
            SELECT DISTINCT address FROM (
                SELECT u.primary_email AS address
                UNION
                SELECT cm.value FROM contact_methods cm
                WHERE cm.user_id = u.id AND cm.kind = 'email' AND cm.verified_at IS NOT NULL
            ) addresses WHERE address <> '')
        FROM memberships m JOIN users u ON u.id = m.user_id
        WHERE m.organization_id = $1 AND m.status = 'active' AND u.status = 'active'
          AND ($2::text = '' OR u.id::text = $2::text)
        ORDER BY u.display_name`, input.OrganizationID, string(input.UserID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := make([]application.MemberAddresses, 0)
	for rows.Next() {
		var userID, membershipID string
		var member application.MemberAddresses
		if err := rows.Scan(&userID, &membershipID, &member.DisplayName, &member.Emails); err != nil {
			return nil, err
		}
		member.UserID, member.MembershipID = domain.ID(userID), domain.ID(membershipID)
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *Store) DeleteContactMethod(ctx context.Context, input application.DeleteContactMethodInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM contact_methods WHERE user_id = $1 AND id = $2`,
		input.UserID, input.MethodID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) getContactMethod(ctx context.Context, userID, methodID domain.ID) (domain.ContactMethod, error) {
	method, err := scanContactMethod(
		s.pool.QueryRow(ctx, contactMethodSelect+`WHERE user_id = $1 AND id = $2`, userID, methodID), userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ContactMethod{}, domain.ErrNotFound
	}
	return method, err
}

// contactMethodAbsenceReason distinguishes a method that is not there from
// one that is already verified, so the caller can say which.
func (s *Store) contactMethodAbsenceReason(ctx context.Context, userID, methodID domain.ID) error {
	method, err := s.getContactMethod(ctx, userID, methodID)
	if err != nil {
		return err
	}
	if method.Verified() {
		return fmt.Errorf("that address is already verified: %w", domain.ErrConflict)
	}
	return domain.ErrNotFound
}

// providerName is how a chat kind is named in a message to a person.
func providerName(kind domain.ContactMethodKind) string {
	switch kind {
	case domain.ContactSlack:
		return "Slack"
	case domain.ContactDiscord:
		return "Discord"
	default:
		return string(kind)
	}
}
