package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

func (s *Store) CreateDecisionOperation(ctx context.Context, input application.CreateDecisionOperationInput) (domain.DecisionOperation, error) {
	if err := input.Validate(); err != nil {
		return domain.DecisionOperation{}, err
	}
	operation, idempotencyKey, intentHash := input.Operation, input.IdempotencyKey, input.IntentHash
	_, err := s.pool.Exec(ctx, `INSERT INTO decision_operations
        (id, organization_id, request_id, idempotency_key, intent_hash, status, created_at)
        VALUES ($1,$2,$3,$4,$5,'pending',$6) ON CONFLICT (organization_id, idempotency_key) DO NOTHING`,
		operation.ID, operation.OrganizationID, operation.RequestID, idempotencyKey, intentHash, operation.CreatedAt)
	if err != nil {
		return domain.DecisionOperation{}, err
	}
	var existingID string
	err = s.pool.QueryRow(ctx, `SELECT id::text FROM decision_operations WHERE organization_id=$1 AND idempotency_key=$2`,
		operation.OrganizationID, idempotencyKey).Scan(&existingID)
	if err != nil {
		return domain.DecisionOperation{}, err
	}
	existing, err := s.GetDecisionOperation(ctx, application.GetDecisionOperationInput{
		OrganizationID: operation.OrganizationID,
		OperationID:    domain.ID(existingID),
	})
	if err != nil {
		return domain.DecisionOperation{}, err
	}
	if existing.RequestID != operation.RequestID || existing.IntentHash != intentHash {
		return domain.DecisionOperation{}, fmt.Errorf("idempotency key is already bound to a different decision: %w", domain.ErrConflict)
	}
	return existing, nil
}

func (s *Store) GetDecisionOperation(ctx context.Context, input application.GetDecisionOperationInput) (domain.DecisionOperation, error) {
	if err := input.Validate(); err != nil {
		return domain.DecisionOperation{}, err
	}
	organizationID, operationID := input.OrganizationID, input.OperationID
	var operation domain.DecisionOperation
	var id, requestID, status string
	var completedAt sql.NullTime
	var decisionID, decisionSource, decisionStatus, note, channel, membershipID sql.NullString
	var reviewerName, reviewerEmail, authMethod, authAssurance, sessionID, credentialID, decisionKey sql.NullString
	var authenticatedAt, decidedAt, expiresAt sql.NullTime
	err := s.pool.QueryRow(ctx, `SELECT o.id::text,o.request_id::text,o.intent_hash,o.status,o.error_code,o.error_message,o.created_at,o.completed_at,
		d.id::text,d.source,d.status,d.note,d.channel,d.membership_id::text,d.reviewer_display_name,d.reviewer_email,
        d.auth_method,d.auth_assurance,d.authenticated_at,d.session_id::text,d.credential_id::text,d.idempotency_key,d.decided_at,d.expires_at
      FROM decision_operations o LEFT JOIN decisions d ON d.id=o.decision_id
	 WHERE o.organization_id=$1 AND o.id=$2`, organizationID, operationID).Scan(&id, &requestID, &operation.IntentHash, &status,
		&operation.ErrorCode, &operation.ErrorMessage, &operation.CreatedAt, &completedAt, &decisionID, &decisionSource,
		&decisionStatus, &note, &channel, &membershipID, &reviewerName, &reviewerEmail, &authMethod, &authAssurance,
		&authenticatedAt, &sessionID, &credentialID, &decisionKey, &decidedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DecisionOperation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.DecisionOperation{}, err
	}
	operation.ID, operation.OrganizationID, operation.RequestID, operation.Status = domain.ID(id), organizationID, domain.ID(requestID), domain.OperationStatus(status)
	if completedAt.Valid {
		operation.CompletedAt = &completedAt.Time
	}
	if decisionID.Valid {
		operation.Decision = &domain.Decision{ID: domain.ID(decisionID.String), RequestID: operation.RequestID,
			OrganizationID: organizationID, MembershipID: domain.ID(membershipID.String), Source: domain.DecisionSource(decisionSource.String), Status: domain.ApprovalStatus(decisionStatus.String),
			Note: note.String, Channel: channel.String, ReviewerDisplayName: reviewerName.String, ReviewerEmail: reviewerEmail.String,
			IdempotencyKey: decisionKey.String, DecidedAt: decidedAt.Time, Auth: domain.AuthContext{Method: authMethod.String,
				Assurance: authAssurance.String, AuthenticatedAt: authenticatedAt.Time, SessionID: domain.ID(sessionID.String), CredentialID: domain.ID(credentialID.String)}}
	}
	if operation.Decision != nil && expiresAt.Valid {
		operation.Decision.ExpiresAt = &expiresAt.Time
	}
	return operation, nil
}

