-- The cel block type is gone ("if this, then that" is a branch whose
-- blocks end in an outcome block) and the always block is renamed the
-- outcome block. Stored revisions of the old types no longer compile.
--
-- This is a prototype migration, like 0005: it drops every existing
-- pipeline revision and block, clears the pipeline pins on routing
-- snapshots, deletes open suspensions, and gives every organization an
-- empty, active entry pipeline again. Deployments that carry pipeline
-- configuration worth keeping recreate it by hand.

-- 1. Clear the old pipeline state. The append-only triggers guard product
--    writes, not a schema migration, so they step aside for these
--    statements only.
ALTER TABLE approval_request_routing DISABLE TRIGGER approval_request_routing_append_only;
UPDATE approval_request_routing SET pipeline_revision_id = NULL, block_id = NULL
    WHERE pipeline_revision_id IS NOT NULL OR block_id IS NOT NULL;
ALTER TABLE approval_request_routing ENABLE TRIGGER approval_request_routing_append_only;

DELETE FROM approval_request_pipeline_suspensions;

ALTER TABLE request_approval_pipelines DISABLE TRIGGER request_approval_pipelines_immutable_revision;
DELETE FROM request_approval_pipelines;
ALTER TABLE request_approval_pipelines ENABLE TRIGGER request_approval_pipelines_immutable_revision;

ALTER TABLE request_approval_blocks DISABLE TRIGGER request_approval_blocks_append_only;
DELETE FROM request_approval_blocks;
ALTER TABLE request_approval_blocks ENABLE TRIGGER request_approval_blocks_append_only;

-- 2. Every organization gets its entry pipeline back: an empty, active
--    revision 1 recorded as created by the organization's earliest owner.
INSERT INTO management_actors (id, organization_id, membership_id)
SELECT m.id, m.organization_id, m.id
FROM memberships m
JOIN role_assignments ra ON ra.organization_id = m.organization_id AND ra.principal_kind = 'membership'
    AND ra.principal_id = m.id AND ra.role_id = '00000000-0000-4000-8000-000000000001' AND ra.scope_kind = 'organization'
ON CONFLICT DO NOTHING;

INSERT INTO request_approval_pipelines
    (id, organization_id, pipeline_key, name, revision, is_active, block_ids, default_escalation_path, created_by_actor_id, created_at)
SELECT gen_random_uuid(), o.id, 'entry', 'Entry pipeline', 1, true, '{}', NULL, owner.id, now()
FROM organizations o
JOIN LATERAL (
    SELECT m.id
    FROM memberships m
    JOIN role_assignments ra ON ra.organization_id = m.organization_id AND ra.principal_kind = 'membership'
        AND ra.principal_id = m.id AND ra.role_id = '00000000-0000-4000-8000-000000000001' AND ra.scope_kind = 'organization'
    WHERE m.organization_id = o.id
    ORDER BY m.created_at, m.id
    LIMIT 1
) owner ON true;
