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
	"github.com/withHumanAI/withHuman/internal/authorization"
)

func (s *Store) CreateAgentEnrollment(ctx context.Context, input application.CreateAgentEnrollmentInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	enrollment := input.Enrollment
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_enrollments
        (id, enrollment_code_hash, user_code, runtime, instance_name, instance_metadata, status, expires_at, created_at, requested_agent_slug)
        VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8, NULLIF($9, ''))`, enrollment.ID, enrollment.EnrollmentCodeHash,
		enrollment.UserCode, enrollment.Runtime, enrollment.InstanceName, enrollment.InstanceMetadata,
		enrollment.ExpiresAt, enrollment.CreatedAt, enrollment.RequestedAgentSlug)
	return err
}

func (s *Store) AuthorizeAgentEnrollment(ctx context.Context, input application.AuthorizeAgentEnrollmentInput) (domain.AgentInstance, error) {
	var instance domain.AgentInstance
	err := retrySerializable(func() error {
		var err error
		instance, err = s.authorizeAgentEnrollment(ctx, input)
		return err
	})
	return instance, err
}

func (s *Store) authorizeAgentEnrollment(ctx context.Context, input application.AuthorizeAgentEnrollmentInput) (domain.AgentInstance, error) {
	if err := input.Validate(); err != nil {
		return domain.AgentInstance{}, err
	}
	organizationID, membershipID, agentID := input.OrganizationID, input.MembershipID, input.AgentID
	userCode, now := input.UserCode, input.Now
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.AgentInstance{}, err
	}
	defer tx.Rollback(ctx)
	var enrollmentID, requestedAgentSlug, instanceName, membershipStatus, enrollmentStatus, enrolledOrg, enrolledAgent, enrolledInstance string
	var metadata json.RawMessage
	err = tx.QueryRow(ctx, `SELECT id::text, COALESCE(requested_agent_slug, ''), instance_name, instance_metadata, status, COALESCE(organization_id::text, ''), COALESCE(agent_id::text, ''), COALESCE(agent_instance_id::text, '')
        FROM agent_enrollments WHERE user_code = $1 AND expires_at > $2 FOR UPDATE`, userCode, now).Scan(
		&enrollmentID, &requestedAgentSlug, &instanceName, &metadata, &enrollmentStatus, &enrolledOrg, &enrolledAgent, &enrolledInstance)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentInstance{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.AgentInstance{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT status FROM memberships WHERE id = $1 AND organization_id = $2 FOR SHARE`, membershipID, organizationID).Scan(&membershipStatus); errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentInstance{}, domain.ErrForbidden
	} else if err != nil {
		return domain.AgentInstance{}, err
	} else if domain.MembershipStatus(membershipStatus) != domain.MembershipActive {
		return domain.AgentInstance{}, domain.ErrMembershipInactive
	}
	if enrolledOrg != "" && enrolledOrg != string(organizationID) {
		return domain.AgentInstance{}, domain.ErrNotFound
	}
	agent, err := scanAgent(tx.QueryRow(ctx, agentSelect+` FROM agents a JOIN organizations o ON o.id = a.organization_id WHERE a.organization_id = $1 AND a.id = $2 AND o.status = 'active' FOR SHARE OF a, o`, organizationID, agentID))
	if err != nil {
		return domain.AgentInstance{}, err
	}
	if agent.Status != domain.AgentActive {
		return domain.AgentInstance{}, domain.ErrAgentInactive
	}
	if requestedAgentSlug != "" && domain.AgentSlug(requestedAgentSlug) != agent.AgentSlug {
		return domain.AgentInstance{}, fmt.Errorf("selected agent does not match the CLI request: %w", domain.ErrConflict)
	}
	grants, err := loadGrants(ctx, tx, organizationID, domain.PrincipalMembership, membershipID)
	if err != nil {
		return domain.AgentInstance{}, err
	}
	// A member connects their own instance of a personal agent on the
	// strength of their membership; a service agent's instance needs
	// credential issuance on that agent, checked here against the same
	// grants the transaction writes under.
	if agent.Kind != domain.AgentPersonal {
		principal := domain.Principal{ActorType: domain.ActorHuman, OrganizationID: organizationID, MembershipID: membershipID}.WithGrants(grants)
		if err := authorization.Check(principal, domain.PermissionAgentCredentialIssue, domain.AgentResource(agent.AgentSlug)); err != nil {
			return domain.AgentInstance{}, err
		}
	}
	if enrollmentStatus != "pending" {
		if enrolledAgent != string(agentID) {
			return domain.AgentInstance{}, domain.ErrConflict
		}
		instance := domain.AgentInstance{AgentSlug: agent.AgentSlug}
		var ownerID sql.NullString
		err := tx.QueryRow(ctx, `SELECT id::text, organization_id::text, agent_id::text, name, status, metadata, created_at, last_seen_at, membership_id::text FROM agent_instances WHERE organization_id=$1 AND id=$2`, organizationID, enrolledInstance).Scan(&instance.ID, &instance.OrganizationID, &instance.AgentID, &instance.Name, &instance.Status, &instance.Metadata, &instance.CreatedAt, &instance.LastSeenAt, &ownerID)
		if err != nil {
			return domain.AgentInstance{}, err
		}
		instance.MembershipID = domain.ID(ownerID.String)
		if instance.Status != domain.AgentActive {
			return domain.AgentInstance{}, domain.ErrAgentInactive
		}
		return instance, tx.Commit(ctx)
	}
	// An instance of a personal agent belongs to the person authorizing
	// it; a service agent's instance belongs to nobody.
	var ownerID domain.ID
	if agent.Kind == domain.AgentPersonal {
		ownerID = membershipID
	}
	instance := domain.AgentInstance{
		ID: domain.ID(uuid.NewString()), OrganizationID: organizationID, AgentID: agentID, AgentSlug: agent.AgentSlug, MembershipID: ownerID, Name: instanceName,
		Status: domain.AgentActive, Metadata: metadata, CreatedAt: now, LastSeenAt: now,
	}
	command, err := tx.Exec(ctx, `INSERT INTO agent_instances
        (id, organization_id, agent_id, membership_id, name, status, metadata, created_at, last_seen_at)
        VALUES ($1, $2, $3, NULLIF($4::text, '')::uuid, $5, 'active', $6, $7, $7)
        ON CONFLICT (agent_id, name) DO NOTHING`, instance.ID, organizationID, agentID, ownerID, instanceName, metadata, now)
	if err != nil {
		return domain.AgentInstance{}, err
	}
	if command.RowsAffected() == 0 {
		return domain.AgentInstance{}, fmt.Errorf("agent instance name already exists: %w", domain.ErrConflict)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_enrollments SET status = 'authorized', organization_id = $2,
        membership_id = $3, agent_id = $4, agent_instance_id = $5, authorized_at = $6 WHERE id = $1`, enrollmentID,
		organizationID, membershipID, agentID, instance.ID, now); err != nil {
		return domain.AgentInstance{}, err
	}
	data, _ := json.Marshal(map[string]any{"agent_slug": agent.AgentSlug, "agent_name": agent.Name, "instance_name": instanceName, "membership_id": ownerID})
	var userID string
	if err := tx.QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE id = $1 AND organization_id = $2`,
		membershipID, organizationID).Scan(&userID); err != nil {
		return domain.AgentInstance{}, err
	}
	if err := s.appendAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: organizationID, EventType: "agent_instance.enrolled",
		ActorType: domain.ActorHuman, ActorID: domain.ID(userID),
		SubjectType: "agent_instance", SubjectID: instance.ID, Data: data, OccurredAt: now,
	}); err != nil {
		return domain.AgentInstance{}, err
	}
	return instance, tx.Commit(ctx)
}

