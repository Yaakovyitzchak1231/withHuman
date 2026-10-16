-- A revision that has never been active is a draft, and a draft can be
-- edited in place: it keeps its number, and saving it again replaces its
-- content. The moment a revision is first activated it is stamped with
-- activated_at and frozen for good, even after another revision replaces
-- it, because requests, routing snapshots and suspensions pin the
-- revisions that ran. The triggers own the stamp: an insert that is born
-- active and an update that activates set it, and nothing clears it.

ALTER TABLE public.request_approval_pipelines ADD COLUMN activated_at timestamp with time zone;
ALTER TABLE public.escalation_paths ADD COLUMN activated_at timestamp with time zone;
ALTER TABLE public.webhook_endpoint_revisions ADD COLUMN activated_at timestamp with time zone;
ALTER TABLE public.team_escalation_policies ADD COLUMN activated_at timestamp with time zone;

-- Nothing recorded which revisions were active before, so every revision
-- that exists now is treated as having been active and stays frozen. Only
-- revisions created from here on start as drafts. The protecting triggers
-- refuse an update that only sets the stamp, so they stand aside for the
-- backfill.
ALTER TABLE public.request_approval_pipelines DISABLE TRIGGER request_approval_pipelines_immutable_revision;
ALTER TABLE public.escalation_paths DISABLE TRIGGER escalation_paths_protect_revision;
ALTER TABLE public.webhook_endpoint_revisions DISABLE TRIGGER webhook_endpoint_revisions_protect_revision;
ALTER TABLE public.team_escalation_policies DISABLE TRIGGER team_escalation_policies_protect_revision;

UPDATE public.request_approval_pipelines SET activated_at = created_at;
UPDATE public.escalation_paths SET activated_at = created_at;
UPDATE public.webhook_endpoint_revisions SET activated_at = created_at;
UPDATE public.team_escalation_policies SET activated_at = created_at;

ALTER TABLE public.request_approval_pipelines ENABLE TRIGGER request_approval_pipelines_immutable_revision;
ALTER TABLE public.escalation_paths ENABLE TRIGGER escalation_paths_protect_revision;
ALTER TABLE public.webhook_endpoint_revisions ENABLE TRIGGER webhook_endpoint_revisions_protect_revision;
ALTER TABLE public.team_escalation_policies ENABLE TRIGGER team_escalation_policies_protect_revision;

ALTER TABLE public.request_approval_pipelines ADD CONSTRAINT request_approval_pipelines_activated_check
    CHECK (NOT is_active OR activated_at IS NOT NULL);
ALTER TABLE public.escalation_paths ADD CONSTRAINT escalation_paths_activated_check
    CHECK (NOT is_active OR activated_at IS NOT NULL);
ALTER TABLE public.webhook_endpoint_revisions ADD CONSTRAINT webhook_endpoint_revisions_activated_check
    CHECK (NOT is_active OR activated_at IS NOT NULL);
ALTER TABLE public.team_escalation_policies ADD CONSTRAINT team_escalation_policies_activated_check
    CHECK (NOT is_active OR activated_at IS NOT NULL);

-- Inserts: a revision born active (a pipeline's first, the seeded entry
-- pipeline) is activated at birth; any other starts as a draft.
CREATE FUNCTION public.stamp_revision_activation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.activated_at := CASE WHEN NEW.is_active THEN NEW.created_at END;
    RETURN NEW;
END $$;

CREATE TRIGGER request_approval_pipelines_stamp_activation BEFORE INSERT ON public.request_approval_pipelines
    FOR EACH ROW EXECUTE FUNCTION public.stamp_revision_activation();
CREATE TRIGGER escalation_paths_stamp_activation BEFORE INSERT ON public.escalation_paths
    FOR EACH ROW EXECUTE FUNCTION public.stamp_revision_activation();
CREATE TRIGGER webhook_endpoint_revisions_stamp_activation BEFORE INSERT ON public.webhook_endpoint_revisions
    FOR EACH ROW EXECUTE FUNCTION public.stamp_revision_activation();
CREATE TRIGGER team_escalation_policies_stamp_activation BEFORE INSERT ON public.team_escalation_policies
    FOR EACH ROW EXECUTE FUNCTION public.stamp_revision_activation();