type decisionRoutingState struct {
	hasTargets       bool
	userIDs          []domain.ID
	teamIDs          []domain.ID
	addedReviewerIDs []domain.ID
	reviewerTeamIDs  []domain.ID
}

// decisionRoutingTargets reads the request's pinned routing snapshot, the
// reviewers added to it afterwards, and the reviewer's team membership
// inside the decision transaction, so the routed-targets check stays
// atomic with the request's terminal state. A request without an
// escalated snapshot stays unscoped; an escalated one admits the targets
// its pinned path reaches under the pinned facts plus the added
// reviewers. An escalated snapshot that reaches nobody still scopes the
// request, so only a break-glass holder may decide it.
func decisionRoutingTargets(ctx context.Context, tx pgx.Tx, organizationID, requestID, reviewerMembershipID domain.ID) (decisionRoutingState, error) {
	pinned, escalated, err := readPinnedRouting(ctx, tx, organizationID, requestID)
	if err != nil || !escalated {
		return decisionRoutingState{}, err
	}
	userIDs, teamIDs, err := reachableDocumentTargets(pinned.document, pinned.facts)
	if err != nil {
		return decisionRoutingState{}, err
	}
	state := decisionRoutingState{hasTargets: true, userIDs: userIDs, teamIDs: teamIDs}
	if state.addedReviewerIDs, err = addedReviewerIDs(ctx, tx, organizationID, requestID); err != nil {
		return decisionRoutingState{}, err
	}
	if state.reviewerTeamIDs, err = membershipTeamIDs(ctx, tx, organizationID, reviewerMembershipID); err != nil {
		return decisionRoutingState{}, err
	}
	return state, nil
}

// membershipTeamIDs lists the teams one membership belongs to right now.
func membershipTeamIDs(ctx context.Context, q routingQuerier, organizationID, membershipID domain.ID) ([]domain.ID, error) {
	var teams []string
	if err := q.QueryRow(ctx, `SELECT COALESCE(array_agg(DISTINCT team_id::text), '{}')
        FROM active_membership_teams WHERE organization_id = $1 AND membership_id = $2`,
		organizationID, membershipID).Scan(&teams); err != nil {
		return nil, err
	}
	ids := make([]domain.ID, 0, len(teams))
	for _, teamID := range teams {
		ids = append(ids, domain.ID(teamID))
	}
	return ids, nil
}

