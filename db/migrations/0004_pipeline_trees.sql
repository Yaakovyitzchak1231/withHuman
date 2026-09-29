-- Request approval pipelines become keyed, named objects, and the agent
-- scope goes away. Every organization has one entry pipeline (key
-- 'entry') that every request starts in; a delegate block in any pipeline
-- hands matching requests to another pipeline by key. Ownership of a
-- pipeline is a role assignment at the new 'pipeline' scope kind.
--
-- This is a prototype migration: it drops every existing pipeline
-- revision and block, clears the pipeline pins on routing snapshots, and
-- deletes open suspensions, because the old two-scope shape has no
-- meaning in the new model. Deployments that carry pipeline configuration
-- worth keeping recreate it by hand.

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

-- 2. Keyed pipelines: pipeline_key and name replace agent_slug. The name
--    lives on each revision, like an escalation path's, so renaming is a
--    revision and history keeps the name it was written under.
DROP INDEX IF EXISTS request_approval_pipelines_active_revision_idx;
DROP INDEX IF EXISTS request_approval_pipelines_live_scope_idx;
DROP INDEX IF EXISTS request_approval_pipelines_organization_scope_active_idx;
DROP INDEX IF EXISTS request_approval_pipelines_organization_scope_revision_idx;
ALTER TABLE request_approval_pipelines
    DROP CONSTRAINT request_approval_pipelines_scope_revision_key,
    DROP CONSTRAINT request_approval_pipelines_agent_slug_check,
    DROP COLUMN agent_slug,
    ADD COLUMN pipeline_key text NOT NULL,
    ADD COLUMN name text NOT NULL,
    ADD CONSTRAINT request_approval_pipelines_pipeline_key_check CHECK (pipeline_key ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    ADD CONSTRAINT request_approval_pipelines_name_check CHECK (btrim(name) <> ''),
    ADD CONSTRAINT request_approval_pipelines_key_revision_key UNIQUE (organization_id, pipeline_key, revision);
CREATE UNIQUE INDEX request_approval_pipelines_active_idx
    ON request_approval_pipelines (organization_id, pipeline_key) WHERE is_active;
CREATE INDEX request_approval_pipelines_live_key_idx
    ON request_approval_pipelines (organization_id, pipeline_key) WHERE archived_at IS NULL;

-- The monotonic revision trigger locks and counts per pipeline key. The
-- store's lockRequestApprovalPipeline derives the same key.
CREATE OR REPLACE FUNCTION public.enforce_request_approval_pipeline_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_revision bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.organization_id::text || ':' || NEW.pipeline_key, 0));
    SELECT COALESCE(max(revision), 0) + 1
      INTO next_revision
      FROM request_approval_pipelines
     WHERE organization_id = NEW.organization_id
       AND pipeline_key = NEW.pipeline_key;
    IF NEW.revision <> next_revision THEN
        RAISE EXCEPTION 'request approval pipeline revision must be %', next_revision;
    END IF;
    RETURN NEW;
END $$;

-- A live pipeline (one with an unarchived revision) always keeps an active
-- revision; only the archive stamp lets a pipeline end a transaction
-- without one.
CREATE OR REPLACE FUNCTION public.enforce_request_approval_pipeline_scope_active() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM request_approval_pipelines
         WHERE organization_id = OLD.organization_id
           AND pipeline_key = OLD.pipeline_key
           AND is_active)
       AND EXISTS (
        SELECT 1 FROM request_approval_pipelines
         WHERE organization_id = OLD.organization_id
           AND pipeline_key = OLD.pipeline_key
           AND archived_at IS NULL) THEN
        RAISE EXCEPTION 'request approval pipeline must keep an active revision';
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION public.protect_request_approval_pipeline_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'request_approval_pipelines records are append-only';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.organization_id IS DISTINCT FROM OLD.organization_id
       OR NEW.pipeline_key IS DISTINCT FROM OLD.pipeline_key
       OR NEW.name IS DISTINCT FROM OLD.name
       OR NEW.revision IS DISTINCT FROM OLD.revision
       OR NEW.block_ids IS DISTINCT FROM OLD.block_ids
       OR NEW.default_escalation_path IS DISTINCT FROM OLD.default_escalation_path
       OR NEW.created_by_actor_id IS DISTINCT FROM OLD.created_by_actor_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'request approval pipeline revision content is immutable';
    END IF;
    IF OLD.archived_at IS NOT NULL AND NEW.archived_at IS NOT NULL THEN
        RAISE EXCEPTION 'archived request approval pipeline revisions are frozen';
    END IF;
    IF NEW.archived_at IS NOT NULL AND NEW.is_active THEN
        RAISE EXCEPTION 'an archived request approval pipeline revision cannot be active';
    END IF;
    IF NEW.is_active IS NOT DISTINCT FROM OLD.is_active AND NEW.archived_at IS NOT DISTINCT FROM OLD.archived_at THEN
        RAISE EXCEPTION 'request approval pipeline updates must change active state or archive state';
    END IF;
    RETURN NEW;