-- Updates: identity never changes; content changes only on a draft that
-- stays an inactive draft in the same statement; otherwise the update
-- switches the active or archive state as before. The stamp is set by
-- the first activation and kept from then on.
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
       OR NEW.revision IS DISTINCT FROM OLD.revision
       OR NEW.created_by_actor_id IS DISTINCT FROM OLD.created_by_actor_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'request approval pipeline revision identity is immutable';
    END IF;
    IF NEW.name IS DISTINCT FROM OLD.name
       OR NEW.block_ids IS DISTINCT FROM OLD.block_ids
       OR NEW.default_escalation_path IS DISTINCT FROM OLD.default_escalation_path
       OR NEW.default_ask_agent_owner IS DISTINCT FROM OLD.default_ask_agent_owner THEN
        IF OLD.activated_at IS NOT NULL OR OLD.archived_at IS NOT NULL
           OR OLD.is_active OR NEW.is_active OR NEW.archived_at IS NOT NULL THEN
            RAISE EXCEPTION 'only a draft request approval pipeline revision may change its content';
        END IF;
        NEW.activated_at := NULL;
        RETURN NEW;
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
    NEW.activated_at := COALESCE(OLD.activated_at, CASE WHEN NEW.is_active THEN now() END);
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION public.protect_escalation_path_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'escalation path revisions are append-only';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.organization_id IS DISTINCT FROM OLD.organization_id
        OR NEW.path_key IS DISTINCT FROM OLD.path_key
        OR NEW.revision IS DISTINCT FROM OLD.revision
        OR NEW.created_by_actor_id IS DISTINCT FROM OLD.created_by_actor_id
        OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'escalation path revision identity is immutable';
    END IF;
    IF NEW.name IS DISTINCT FROM OLD.name
        OR NEW.document IS DISTINCT FROM OLD.document
        OR NEW.document_version IS DISTINCT FROM OLD.document_version THEN
        IF OLD.activated_at IS NOT NULL OR OLD.archived_at IS NOT NULL
            OR OLD.is_active OR NEW.is_active OR NEW.archived_at IS NOT NULL THEN
            RAISE EXCEPTION 'only a draft escalation path revision may change its content';
        END IF;
        NEW.activated_at := NULL;
        RETURN NEW;
    END IF;
    IF OLD.archived_at IS NOT NULL THEN
        RAISE EXCEPTION 'archived escalation path revisions are frozen';
    END IF;
    IF NEW.archived_at IS NOT NULL AND NEW.is_active THEN
        RAISE EXCEPTION 'an archived escalation path revision cannot be active';
    END IF;
    IF NEW.is_active IS NOT DISTINCT FROM OLD.is_active AND NEW.archived_at IS NULL THEN
        RAISE EXCEPTION 'escalation path revision update must change is_active or archive the revision';
    END IF;
    NEW.activated_at := COALESCE(OLD.activated_at, CASE WHEN NEW.is_active THEN now() END);
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.protect_team_escalation_policy_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'team escalation policy revisions are append-only';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.organization_id IS DISTINCT FROM OLD.organization_id
        OR NEW.team_id IS DISTINCT FROM OLD.team_id
        OR NEW.revision IS DISTINCT FROM OLD.revision
        OR NEW.created_by_actor_id IS DISTINCT FROM OLD.created_by_actor_id
        OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'team escalation policy revision identity is immutable';
    END IF;
    IF NEW.document IS DISTINCT FROM OLD.document
        OR NEW.document_version IS DISTINCT FROM OLD.document_version THEN
        IF OLD.activated_at IS NOT NULL OR OLD.is_active OR NEW.is_active THEN
            RAISE EXCEPTION 'only a draft team escalation policy revision may change its content';
        END IF;
        NEW.activated_at := NULL;
        RETURN NEW;
    END IF;
    IF NEW.is_active IS NOT DISTINCT FROM OLD.is_active THEN
        RAISE EXCEPTION 'team escalation policy revision update must change is_active';
    END IF;
    NEW.activated_at := COALESCE(OLD.activated_at, CASE WHEN NEW.is_active THEN now() END);
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.protect_webhook_endpoint_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'webhook endpoint revisions are append-only';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.organization_id IS DISTINCT FROM OLD.organization_id
        OR NEW.endpoint_key IS DISTINCT FROM OLD.endpoint_key
        OR NEW.revision IS DISTINCT FROM OLD.revision
        OR NEW.created_by_actor_id IS DISTINCT FROM OLD.created_by_actor_id
        OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'webhook endpoint revision identity is immutable';
    END IF;
    IF NEW.name IS DISTINCT FROM OLD.name OR NEW.url IS DISTINCT FROM OLD.url THEN
        IF OLD.activated_at IS NOT NULL OR OLD.is_active OR NEW.is_active THEN
            RAISE EXCEPTION 'only a draft webhook endpoint revision may change its content';
        END IF;
        NEW.activated_at := NULL;
        RETURN NEW;
    END IF;
    IF NEW.is_active IS NOT DISTINCT FROM OLD.is_active THEN
        RAISE EXCEPTION 'webhook endpoint revision update must change is_active';
    END IF;
    NEW.activated_at := COALESCE(OLD.activated_at, CASE WHEN NEW.is_active THEN now() END);
    RETURN NEW;
END;
$$;

-- A draft's blocks may now change, so their check runs on that update too.
DROP TRIGGER request_approval_pipelines_validate_blocks ON public.request_approval_pipelines;
CREATE CONSTRAINT TRIGGER request_approval_pipelines_validate_blocks
    AFTER INSERT OR UPDATE OF block_ids ON public.request_approval_pipelines
    DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION public.validate_request_approval_pipeline_blocks();