func (s *Store) ExchangeAgentEnrollment(ctx context.Context, input application.ExchangeAgentEnrollmentInput) (domain.AgentEnrollmentIdentity, error) {
	if err := input.Validate(); err != nil {
		return domain.AgentEnrollmentIdentity{}, err
	}
	enrollmentCodeHash, credential, now := input.EnrollmentCodeHash, input.Credential, input.Now
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.AgentEnrollmentIdentity{}, err
	}
	defer tx.Rollback(ctx)
	var instance domain.AgentInstance
	var id, organizationID, agentID, instanceStatus, agentStatus string
	var ownerID sql.NullString
	err = tx.QueryRow(ctx, `SELECT ai.id::text, ai.organization_id::text, ai.agent_id::text, ai.name,
        ai.status, ai.metadata, ai.created_at, ai.last_seen_at, a.status, ai.membership_id::text
      FROM agent_enrollments e
      JOIN agent_instances ai ON ai.id = e.agent_instance_id AND ai.organization_id = e.organization_id
      JOIN agents a ON a.id = ai.agent_id AND a.organization_id = ai.organization_id
     WHERE e.enrollment_code_hash = $1 AND e.status = 'authorized' AND e.expires_at > $2
     FOR UPDATE OF e`, enrollmentCodeHash, now).Scan(&id, &organizationID, &agentID, &instance.Name,
		&instanceStatus, &instance.Metadata, &instance.CreatedAt, &instance.LastSeenAt, &agentStatus, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentEnrollmentIdentity{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.AgentEnrollmentIdentity{}, err
	}
	if domain.AgentStatus(agentStatus) != domain.AgentActive || domain.AgentStatus(instanceStatus) != domain.AgentActive {
		return domain.AgentEnrollmentIdentity{}, domain.ErrAgentInactive
	}
	instance.ID, instance.OrganizationID, instance.AgentID = domain.ID(id), domain.ID(organizationID), domain.ID(agentID)
	instance.MembershipID, instance.Status, instance.LastSeenAt = domain.ID(ownerID.String), domain.AgentStatus(instanceStatus), now
	agent, err := scanAgent(tx.QueryRow(ctx, agentSelect+` FROM agents a WHERE a.organization_id = $1 AND a.id = $2 FOR SHARE`, instance.OrganizationID, instance.AgentID))
	if err != nil {
		return domain.AgentEnrollmentIdentity{}, err
	}
	instance.AgentSlug = agent.AgentSlug
	if err := insertAgentCredential(ctx, tx, instance.OrganizationID, instance.ID, credential); err != nil {
		return domain.AgentEnrollmentIdentity{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_enrollments SET status = 'consumed', consumed_at = $2
        WHERE enrollment_code_hash = $1`, enrollmentCodeHash, now); err != nil {
		return domain.AgentEnrollmentIdentity{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_instances SET last_seen_at = $2 WHERE id = $1`, instance.ID, now); err != nil {
		return domain.AgentEnrollmentIdentity{}, err
	}
	return domain.AgentEnrollmentIdentity{Agent: agent, AgentInstance: instance}, tx.Commit(ctx)
}

func (s *Store) GetAgentEnrollment(ctx context.Context, input application.GetAgentEnrollmentInput) (domain.AgentEnrollment, error) {
	if err := input.Validate(); err != nil {
		return domain.AgentEnrollment{}, err
	}
	var e domain.AgentEnrollment
	err := s.pool.QueryRow(ctx, `SELECT runtime, COALESCE(requested_agent_slug, ''), instance_name, status, expires_at, COALESCE(agent_id::text, '') FROM agent_enrollments WHERE user_code=$1 AND expires_at>$2 AND (organization_id IS NULL OR organization_id=$3)`, input.UserCode, input.Now, input.OrganizationID).Scan(&e.Runtime, &e.RequestedAgentSlug, &e.InstanceName, &e.Status, &e.ExpiresAt, &e.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, domain.ErrNotFound
	}
	return e, err
}

func (s *Store) ListEnrollmentAgents(ctx context.Context, input application.ListEnrollmentAgentsInput) (domain.EnrollmentAgents, error) {
	page := domain.EnrollmentAgents{Agents: []domain.Agent{}}
	if err := input.Validate(); err != nil {
		return page, err
	}
	// Every active member may enroll their own instance of a personal
	// agent, so those are always eligible; service agents are the ones the
	// scope of the person's credential issuance permission names.
	rows, err := s.pool.Query(ctx, agentSelect+` FROM agents a WHERE a.organization_id=$1 AND a.status='active' AND (a.kind='personal' OR $2::boolean OR a.slug=ANY($3::text[])) AND (strpos(lower(a.name), lower($4))>0 OR strpos(lower(a.slug), lower($4))>0) ORDER BY lower(a.name), a.id LIMIT $5 OFFSET $6`, input.OrganizationID, input.Scope.All, input.Scope.IDs(domain.ScopeAgent), input.Query, input.Limit+1, input.Offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return page, err
		}
		if len(page.Agents) == input.Limit {
			next := input.Offset + input.Limit
			page.NextOffset = &next
			break
		}
		page.Agents = append(page.Agents, agent)
	}
	return page, rows.Err()
}