END $$;

-- 3. A suspension pins every pipeline of the run, entered or not, one row
--    per key, so the worker flattens the same revisions into the same
--    positions when it resumes.
ALTER TABLE approval_request_pipeline_suspensions
    DROP CONSTRAINT approval_request_pipeline_suspensions_check,
    DROP CONSTRAINT approval_request_pipeline_sus_agent_type_pipeline_id_organ_fkey,
    DROP CONSTRAINT approval_request_pipeline_sus_organization_pipeline_id_org_fkey,
    DROP COLUMN organization_pipeline_id,
    DROP COLUMN agent_pipeline_id,
    ADD CONSTRAINT approval_request_pipeline_suspensions_request_id_organization_id_key UNIQUE (request_id, organization_id);

CREATE TABLE approval_request_pipeline_suspension_pins (
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    pipeline_key text NOT NULL,
    pipeline_id uuid NOT NULL,
    CONSTRAINT approval_request_pipeline_suspension_pins_pkey PRIMARY KEY (request_id, pipeline_key),
    CONSTRAINT approval_request_pipeline_suspension_pins_pipeline_key_check CHECK (pipeline_key ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    CONSTRAINT approval_request_pipeline_suspension_pins_request_id_organization_id_fkey
        FOREIGN KEY (request_id, organization_id) REFERENCES approval_request_pipeline_suspensions (request_id, organization_id) ON DELETE CASCADE,
    CONSTRAINT approval_request_pipeline_suspension_pins_pipeline_id_organization_id_fkey
        FOREIGN KEY (pipeline_id, organization_id) REFERENCES request_approval_pipelines (id, organization_id),
    CONSTRAINT approval_request_pipeline_suspension_pins_organization_id_fkey
        FOREIGN KEY (organization_id) REFERENCES organizations (id)
);
CREATE INDEX approval_request_pipeline_suspension_pins_pipeline_idx
    ON approval_request_pipeline_suspension_pins (organization_id, pipeline_id);

-- 4. The 'pipeline' scope kind: a role assignment or permission policy at
--    pipeline:<key> is how a team owns a delegated pipeline.
ALTER TABLE role_assignments
    DROP CONSTRAINT role_assignments_scope_kind_check,
    ADD CONSTRAINT role_assignments_scope_kind_check CHECK (scope_kind = ANY (ARRAY['organization'::text, 'team'::text, 'agent'::text, 'pipeline'::text])),
    ADD CONSTRAINT role_assignments_pipeline_scope_check CHECK ((scope_kind <> 'pipeline'::text) OR (scope_id ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text));
ALTER TABLE roles
    DROP CONSTRAINT roles_bindable_scope_kinds_check,
    ADD CONSTRAINT roles_bindable_scope_kinds_check CHECK ((bindable_scope_kinds <@ ARRAY['organization'::text, 'team'::text, 'agent'::text, 'pipeline'::text]) AND ('organization'::text = ANY (bindable_scope_kinds)));
ALTER TABLE directory_object_links
    DROP CONSTRAINT directory_object_links_scope_kind_check,
    ADD CONSTRAINT directory_object_links_scope_kind_check CHECK (scope_kind = ANY (ARRAY['organization'::text, 'team'::text, 'agent'::text, 'pipeline'::text]));
UPDATE roles SET bindable_scope_kinds = '{organization,team,agent,pipeline}'
    WHERE id = '00000000-0000-4000-8000-000000000002';

-- 5. Every existing organization gets its entry pipeline: an empty, active
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
