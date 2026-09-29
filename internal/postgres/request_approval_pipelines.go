package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// agent_slug IS NULL stores the organization scope; the Go layer represents
// that scope as an empty AgentSlug, so reads coalesce and writes NULLIF.
const requestApprovalPipelineSelect = `SELECT id::text, organization_id::text, COALESCE(agent_slug, ''), revision,
	is_active, block_ids::text[], COALESCE(default_escalation_path, ''), default_ask_agent_owner,
	created_by_actor_id::text, created_at, archived_at
	FROM request_approval_pipelines `

const requestApprovalBlockSelect = `SELECT b.id::text, b.organization_id::text, b.block_key, b.name, b.block_type,
	b.config_version, b.config, b.timeout_ms, b.max_attempts, b.content_hash,
	b.created_by_actor_id::text, b.created_at `

func (s *Store) CreateRequestApprovalPipelineRevision(ctx context.Context, input application.CreateRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	if err := input.Validate(); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockRequestApprovalPipeline(ctx, tx, input.OrganizationID, input.AgentSlug); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	// A scope comes into being with its first agent (or the organization),
	// never through the editor, so an unknown agent type has no pipeline;
	// a scope archived with its last agent takes no revisions either.
	if _, hasActive, err := activeRequestApprovalPipelineRevision(ctx, tx, input.OrganizationID, input.AgentSlug); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	} else if !hasActive {
		return domain.RequestApprovalPipelineRevision{}, missingRequestApprovalPipelineScope(ctx, tx, input.OrganizationID, input.AgentSlug)
	}
	actorID, err := managementActorID(ctx, tx, input.OrganizationID, input.CreatedByActorID)
	if err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}

	blockIDs := make([]domain.ID, 0, len(input.Blocks))
	for _, blockInput := range input.Blocks {
		block, err := insertOrReuseRequestApprovalBlock(ctx, tx, input, blockInput)
		if err != nil {
			return domain.RequestApprovalPipelineRevision{}, err
		}
		blockIDs = append(blockIDs, block.ID)
	}
	var revisionNumber int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(revision), 0) + 1
		FROM request_approval_pipelines WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, '')`,
		input.OrganizationID, input.AgentSlug).Scan(&revisionNumber); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	revision := domain.RequestApprovalPipelineRevision{
		ID: input.ID, OrganizationID: input.OrganizationID, AgentSlug: input.AgentSlug,
		Revision: revisionNumber, IsActive: false, BlockIDs: blockIDs, DefaultEscalationPath: input.DefaultEscalationPath,
		DefaultAskAgentOwner: input.DefaultAskAgentOwner, CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
	}
	if _, err := tx.Exec(ctx, `INSERT INTO request_approval_pipelines
		(id, organization_id, agent_slug, revision, is_active, block_ids, default_escalation_path,
		 default_ask_agent_owner, created_by_actor_id, created_at)
		VALUES ($1, $2, NULLIF($3, ''), $4, false, $5, NULLIF($6, ''), $7, $8, $9)`, revision.ID, revision.OrganizationID,
		revision.AgentSlug, revision.Revision, idStrings(revision.BlockIDs), revision.DefaultEscalationPath,
		revision.DefaultAskAgentOwner, revision.CreatedByActorID, revision.CreatedAt); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	data, _ := json.Marshal(map[string]any{
		"scope":       domain.PipelineScopeName(revision.AgentSlug),
		"agent_slug":  revision.AgentSlug,
		"revision":    revision.Revision,
		"block_count": len(revision.BlockIDs),
	})
	if err := s.appendManagementAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: input.OrganizationID, EventType: "request_approval_pipeline.revision_created",
		ActorType: domain.ActorHuman, ActorID: actorID,
		SubjectType: "request_approval_pipeline", SubjectID: revision.ID,
		Data: data, OccurredAt: input.Now,
	}); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	return revision, nil
}

func (s *Store) ListRequestApprovalPipelineSummaries(ctx context.Context, input application.ListRequestApprovalPipelineSummariesInput) ([]domain.RequestApprovalPipelineSummary, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	limit := input.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// A scope is archived when every revision is; one live revision (the
	// fresh one a later agent of the type brought) makes it live again,
	// its archived history included in the counts.
	statusFilter := "HAVING bool_or(archived_at IS NULL)"
	switch input.Status {
	case application.PipelineStatusArchived:
		statusFilter = "HAVING bool_and(archived_at IS NOT NULL)"
	case application.PipelineStatusAll:
		statusFilter = ""
	}
	// The organization pipeline has a null agent type, so it never matches
	// the agent-type list: a scoped reader sees only their own types.
	rows, err := s.pool.Query(ctx, `SELECT COALESCE(agent_slug, ''),
		max(revision) FILTER (WHERE is_active), max(revision), count(*), max(created_at),
		CASE WHEN bool_and(archived_at IS NOT NULL) THEN max(archived_at) END
		FROM request_approval_pipelines
		WHERE organization_id = $1 AND ($3::boolean OR agent_slug = ANY($4::text[]))
		GROUP BY agent_slug `+statusFilter+`
		ORDER BY COALESCE(agent_slug, '')
		LIMIT $2`, input.OrganizationID, limit, input.Scope.All, input.Scope.IDs(domain.ScopeAgent))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make([]domain.RequestApprovalPipelineSummary, 0)
	for rows.Next() {
		var summary domain.RequestApprovalPipelineSummary
		if err := rows.Scan(&summary.AgentSlug, &summary.ActiveRevision, &summary.LatestRevision,
			&summary.RevisionCount, &summary.LatestCreatedAt, &summary.ArchivedAt); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func insertOrReuseRequestApprovalBlock(ctx context.Context, tx pgx.Tx, input application.CreateRequestApprovalPipelineRevisionInput, blockInput application.RequestApprovalBlockSnapshotInput) (domain.RequestApprovalBlock, error) {
	query := requestApprovalBlockSelect + `FROM request_approval_blocks b WHERE b.organization_id = $1
		AND b.content_hash = request_approval_block_content_hash($2, $3, $4, $5, $6, $7, $8)`
	row := tx.QueryRow(ctx, `INSERT INTO request_approval_blocks
		(id, organization_id, block_key, name, block_type, config_version, config, timeout_ms,
		 max_attempts, created_by_actor_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (organization_id, content_hash) DO NOTHING
		RETURNING id::text, organization_id::text, block_key, name, block_type, config_version,
		config, timeout_ms, max_attempts, content_hash, created_by_actor_id::text, created_at`,
		blockInput.ID, input.OrganizationID, blockInput.BlockKey, blockInput.Name, blockInput.BlockType,
		blockInput.ConfigVersion, blockInput.Config, blockInput.TimeoutMS, blockInput.MaxAttempts,
		input.CreatedByActorID, input.Now)
	block, err := scanRequestApprovalBlock(row)
	if !errors.Is(err, domain.ErrNotFound) {
		return block, err
	}
	return scanRequestApprovalBlock(tx.QueryRow(ctx, query, input.OrganizationID, blockInput.BlockKey,
		blockInput.Name, blockInput.BlockType, blockInput.ConfigVersion, blockInput.Config,
		blockInput.TimeoutMS, blockInput.MaxAttempts))
}

func (s *Store) GetRequestApprovalPipelineRevision(ctx context.Context, input application.GetRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	if err := input.Validate(); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	return scanRequestApprovalPipelineRevision(s.pool.QueryRow(ctx, requestApprovalPipelineSelect+
		`WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, '') AND revision = $3`,
		input.OrganizationID, input.AgentSlug, input.Revision))
}

func (s *Store) GetRequestApprovalPipelineRevisionByID(ctx context.Context, input application.GetRequestApprovalPipelineRevisionByIDInput) (domain.RequestApprovalPipelineRevision, error) {
	if err := input.Validate(); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	return scanRequestApprovalPipelineRevision(s.pool.QueryRow(ctx, requestApprovalPipelineSelect+
		`WHERE organization_id = $1 AND id = $2`, input.OrganizationID, input.RevisionID))
}

func (s *Store) ListRequestApprovalPipelineRevisions(ctx context.Context, input application.ListRequestApprovalPipelineRevisionsInput) (application.RevisionPage[domain.RequestApprovalPipelineRevision], error) {
	var page application.RevisionPage[domain.RequestApprovalPipelineRevision]
	if err := input.Validate(); err != nil {
		return page, err
	}
	limit := revisionPageLimit(input.Limit)
	before, _ := application.DecodeRevisionCursor(input.Cursor)
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM request_approval_pipelines
		WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, '')`,
		input.OrganizationID, input.AgentSlug).Scan(&page.TotalCount); err != nil {
		return page, err
	}
	rows, err := s.pool.Query(ctx, requestApprovalPipelineSelect+`WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, '')
		AND ($3 = 0 OR revision < $3)
		ORDER BY revision DESC LIMIT $4`, input.OrganizationID, input.AgentSlug, before, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	revisions := make([]domain.RequestApprovalPipelineRevision, 0)
	for rows.Next() {
		revision, err := scanRequestApprovalPipelineRevision(rows)
		if err != nil {
			return page, err
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	page.Revisions, page.NextCursor = trimRevisionPage(revisions, limit, func(revision domain.RequestApprovalPipelineRevision) int64 { return revision.Revision })
	return page, nil
}

func (s *Store) ListActiveRequestApprovalPipelineRevisions(ctx context.Context, input application.ListActiveRequestApprovalPipelineRevisionsInput) ([]domain.RequestApprovalPipelineRevision, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, requestApprovalPipelineSelect+`WHERE is_active
		AND (NULLIF($1, '')::uuid IS NULL OR id > NULLIF($1, '')::uuid)
		ORDER BY id LIMIT $2`, input.AfterID, input.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	revisions := make([]domain.RequestApprovalPipelineRevision, 0, input.Limit)
	for rows.Next() {
		revision, err := scanRequestApprovalPipelineRevision(rows)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	return revisions, rows.Err()
}

func (s *Store) GetActiveRequestApprovalPipelineRevision(ctx context.Context, input application.GetActiveRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	if err := input.Validate(); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	return scanRequestApprovalPipelineRevision(s.pool.QueryRow(ctx, requestApprovalPipelineSelect+
		`WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, '') AND is_active`, input.OrganizationID, input.AgentSlug))
}

func (s *Store) GetRequestApprovalBlocks(ctx context.Context, input application.GetRequestApprovalBlocksInput) ([]domain.RequestApprovalBlock, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if len(input.BlockIDs) == 0 {
		return []domain.RequestApprovalBlock{}, nil
	}
	rows, err := s.pool.Query(ctx, requestApprovalBlockSelect+`FROM unnest($2::uuid[]) WITH ORDINALITY requested(id, position)
		JOIN request_approval_blocks b ON b.id = requested.id AND b.organization_id = $1
		ORDER BY requested.position`, input.OrganizationID, idStrings(input.BlockIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blocks := make([]domain.RequestApprovalBlock, 0, len(input.BlockIDs))
	for rows.Next() {
		block, err := scanRequestApprovalBlock(rows)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(blocks) != len(input.BlockIDs) {
		return nil, domain.ErrNotFound
	}
	return blocks, nil
}

func (s *Store) ActivateRequestApprovalPipelineRevision(ctx context.Context, input application.ActivateRequestApprovalPipelineRevisionInput) (domain.RequestApprovalPipelineRevision, error) {
	if err := input.Validate(); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockRequestApprovalPipeline(ctx, tx, input.OrganizationID, input.AgentSlug); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	current, hasCurrent, err := activeRequestApprovalPipelineRevision(ctx, tx, input.OrganizationID, input.AgentSlug)
	if err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	if !hasCurrent {
		return domain.RequestApprovalPipelineRevision{}, missingRequestApprovalPipelineScope(ctx, tx, input.OrganizationID, input.AgentSlug)
	}
	if current.Revision != input.ExpectedActiveRevision {
		return domain.RequestApprovalPipelineRevision{}, fmt.Errorf("active request approval pipeline revision changed: %w", domain.ErrPreconditionFailed)
	}
	target, err := scanRequestApprovalPipelineRevision(tx.QueryRow(ctx, requestApprovalPipelineSelect+
		`WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, '') AND revision = $3`, input.OrganizationID,
		input.AgentSlug, input.Revision))
	if err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	// A revived scope keeps the revisions archived with its last agent as
	// frozen history; rolling back to one is refused.
	if target.ArchivedAt != nil {
		return domain.RequestApprovalPipelineRevision{}, fmt.Errorf("revision %d: %w", target.Revision, domain.ErrRequestApprovalPipelineArchived)
	}
	if current.ID == target.ID {
		if err := tx.Commit(ctx); err != nil {
			return domain.RequestApprovalPipelineRevision{}, err
		}
		return target, nil
	}
	if err := requirePipelineReferences(ctx, tx, target); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	actorID, err := managementActorID(ctx, tx, input.OrganizationID, input.ActorID)
	if err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE request_approval_pipelines SET is_active = false
		WHERE organization_id = $1 AND id = $2`, input.OrganizationID, current.ID); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE request_approval_pipelines SET is_active = true
		WHERE organization_id = $1 AND id = $2`, input.OrganizationID, target.ID); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	data, _ := json.Marshal(map[string]any{
		"scope":                    domain.PipelineScopeName(target.AgentSlug),
		"agent_slug":               target.AgentSlug,
		"revision":                 target.Revision,
		"previous_active_revision": current.Revision,
	})
	if err := s.appendManagementAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: input.OrganizationID, EventType: "request_approval_pipeline.activated",
		ActorType: domain.ActorHuman, ActorID: actorID,
		SubjectType: "request_approval_pipeline", SubjectID: target.ID,
		Data: data, OccurredAt: input.Now,
	}); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	target.IsActive = true
	return target, nil
}

// ensureRequestApprovalPipelineScope gives a scope its first revision: an
// empty, active revision 1 that passes every request through. It runs in
// the transaction that brings the scope into being (the organization's
// activation, or the agent's creation) so a scope never exists without
// an active revision. A scope that already has a live revision is left
// alone. The membership is recorded as the
// creator and audited as the actor, exactly as a hand-written revision
// would be.
func (s *Store) ensureRequestApprovalPipelineScope(ctx context.Context, tx pgx.Tx, organizationID domain.ID, agentSlug domain.AgentSlug, membershipID domain.ID, now time.Time) error {
	if err := lockRequestApprovalPipeline(ctx, tx, organizationID, agentSlug); err != nil {
		return err
	}
	var latest, live int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(revision), 0), count(*) FILTER (WHERE archived_at IS NULL)
		FROM request_approval_pipelines
		WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, '')`,
		organizationID, agentSlug).Scan(&latest, &live); err != nil {
		return err
	}
	if live > 0 {
		return nil
	}
	actorID, err := managementActorID(ctx, tx, organizationID, membershipID)
	if err != nil {
		return err
	}
	revisionID, revisionNumber := domain.ID(uuid.NewString()), latest+1
	if _, err := tx.Exec(ctx, `INSERT INTO request_approval_pipelines
		(id, organization_id, agent_slug, revision, is_active, block_ids, default_escalation_path,
		 created_by_actor_id, created_at)
		VALUES ($1, $2, NULLIF($3, ''), $4, true, '{}', NULL, $5, $6)`,
		revisionID, organizationID, agentSlug, revisionNumber, membershipID, now); err != nil {
		return err
	}
	scopeName := domain.PipelineScopeName(agentSlug)
	created, _ := json.Marshal(map[string]any{
		"scope": scopeName, "agent_slug": agentSlug, "revision": revisionNumber, "block_count": 0,
	})
	if err := s.appendManagementAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: organizationID, EventType: "request_approval_pipeline.revision_created",
		ActorType: domain.ActorHuman, ActorID: actorID,
		SubjectType: "request_approval_pipeline", SubjectID: revisionID,
		Data: created, OccurredAt: now,
	}); err != nil {
		return err
	}
	activated, _ := json.Marshal(map[string]any{
		"scope": scopeName, "agent_slug": agentSlug, "revision": revisionNumber, "previous_active_revision": nil,
	})
	return s.appendManagementAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: organizationID, EventType: "request_approval_pipeline.activated",
		ActorType: domain.ActorHuman, ActorID: actorID,
		SubjectType: "request_approval_pipeline", SubjectID: revisionID,
		Data: activated, OccurredAt: now,
	})
}

// archiveRequestApprovalPipelineScope stamps every live revision of the
// scope and deactivates the active one in the same update, the one way
// a scope loses its active revision. It runs when the last live agent of
// the type is archived, inside that transaction and under the scope's
// lock, and reports whether anything was left to archive.
func (s *Store) archiveRequestApprovalPipelineScope(ctx context.Context, tx pgx.Tx, organizationID domain.ID, agentSlug domain.AgentSlug, actorID domain.ID, now time.Time) (bool, error) {
	active, hasActive, err := activeRequestApprovalPipelineRevision(ctx, tx, organizationID, agentSlug)
	if err != nil {
		return false, err
	}
	command, err := tx.Exec(ctx, `UPDATE request_approval_pipelines SET is_active = false, archived_at = $3
		WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, '') AND archived_at IS NULL`,
		organizationID, agentSlug, now)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role_assignments WHERE organization_id=$1 AND principal_kind='api_key' AND scope_kind='agent' AND scope_id=$2`, organizationID, string(agentSlug)); err != nil {
		return false, err
	}
	if command.RowsAffected() == 0 {
		return false, nil
	}

	var activeRevision any
	subjectID := domain.ID("")
	if hasActive {
		activeRevision, subjectID = active.Revision, active.ID
	}
	data, _ := json.Marshal(map[string]any{
		"scope":           domain.PipelineScopeName(agentSlug),
		"agent_slug":      agentSlug,
		"revision_count":  command.RowsAffected(),
		"active_revision": activeRevision,
	})
	return true, s.appendManagementAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: organizationID, EventType: "request_approval_pipeline.archived",
		ActorType: domain.ActorHuman, ActorID: actorID,
		SubjectType: "request_approval_pipeline", SubjectID: subjectID,
		Data: data, OccurredAt: now,
	})
}

