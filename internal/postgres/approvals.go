package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

func (s *Store) InitializeApproval(ctx context.Context, input application.InitializeApprovalInput) (domain.ApprovalRequest, error) {
	if err := input.Validate(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	request := input.Request
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `INSERT INTO approval_requests
		(id, organization_id, agent_id, agent_instance_id, agent_name, agent_instance_name, agent_slug, tool, arguments,
		 agent_reasoning, context, idempotency_key, semantic_hash, status, deadline_at, created_at, updated_at, timeout, server)
		SELECT $1,$2,$3,$4,a.name,ai.name,a.slug,$6,$7,$8,
		       NULLIF($9::text,'')::jsonb,$10,$11,$12,$13,$14,$15,$16,$17
		  FROM agents a JOIN agent_instances ai ON ai.agent_id=a.id AND ai.organization_id=a.organization_id
		 WHERE a.id=$3 AND ai.id=$4 AND a.organization_id=$2 AND a.slug=$5
		ON CONFLICT (organization_id, agent_instance_id, idempotency_key) DO NOTHING`, request.ID, request.OrganizationID,
		request.AgentID, request.AgentInstanceID, request.AgentSlug, request.Tool, request.Arguments,
		request.AgentReasoning, string(request.Context), request.IdempotencyKey, request.SemanticHash,
		request.Status, request.DeadlineAt, request.CreatedAt, request.UpdatedAt, request.Timeout, request.Server)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if command.RowsAffected() == 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.ApprovalRequest{}, err
		}
		return s.GetApprovalByIdempotencyKey(ctx, application.GetApprovalByIdempotencyKeyInput{
			OrganizationID:  request.OrganizationID,
			AgentInstanceID: request.AgentInstanceID,
			IdempotencyKey:  request.IdempotencyKey,
		})
	}
	if request.Status == domain.ApprovalPending {
		if _, err := tx.Exec(ctx, `INSERT INTO outbox_events
			(id, organization_id, topic, payload, created_at)
			VALUES ($1, $2, $3, jsonb_build_object('request_id', $4::text), $5)`, approvalWorkflowEventID(request.ID),
			request.OrganizationID, approvalWorkflowStartTopic, request.ID, request.CreatedAt); err != nil {
			return domain.ApprovalRequest{}, err
		}
	}
	if input.Decision != nil {
		decision := input.Decision
		if _, err := tx.Exec(ctx, `INSERT INTO decisions
			(id, request_id, organization_id, source, status, note, channel, auth_method, auth_assurance,
			 authenticated_at, idempotency_key, decided_at)
			VALUES ($1, $2, $3, 'pipeline', $4, $5, $6, $7, $8, $9, $10, $11)`, decision.ID, decision.RequestID,
			decision.OrganizationID, decision.Status, decision.Note, decision.Channel, decision.Auth.Method,
			decision.Auth.Assurance, decision.Auth.AuthenticatedAt, decision.IdempotencyKey,
			decision.DecidedAt); err != nil {
			return domain.ApprovalRequest{}, err
		}
		if err := recordRequestResolvedEvent(ctx, tx, decision.OrganizationID, decision.RequestID); err != nil {
			return domain.ApprovalRequest{}, err
		}
	}
	for _, event := range input.AuditEvents {
		if err := s.appendAuditEvent(ctx, tx, event); err != nil {
			return domain.ApprovalRequest{}, err
		}
	}
	if suspension := input.Suspension; suspension != nil {
		if err := insertPipelineSuspension(ctx, tx, *suspension); err != nil {
			return domain.ApprovalRequest{}, err
		}
	}
	if routing := input.Routing; routing != nil {
		if err := insertApprovalRouting(ctx, tx, *routing); err != nil {
			return domain.ApprovalRequest{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return s.GetApprovalByIdempotencyKey(ctx, application.GetApprovalByIdempotencyKeyInput{
		OrganizationID:  request.OrganizationID,
		AgentInstanceID: request.AgentInstanceID,
		IdempotencyKey:  request.IdempotencyKey,
	})
}

const approvalSelect = `SELECT r.id::text, r.organization_id::text, r.agent_id::text, r.agent_instance_id::text,
	r.agent_name, r.agent_instance_name, r.agent_slug, r.tool, r.arguments, r.agent_reasoning, r.context,
	r.idempotency_key, r.semantic_hash, r.status, r.deadline_at, r.created_at, r.updated_at, r.timeout, r.server,
	d.id::text, d.membership_id::text, d.source, d.status, d.note, d.channel,
    d.reviewer_display_name, d.reviewer_email, d.auth_method, d.auth_assurance,
    d.authenticated_at, d.session_id::text, d.credential_id::text, d.idempotency_key, d.decided_at, d.expires_at,
    cl.id::text, cl.membership_id::text, cu.display_name, cl.claimed_at, cl.expires_at
  FROM approval_requests r LEFT JOIN decisions d ON d.request_id = r.id
  LEFT JOIN approval_request_claims cl
    ON cl.request_id = r.id AND cl.organization_id = r.organization_id AND cl.released_at IS NULL
  LEFT JOIN memberships cm ON cm.id = cl.membership_id AND cm.organization_id = cl.organization_id
  LEFT JOIN users cu ON cu.id = cm.user_id `

func (s *Store) GetApproval(ctx context.Context, input application.GetApprovalInput) (domain.ApprovalRequest, error) {
	if err := input.Validate(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	request, err := scanApproval(s.pool.QueryRow(ctx, approvalSelect+`WHERE r.organization_id=$1 AND r.id=$2`, input.OrganizationID, input.RequestID))
	if err != nil || input.VisibleTo == "" {
		return request, err
	}
	// A request outside the membership's view reads as absent, so the
	// detail route never confirms that a hidden request exists.
	visible, err := s.visibleApprovals(ctx, input.OrganizationID, input.VisibleTo, false, []domain.ApprovalRequest{request})
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if len(visible) == 0 {
		return domain.ApprovalRequest{}, domain.ErrNotFound
	}
	return request, nil
}

func (s *Store) GetApprovalByIdempotencyKey(ctx context.Context, input application.GetApprovalByIdempotencyKeyInput) (domain.ApprovalRequest, error) {
	if err := input.Validate(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return scanApproval(s.pool.QueryRow(ctx, approvalSelect+`WHERE r.organization_id=$1 AND r.agent_instance_id=$2 AND r.idempotency_key=$3`, input.OrganizationID, input.AgentInstanceID, input.IdempotencyKey))
}

func (s *Store) ListApprovals(ctx context.Context, input application.ListApprovalsInput) ([]domain.ApprovalRequest, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	limit := input.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if input.VisibleTo == "" {
		return s.listApprovalCandidates(ctx, input, limit, input.Offset)
	}
	// Routing visibility is evaluated against pinned documents in Go.
	// Page after that check, so an invisible batch cannot hide later rows.
	visible := make([]domain.ApprovalRequest, 0, limit)
	skip := input.Offset
	for offset := 0; ; offset += 200 {
		candidates, err := s.listApprovalCandidates(ctx, input, 200, offset)
		if err != nil {
			return nil, err
		}
		allowed, err := s.visibleApprovals(ctx, input.OrganizationID, input.VisibleTo, input.HideUnrouted, candidates)
		if err != nil {
			return nil, err
		}
		for _, request := range allowed {
			if skip > 0 {
				skip--
				continue
			}
			visible = append(visible, request)
			if len(visible) == limit {
				return visible, nil
			}
		}
		if len(candidates) < 200 {
			return visible, nil
		}
	}
}

func (s *Store) listApprovalCandidates(ctx context.Context, input application.ListApprovalsInput, limit, offset int) ([]domain.ApprovalRequest, error) {
	rows, err := s.pool.Query(ctx, approvalSelect+`WHERE r.organization_id=$1
		AND (NOT $2::boolean OR d.source IS NULL OR d.source <> $3)
		AND ($5::text = '' OR r.agent_slug = $5)
		AND ($7::text = '' OR r.status = $7)
		AND ($8::text = '' OR strpos(lower(r.tool || ' ' || COALESCE(r.agent_instance_name, '')), lower($8)) > 0)
		AND ($9::text = '' OR EXISTS (SELECT 1 FROM approval_request_notifications n
			WHERE n.request_id = r.id AND n.organization_id = r.organization_id AND n.membership_id = NULLIF($9, '')::uuid))
		ORDER BY r.created_at DESC, r.id DESC LIMIT $4 OFFSET $6`, input.OrganizationID, input.HumanReviewOnly,
		string(domain.DecisionSourcePipeline), limit, string(input.AgentSlug), offset, input.Status, input.Query, string(input.NotifiedTo))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := []domain.ApprovalRequest{}
	for rows.Next() {
		request, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func scanApproval(row rowScanner) (domain.ApprovalRequest, error) {
	var request domain.ApprovalRequest
	var id, organizationID, agentID, agentInstanceID, status string
	var contextJSON []byte
	var decisionID, membershipID, decisionSource, decisionStatus, note, channel sql.NullString
	var reviewerName, reviewerEmail, authMethod, authAssurance sql.NullString
	var authenticatedAt, decidedAt, expiresAt sql.NullTime
	var sessionID, credentialID, decisionKey sql.NullString
	var claimID, claimMembershipID, claimantName sql.NullString
	var claimedAt, claimExpiresAt sql.NullTime
	err := row.Scan(&id, &organizationID, &agentID, &agentInstanceID, &request.AgentName, &request.AgentInstanceName,
		&request.AgentSlug, &request.Tool, &request.Arguments, &request.AgentReasoning, &contextJSON,
		&request.IdempotencyKey, &request.SemanticHash, &status, &request.DeadlineAt, &request.CreatedAt, &request.UpdatedAt, &request.Timeout, &request.Server,
		&decisionID, &membershipID, &decisionSource, &decisionStatus, &note, &channel, &reviewerName, &reviewerEmail,
		&authMethod, &authAssurance, &authenticatedAt, &sessionID, &credentialID, &decisionKey, &decidedAt, &expiresAt,
		&claimID, &claimMembershipID, &claimantName, &claimedAt, &claimExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ApprovalRequest{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	request.ID, request.OrganizationID, request.AgentID, request.AgentInstanceID = domain.ID(id), domain.ID(organizationID), domain.ID(agentID), domain.ID(agentInstanceID)
	request.Status, request.Context = domain.ApprovalStatus(status), contextJSON
	if decisionID.Valid {
		request.Decision = &domain.Decision{
			ID: domain.ID(decisionID.String), RequestID: request.ID, OrganizationID: request.OrganizationID,
			MembershipID: domain.ID(membershipID.String), Source: domain.DecisionSource(decisionSource.String), Status: domain.ApprovalStatus(decisionStatus.String),
			Note: note.String, Channel: channel.String, ReviewerDisplayName: reviewerName.String,
			ReviewerEmail: reviewerEmail.String, IdempotencyKey: decisionKey.String, DecidedAt: decidedAt.Time,
			Auth: domain.AuthContext{Method: authMethod.String, Assurance: authAssurance.String,
				AuthenticatedAt: authenticatedAt.Time, SessionID: domain.ID(sessionID.String), CredentialID: domain.ID(credentialID.String)},
		}
	}
	if request.Decision != nil && expiresAt.Valid {
		request.Decision.ExpiresAt = &expiresAt.Time
	}
	if claimID.Valid {
		request.Claim = &domain.Claim{
			ID: domain.ID(claimID.String), RequestID: request.ID, OrganizationID: request.OrganizationID,
			MembershipID: domain.ID(claimMembershipID.String), ClaimantDisplayName: claimantName.String,
			ClaimedAt: claimedAt.Time, ExpiresAt: claimExpiresAt.Time,
		}
	}
	return request, nil
}

func (s *Store) ExpireApproval(ctx context.Context, input application.ExpireApprovalInput) (domain.ApprovalRequest, error) {
	if err := input.Validate(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	organizationID, requestID, now := input.OrganizationID, input.RequestID, input.Now
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM approval_requests WHERE organization_id=$1 AND id=$2 FOR UPDATE`, organizationID, requestID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ApprovalRequest{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if domain.ApprovalStatus(status).Terminal() {
		if err := tx.Commit(ctx); err != nil {
			return domain.ApprovalRequest{}, err
		}
		return s.GetApproval(ctx, application.GetApprovalInput{OrganizationID: organizationID, RequestID: requestID})
	}
	decisionID := domain.ID(uuid.NewString())
	key := "expiration:" + string(requestID)
	if _, err := tx.Exec(ctx, `INSERT INTO decisions
		(id,request_id,organization_id,source,status,channel,auth_method,auth_assurance,authenticated_at,idempotency_key,decided_at)
		VALUES ($1,$2,$3,'system','expired','system','system','system',$5,$4,$5)`, decisionID, requestID, organizationID, key, now); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE approval_requests SET status='expired',updated_at=$3 WHERE organization_id=$1 AND id=$2`, organizationID, requestID, now); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if err := recordRequestResolvedEvent(ctx, tx, organizationID, requestID); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if err := closeOpenClaims(ctx, tx, organizationID, requestID, domain.ClaimExpired, now); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: organizationID, EventType: "approval.expired", ActorType: domain.ActorSystem,
		SubjectType: "approval_request", SubjectID: requestID,
		Data: []byte(`{}`), OccurredAt: now,
	}); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return s.GetApproval(ctx, application.GetApprovalInput{OrganizationID: organizationID, RequestID: requestID})
}

// CancelApproval withdraws a pending request on behalf of the instance
// that opened it. The cancellation is the request's one terminal decision
// (source agent, channel aap), any open claim closes with reason
// cancelled, and approval.cancelled is appended with the instance in its
// data. A request this instance already cancelled comes back unchanged so
// an adapter's retried cancel is idempotent; any other terminal state is
// ErrAlreadyTerminal, and another instance is ErrForbidden.
func (s *Store) CancelApproval(ctx context.Context, input application.CancelApprovalInput) (domain.ApprovalRequest, error) {
	if err := input.Validate(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	organizationID, requestID, now := input.OrganizationID, input.RequestID, input.Now
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	defer tx.Rollback(ctx)
	var status, instanceID, instanceName string
	var agentID string
	err = tx.QueryRow(ctx, `SELECT status, agent_id::text, agent_instance_id::text, agent_instance_name
		FROM approval_requests WHERE organization_id=$1 AND id=$2 FOR UPDATE`, organizationID, requestID).
		Scan(&status, &agentID, &instanceID, &instanceName)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ApprovalRequest{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if domain.ID(instanceID) != input.AgentInstanceID {
		return domain.ApprovalRequest{}, fmt.Errorf("only the instance that opened the request may cancel it: %w", domain.ErrForbidden)
	}
	switch domain.ApprovalStatus(status) {
	case domain.ApprovalCancelled:
		if err := tx.Commit(ctx); err != nil {
			return domain.ApprovalRequest{}, err
		}
		return s.GetApproval(ctx, application.GetApprovalInput{OrganizationID: organizationID, RequestID: requestID})
	case domain.ApprovalPending:
	default:
		return domain.ApprovalRequest{}, domain.ErrAlreadyTerminal
	}
	decisionID := domain.ID(uuid.NewString())
	key := "cancellation:" + string(requestID)
	if _, err := tx.Exec(ctx, `INSERT INTO decisions
		(id,request_id,organization_id,source,status,channel,auth_method,auth_assurance,authenticated_at,credential_id,idempotency_key,decided_at)
		VALUES ($1,$2,$3,'agent','cancelled','aap',$4,$5,$6,NULLIF($7::text,'')::uuid,$8,$9)`, decisionID, requestID, organizationID,
		input.Auth.Method, input.Auth.Assurance, input.Auth.AuthenticatedAt, input.Auth.CredentialID, key, now); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE approval_requests SET status='cancelled',updated_at=$3 WHERE organization_id=$1 AND id=$2`, organizationID, requestID, now); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if err := recordRequestResolvedEvent(ctx, tx, organizationID, requestID); err != nil {
		return domain.ApprovalRequest{}, err
	}
	// A claim that already lapsed closes as lapsed with its own event, as
	// on a decision; only a still-live claim is closed by the cancellation.
	if err := s.lapseStaleClaims(ctx, tx, organizationID, requestID, now); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if err := closeOpenClaims(ctx, tx, organizationID, requestID, domain.ClaimCancelled, now); err != nil {
		return domain.ApprovalRequest{}, err
	}
	data, _ := json.Marshal(map[string]any{"agent_instance_id": instanceID, "agent_instance_name": instanceName})
	if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: organizationID, EventType: "approval.cancelled",
		ActorType: domain.ActorAgent, ActorID: domain.ID(agentID),
		SubjectType: "approval_request", SubjectID: requestID,
		Data: data, OccurredAt: now,
	}); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return s.GetApproval(ctx, application.GetApprovalInput{OrganizationID: organizationID, RequestID: requestID})
}

func (s *Store) CountPendingApprovals(ctx context.Context, input application.CountPendingApprovalsInput) (int64, error) {
	if err := input.Validate(); err != nil {
		return 0, err
	}
	var count int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM approval_requests
        WHERE organization_id = $1 AND status = 'pending'`, input.OrganizationID).Scan(&count)
	return count, err
}