func (s *Store) ApplyDecision(ctx context.Context, input application.ApplyDecisionInput) (domain.DecisionOperation, error) {
	if err := input.Validate(); err != nil {
		return domain.DecisionOperation{}, err
	}
	command, now := input.Command, input.Now
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.DecisionOperation{}, err
	}
	defer tx.Rollback(ctx)
	var operationStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM decision_operations WHERE organization_id=$1 AND id=$2 FOR UPDATE`,
		command.OrganizationID, command.OperationID).Scan(&operationStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DecisionOperation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.DecisionOperation{}, err
	}
	if operationStatus != string(domain.OperationPending) {
		if err := tx.Commit(ctx); err != nil {
			return domain.DecisionOperation{}, err
		}
		return s.GetDecisionOperation(ctx, application.GetDecisionOperationInput{
			OrganizationID: command.OrganizationID,
			OperationID:    command.OperationID,
		})
	}
	var requestStatus string
	err = tx.QueryRow(ctx, `SELECT r.status
		FROM approval_requests r WHERE r.organization_id=$1 AND r.id=$2 FOR UPDATE`, command.OrganizationID, command.RequestID).Scan(&requestStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DecisionOperation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.DecisionOperation{}, err
	}

	rejectionCode, rejectionMessage := "", ""
	if domain.ApprovalStatus(requestStatus).Terminal() {
		rejectionCode, rejectionMessage = "already_terminal", domain.ErrAlreadyTerminal.Error()
	}
	var membershipStatus, displayName, email string
	if rejectionCode == "" {
		err = tx.QueryRow(ctx, `SELECT m.status,u.display_name,u.primary_email
          FROM memberships m JOIN users u ON u.id=m.user_id
         WHERE m.organization_id=$1 AND m.id=$2`, command.OrganizationID, command.Principal.MembershipID).Scan(
			&membershipStatus, &displayName, &email)
		if errors.Is(err, pgx.ErrNoRows) {
			rejectionCode, rejectionMessage = "forbidden", domain.ErrForbidden.Error()
		} else if err != nil {
			return domain.DecisionOperation{}, err
		}
	}
	var allowSelfApproval bool
	var reauthSeconds int64
	var mfaMode, unroutedRequests string
	var verdict authorization.Verdict
	if rejectionCode == "" {
		if err := tx.QueryRow(ctx, `SELECT allow_self_approval,approval_reauth_window_seconds,mfa_mode,unrouted_requests
            FROM organization_policies WHERE organization_id=$1`, command.OrganizationID).Scan(
			&allowSelfApproval, &reauthSeconds, &mfaMode, &unroutedRequests); err != nil {
			return domain.DecisionOperation{}, err
		}
		routingTargets, err := decisionRoutingTargets(ctx, tx, command.OrganizationID, command.RequestID, command.Principal.MembershipID)
		if err != nil {
			return domain.DecisionOperation{}, err
		}
		// Grants are read inside the transaction so the check sees the
		// same assignments the decision is written against.
		grants, err := loadGrants(ctx, tx, command.OrganizationID, domain.PrincipalMembership, command.Principal.MembershipID)
		if err != nil {
			return domain.DecisionOperation{}, err
		}
		var rejection *authorization.Rejection
		verdict, rejection = authorization.EvaluateDecision(authorization.DecisionInput{
			MembershipStatus:      domain.MembershipStatus(membershipStatus),
			Grants:                grants,
			RequestID:             command.RequestID,
			RequesterMembershipID: "",
			ReviewerMembershipID:  command.Principal.MembershipID,
			AllowSelfApproval:     allowSelfApproval,
			HasRoutingTargets:     routingTargets.hasTargets,
			RoutedUserIDs:         routingTargets.userIDs,
			RoutedTeamIDs:         routingTargets.teamIDs,
			AddedReviewerIDs:      routingTargets.addedReviewerIDs,
			ReviewerTeamIDs:       routingTargets.reviewerTeamIDs,
			UnroutedRequests:      domain.UnroutedRequestsPolicy(unroutedRequests),
			OutsideRouting:        command.OutsideRouting,
			Authentication:        command.Principal.Auth,
			ApprovalReauthWindow:  time.Duration(reauthSeconds) * time.Second,
			MFAMode:               domain.MFAMode(mfaMode),
			Now:                   now,
		})
		if rejection != nil {
			rejectionCode, rejectionMessage = rejection.Code, rejection.Err.Error()
		}
	}
	if rejectionCode != "" {
		if _, err := tx.Exec(ctx, `UPDATE decision_operations SET status='rejected',error_code=$3,error_message=$4,completed_at=$5
            WHERE organization_id=$1 AND id=$2`, command.OrganizationID, command.OperationID, rejectionCode, rejectionMessage, now); err != nil {
			return domain.DecisionOperation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.DecisionOperation{}, err
		}
		return s.GetDecisionOperation(ctx, application.GetDecisionOperationInput{
			OrganizationID: command.OrganizationID,
			OperationID:    command.OperationID,
		})
	}

	decisionID := domain.ID(uuid.NewString())
	_, err = tx.Exec(ctx, `INSERT INTO decisions
		(id,request_id,organization_id,membership_id,source,status,note,channel,reviewer_display_name,reviewer_email,
		 auth_method,auth_assurance,authenticated_at,session_id,credential_id,idempotency_key,decided_at)
		VALUES ($1,$2,$3,$4,'human',$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13::text,'')::uuid,NULLIF($14::text,'')::uuid,$15,$16)`,
		decisionID, command.RequestID, command.OrganizationID, command.Principal.MembershipID, command.Status,
		command.Note, command.Channel, displayName, email, command.Principal.Auth.Method, command.Principal.Auth.Assurance,
		command.Principal.Auth.AuthenticatedAt, command.Principal.Auth.SessionID, command.Principal.Auth.CredentialID,
		command.IdempotencyKey, now)
	if err != nil {
		return domain.DecisionOperation{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE approval_requests SET status=$3,updated_at=$4 WHERE organization_id=$1 AND id=$2`,
		command.OrganizationID, command.RequestID, command.Status, now); err != nil {
		return domain.DecisionOperation{}, err
	}
	if err := recordRequestResolvedEvent(ctx, tx, command.OrganizationID, command.RequestID); err != nil {
		return domain.DecisionOperation{}, err
	}
	// A claim that outlived its expiry stopped holding the request then,
	// so it closes as lapsed with its own event rather than being credited
	// to a decision another reviewer made. Only a still-live claim is
	// closed by the decision itself.
	if err := s.lapseStaleClaims(ctx, tx, command.OrganizationID, command.RequestID, now); err != nil {
		return domain.DecisionOperation{}, err
	}
	if err := closeOpenClaims(ctx, tx, command.OrganizationID, command.RequestID, domain.ClaimDecided, now); err != nil {
		return domain.DecisionOperation{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE decision_operations SET status='applied',decision_id=$3,completed_at=$4
        WHERE organization_id=$1 AND id=$2`, command.OrganizationID, command.OperationID, decisionID, now); err != nil {
		return domain.DecisionOperation{}, err
	}
	data, _ := json.Marshal(map[string]any{"status": command.Status, "note": command.Note, "channel": command.Channel, "reviewer_display_name": displayName, "reviewer_email": email})
	if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: command.OrganizationID, EventType: "approval.decided",
		ActorType: domain.ActorHuman, ActorID: command.Principal.ActorID,
		SubjectType: "approval_request", SubjectID: command.RequestID,
		Data: data, OccurredAt: now,
	}); err != nil {
		return domain.DecisionOperation{}, err
	}
	// A decision made outside routing (break glass) is a dangerous act in
	// its own right, so it gets its own event beside the decision rather
	// than a flag readers could miss.
	if verdict.OutsideRouting {
		unrouted, _ := json.Marshal(map[string]any{"decision_id": decisionID, "reason": verdict.Reason, "dangerous": true})
		if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
			OrganizationID: command.OrganizationID, EventType: "decision.unrouted",
			ActorType: domain.ActorHuman, ActorID: command.Principal.ActorID,
			SubjectType: "approval_request", SubjectID: command.RequestID,
			Data: unrouted, OccurredAt: now,
		}); err != nil {
			return domain.DecisionOperation{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DecisionOperation{}, err
	}
	return s.GetDecisionOperation(ctx, application.GetDecisionOperationInput{
		OrganizationID: command.OrganizationID,
		OperationID:    command.OperationID,
	})
}