// missingRequestApprovalPipelineScope explains a scope without an active
// revision: archived with the last agent of its type, or never created.
func missingRequestApprovalPipelineScope(ctx context.Context, tx pgx.Tx, organizationID domain.ID, agentSlug domain.AgentSlug) error {
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM request_approval_pipelines
		WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, ''))`,
		organizationID, agentSlug).Scan(&archived); err != nil {
		return err
	}
	if archived {
		return fmt.Errorf("request approval pipeline for agent type %q: %w", agentSlug, domain.ErrRequestApprovalPipelineArchived)
	}
	return fmt.Errorf("request approval pipeline scope does not exist: %w", domain.ErrNotFound)
}

// pipelineReferences is what a revision's immutable content points at
// outside itself: escalation path keys (block paths and the default) and
// webhook endpoint keys, each sorted and deduplicated.
type pipelineReferences struct {
	pathKeys     []string
	endpointKeys []string
}

func readPipelineReferences(ctx context.Context, tx pgx.Tx, revision domain.RequestApprovalPipelineRevision) (pipelineReferences, error) {
	rows, err := tx.Query(ctx, `SELECT block_type, COALESCE(config->>'escalation_path', ''), COALESCE(config->>'endpoint_key', '')
		FROM request_approval_blocks WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
		revision.OrganizationID, revision.BlockIDs)
	if err != nil {
		return pipelineReferences{}, err
	}
	defer rows.Close()
	paths, endpoints := map[string]struct{}{}, map[string]struct{}{}
	if revision.DefaultEscalationPath != "" {
		paths[revision.DefaultEscalationPath] = struct{}{}
	}
	for rows.Next() {
		var blockType, pathKey, endpointKey string
		if err := rows.Scan(&blockType, &pathKey, &endpointKey); err != nil {
			return pipelineReferences{}, err
		}
		if pathKey != "" {
			paths[pathKey] = struct{}{}
		}
		if blockType == "webhook" && endpointKey != "" {
			endpoints[endpointKey] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return pipelineReferences{}, err
	}
	return pipelineReferences{pathKeys: slices.Sorted(maps.Keys(paths)), endpointKeys: slices.Sorted(maps.Keys(endpoints))}, nil
}

// requirePipelineReferences is the activation guard against a revision
// whose references are gone: every escalation path it names and every
// webhook endpoint it posts to must have an active revision. It runs
// inside the activating transaction and takes each path's and each
// endpoint's advisory lock (after the pipeline's, in sorted key order)
// so a concurrent archive either sees this activation or waits for it.
func requirePipelineReferences(ctx context.Context, tx pgx.Tx, revision domain.RequestApprovalPipelineRevision) error {
	references, err := readPipelineReferences(ctx, tx, revision)
	if err != nil {
		return err
	}
	for _, pathKey := range references.pathKeys {
		if err := lockEscalationPath(ctx, tx, revision.OrganizationID, pathKey); err != nil {
			return err
		}
	}
	if len(references.pathKeys) > 0 {
		active, err := queryStrings(ctx, tx, `SELECT path_key FROM escalation_paths
			WHERE organization_id = $1 AND path_key = ANY($2) AND is_active`, revision.OrganizationID, references.pathKeys)
		if err != nil {
			return err
		}
		if inactive := missingStrings(references.pathKeys, active); len(inactive) > 0 {
			return domain.EscalationPathsInactiveError{PathKeys: inactive}
		}
	}
	for _, endpointKey := range references.endpointKeys {
		if err := lockWebhookEndpoint(ctx, tx, revision.OrganizationID, endpointKey); err != nil {
			return err
		}
	}
	if len(references.endpointKeys) > 0 {
		active, err := queryStrings(ctx, tx, `SELECT endpoint_key FROM webhook_endpoint_revisions
			WHERE organization_id = $1 AND endpoint_key = ANY($2) AND is_active`, revision.OrganizationID, references.endpointKeys)
		if err != nil {
			return err
		}
		if inactive := missingStrings(references.endpointKeys, active); len(inactive) > 0 {
			return domain.WebhookEndpointsInactiveError{EndpointKeys: inactive}
		}
	}
	return nil
}

func queryStrings(ctx context.Context, q querier, sql string, args ...any) ([]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// missingStrings returns the wanted values absent from found, in order.
func missingStrings(wanted, found []string) []string {
	present := make(map[string]struct{}, len(found))
	for _, value := range found {
		present[value] = struct{}{}
	}
	missing := make([]string, 0)
	for _, value := range wanted {
		if _, ok := present[value]; !ok {
			missing = append(missing, value)
		}
	}
	return missing
}

func scanRequestApprovalPipelineRevision(row rowScanner) (domain.RequestApprovalPipelineRevision, error) {
	var revision domain.RequestApprovalPipelineRevision
	var id, organizationID, agentSlug, membershipID string
	var blockIDs []string
	err := row.Scan(&id, &organizationID, &agentSlug, &revision.Revision, &revision.IsActive,
		&blockIDs, &revision.DefaultEscalationPath, &revision.DefaultAskAgentOwner, &membershipID, &revision.CreatedAt, &revision.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RequestApprovalPipelineRevision{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.RequestApprovalPipelineRevision{}, err
	}
	revision.ID = domain.ID(id)
	revision.OrganizationID = domain.ID(organizationID)
	revision.AgentSlug = domain.AgentSlug(agentSlug)
	revision.CreatedByActorID = domain.ID(membershipID)
	revision.BlockIDs = make([]domain.ID, len(blockIDs))
	for index, blockID := range blockIDs {
		revision.BlockIDs[index] = domain.ID(blockID)
	}
	return revision, nil
}

func scanRequestApprovalBlock(row rowScanner) (domain.RequestApprovalBlock, error) {
	var block domain.RequestApprovalBlock
	var id, organizationID, membershipID string
	err := row.Scan(&id, &organizationID, &block.BlockKey, &block.Name, &block.BlockType,
		&block.ConfigVersion, &block.Config, &block.TimeoutMS, &block.MaxAttempts, &block.ContentHash,
		&membershipID, &block.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RequestApprovalBlock{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.RequestApprovalBlock{}, err
	}
	block.ID = domain.ID(id)
	block.OrganizationID = domain.ID(organizationID)
	block.CreatedByActorID = domain.ID(membershipID)
	return block, nil
}

func activeRequestApprovalPipelineRevision(ctx context.Context, tx pgx.Tx, organizationID domain.ID, agentSlug domain.AgentSlug) (domain.RequestApprovalPipelineRevision, bool, error) {
	revision, err := scanRequestApprovalPipelineRevision(tx.QueryRow(ctx, requestApprovalPipelineSelect+
		`WHERE organization_id = $1 AND agent_slug IS NOT DISTINCT FROM NULLIF($2, '') AND is_active`, organizationID, agentSlug))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.RequestApprovalPipelineRevision{}, false, nil
	}
	return revision, err == nil, err
}

// lockRequestApprovalPipeline serializes scope mutations. The key must match
// the enforce_request_approval_pipeline_revision trigger: the organization
// scope locks the '@organization' sentinel, which no agent type can equal.
func lockRequestApprovalPipeline(ctx context.Context, tx pgx.Tx, organizationID domain.ID, agentSlug domain.AgentSlug) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text || ':' || COALESCE(NULLIF($2::text, ''), '@organization'), 0))`,
		organizationID, agentSlug)
	return err
}

func idStrings(ids []domain.ID) []string {
	values := make([]string, len(ids))
	for index, id := range ids {
		values[index] = string(id)
	}
	return values
}

// restoreRequestApprovalPipelineScope brings an archived agent's pipeline
// back with the agent: every archived revision loses its stamp and the
// latest one becomes active again, so the scope keeps its history and
// its "one active revision" invariant. It reports whether anything was
// archived to restore.
func (s *Store) restoreRequestApprovalPipelineScope(ctx context.Context, tx pgx.Tx, organizationID domain.ID, agentSlug domain.AgentSlug, actorID domain.ID, now time.Time) (bool, error) {
	var latestID string
	var latestRevision int64
	err := tx.QueryRow(ctx, `SELECT id::text, revision FROM request_approval_pipelines
		WHERE organization_id = $1 AND agent_slug = $2 AND archived_at IS NOT NULL
		ORDER BY revision DESC LIMIT 1`, organizationID, agentSlug).Scan(&latestID, &latestRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	command, err := tx.Exec(ctx, `UPDATE request_approval_pipelines SET archived_at = NULL, is_active = (id = $3::uuid)
		WHERE organization_id = $1 AND agent_slug = $2 AND archived_at IS NOT NULL`,
		organizationID, agentSlug, latestID)
	if err != nil {
		return false, err
	}
	data, _ := json.Marshal(map[string]any{
		"scope": domain.PipelineScopeName(agentSlug), "agent_slug": agentSlug,
		"revision_count": command.RowsAffected(), "active_revision": latestRevision,
	})
	return true, s.appendManagementAuditEvent(ctx, tx, domain.AuditEvent{
		OrganizationID: organizationID, EventType: "request_approval_pipeline.restored",
		ActorType: domain.ActorHuman, ActorID: actorID,
		SubjectType: "request_approval_pipeline", SubjectID: domain.ID(latestID),
		Data: data, OccurredAt: now,
	})
}
