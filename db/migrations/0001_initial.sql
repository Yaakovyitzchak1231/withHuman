-- withHuman core schema, squashed on 2026-09-24 at the repository split
-- (design_doc/15-repository-split.md): every earlier file folded into one,
-- the hosted edition's tables gone to its own chain. Every deployment is
-- recreated from this file; it is frozen like any shipped migration, so
-- changes go in a new numbered file.


-- pgcrypto (extension)
CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

-- check_role_assignment() (function)
CREATE FUNCTION public.check_role_assignment() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    principal_exists boolean;
    role_org uuid;
    role_built_in boolean;
    role_kinds text[];
BEGIN
    CASE NEW.principal_kind
        WHEN 'membership' THEN
            SELECT EXISTS (SELECT 1 FROM memberships WHERE id = NEW.principal_id AND organization_id = NEW.organization_id) INTO principal_exists;
        WHEN 'team' THEN
            SELECT EXISTS (SELECT 1 FROM teams WHERE archived_at IS NULL AND id = NEW.principal_id AND organization_id = NEW.organization_id) INTO principal_exists;
        WHEN 'api_key' THEN
            SELECT EXISTS (SELECT 1 FROM api_keys WHERE id=NEW.principal_id AND organization_id=NEW.organization_id AND kind='organization') INTO principal_exists;
        WHEN 'agent' THEN
            SELECT EXISTS (SELECT 1 FROM agents WHERE id = NEW.principal_id AND organization_id = NEW.organization_id) INTO principal_exists;
    END CASE;
    IF NOT principal_exists THEN
        RAISE EXCEPTION 'role assignment principal does not exist in this organization';
    END IF;
    IF NEW.scope_kind = 'team' AND NOT EXISTS (
        SELECT 1 FROM teams WHERE archived_at IS NULL AND id::text = NEW.scope_id AND organization_id = NEW.organization_id) THEN
        RAISE EXCEPTION 'role assignment scope team does not exist in this organization';
    END IF;
    SELECT organization_id, built_in, bindable_scope_kinds INTO role_org, role_built_in, role_kinds
      FROM roles WHERE id = NEW.role_id;
    IF NOT role_built_in AND role_org IS DISTINCT FROM NEW.organization_id THEN
        RAISE EXCEPTION 'role belongs to another organization';
    END IF;
    IF NOT (NEW.scope_kind = ANY (role_kinds)) THEN
        RAISE EXCEPTION 'role is not bindable at scope kind %', NEW.scope_kind;
    END IF;
    RETURN NEW;
END $$;

-- enforce_escalation_path_revision() (function)
CREATE FUNCTION public.enforce_escalation_path_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_revision bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(
        hashtextextended('escalation_path:' || NEW.organization_id::text || ':' || NEW.path_key, 0)
    );
    SELECT COALESCE(max(revision), 0) + 1
        INTO next_revision
        FROM escalation_paths
        WHERE organization_id = NEW.organization_id
          AND path_key = NEW.path_key;
    IF NEW.revision IS DISTINCT FROM next_revision THEN
        RAISE EXCEPTION 'escalation path revision must be %', next_revision;
    END IF;
    RETURN NEW;
END;
$$;

-- enforce_request_approval_pipeline_revision() (function)
CREATE FUNCTION public.enforce_request_approval_pipeline_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_revision bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(
        NEW.organization_id::text || ':' || COALESCE(NEW.agent_slug, '@organization'),
        0
    ));
    SELECT COALESCE(max(revision), 0) + 1
      INTO next_revision
      FROM request_approval_pipelines
     WHERE organization_id = NEW.organization_id
       AND agent_slug IS NOT DISTINCT FROM NEW.agent_slug;
    IF NEW.revision <> next_revision THEN
        RAISE EXCEPTION 'request approval pipeline revision must be %', next_revision;
    END IF;
    RETURN NEW;
END $$;

-- enforce_request_approval_pipeline_scope_active() (function)
CREATE FUNCTION public.enforce_request_approval_pipeline_scope_active() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM request_approval_pipelines
         WHERE organization_id = OLD.organization_id
           AND agent_slug IS NOT DISTINCT FROM OLD.agent_slug
           AND is_active)
       AND EXISTS (
        SELECT 1 FROM request_approval_pipelines
         WHERE organization_id = OLD.organization_id
           AND agent_slug IS NOT DISTINCT FROM OLD.agent_slug
           AND archived_at IS NULL) THEN
        RAISE EXCEPTION 'request approval pipeline scope must keep an active revision';
    END IF;
    RETURN NULL;
END $$;

-- enforce_team_escalation_policy_revision() (function)
CREATE FUNCTION public.enforce_team_escalation_policy_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_revision bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(
        hashtextextended('team_escalation_policy:' || NEW.organization_id::text || ':' || NEW.team_id::text, 0)
    );
    SELECT COALESCE(max(revision), 0) + 1
        INTO next_revision
        FROM team_escalation_policies
        WHERE organization_id = NEW.organization_id
          AND team_id = NEW.team_id;
    IF NEW.revision IS DISTINCT FROM next_revision THEN
        RAISE EXCEPTION 'team escalation policy revision must be %', next_revision;
    END IF;
    RETURN NEW;
END;
$$;

-- enforce_webhook_endpoint_revision() (function)
CREATE FUNCTION public.enforce_webhook_endpoint_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_revision bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(
        hashtextextended('webhook_endpoint:' || NEW.organization_id::text || ':' || NEW.endpoint_key, 0)
    );
    SELECT COALESCE(max(revision), 0) + 1
        INTO next_revision
        FROM webhook_endpoint_revisions
        WHERE organization_id = NEW.organization_id
          AND endpoint_key = NEW.endpoint_key;
    IF NEW.revision IS DISTINCT FROM next_revision THEN
        RAISE EXCEPTION 'webhook endpoint revision must be %', next_revision;
    END IF;
    RETURN NEW;
END;
$$;

-- identify_permission_policy() (function)
CREATE FUNCTION public.identify_permission_policy() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.permission_policy_id IS NULL AND NEW.principal_kind <> 'agent'
       AND NEW.role_id <> '00000000-0000-4000-8000-000000000001' THEN
        INSERT INTO permission_policies (id, organization_id) VALUES (NEW.id, NEW.organization_id);
        NEW.permission_policy_id := NEW.id;
    END IF;
    RETURN NEW;
END $$;

-- protect_escalation_path_revision() (function)
CREATE FUNCTION public.protect_escalation_path_revision() RETURNS trigger
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
        OR NEW.name IS DISTINCT FROM OLD.name
        OR NEW.document IS DISTINCT FROM OLD.document
        OR NEW.document_version IS DISTINCT FROM OLD.document_version
        OR NEW.created_by_actor_id IS DISTINCT FROM OLD.created_by_actor_id
        OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'only is_active and archived_at may change on an escalation path revision';
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
    RETURN NEW;
END;
$$;

-- protect_last_owner() (function)
CREATE FUNCTION public.protect_last_owner() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target_membership uuid;
    target_org uuid;
    active_owner_count bigint;
BEGIN
    target_membership := COALESCE(OLD.membership_id, NEW.membership_id);
    SELECT organization_id INTO target_org FROM memberships WHERE id = target_membership;
    SELECT count(*) INTO active_owner_count
      FROM membership_roles mr
      JOIN memberships m ON m.id = mr.membership_id
     WHERE m.organization_id = target_org AND m.status = 'active' AND mr.role = 'owner';
    IF TG_OP = 'DELETE' AND OLD.role = 'owner' AND active_owner_count = 0 THEN
        RAISE EXCEPTION 'organization must retain at least one active owner';
    END IF;
    RETURN COALESCE(NEW, OLD);
END $$;

-- protect_last_owner_assignment() (function)
CREATE FUNCTION public.protect_last_owner_assignment() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    active_owner_count bigint;
BEGIN
    IF OLD.principal_kind = 'membership' AND OLD.role_id = '00000000-0000-4000-8000-000000000001'
       AND OLD.scope_kind = 'organization' THEN
        SELECT count(*) INTO active_owner_count
          FROM role_assignments ra
          JOIN memberships m ON m.id = ra.principal_id
         WHERE ra.organization_id = OLD.organization_id AND ra.principal_kind = 'membership'
           AND ra.role_id = '00000000-0000-4000-8000-000000000001' AND ra.scope_kind = 'organization'
           AND m.status = 'active';
        IF active_owner_count = 0 THEN
            RAISE EXCEPTION 'organization must retain at least one active owner';
        END IF;
    END IF;
    RETURN COALESCE(NEW, OLD);
END $$;

-- protect_owner_membership_state() (function)
CREATE FUNCTION public.protect_owner_membership_state() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    owns_org boolean;
    other_active_owners bigint;
BEGIN
    IF OLD.status = 'active' AND NEW.status <> 'active' THEN
        SELECT EXISTS (
            SELECT 1 FROM role_assignments
             WHERE principal_kind = 'membership' AND principal_id = OLD.id
               AND role_id = '00000000-0000-4000-8000-000000000001' AND scope_kind = 'organization'
        ) INTO owns_org;
        IF owns_org THEN
            SELECT count(*) INTO other_active_owners
              FROM role_assignments ra
              JOIN memberships m ON m.id = ra.principal_id
             WHERE ra.organization_id = OLD.organization_id AND ra.principal_kind = 'membership'
               AND ra.role_id = '00000000-0000-4000-8000-000000000001' AND ra.scope_kind = 'organization'
               AND m.status = 'active' AND m.id <> OLD.id;
            IF other_active_owners = 0 THEN
                RAISE EXCEPTION 'organization must retain at least one active owner';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$;

-- protect_request_approval_pipeline_revision() (function)
CREATE FUNCTION public.protect_request_approval_pipeline_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'request_approval_pipelines records are append-only';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.organization_id IS DISTINCT FROM OLD.organization_id
       OR NEW.agent_slug IS DISTINCT FROM OLD.agent_slug
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

-- protect_team_escalation_policy_revision() (function)
CREATE FUNCTION public.protect_team_escalation_policy_revision() RETURNS trigger
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
        OR NEW.document IS DISTINCT FROM OLD.document
        OR NEW.document_version IS DISTINCT FROM OLD.document_version
        OR NEW.created_by_actor_id IS DISTINCT FROM OLD.created_by_actor_id
        OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'only is_active may change on a team escalation policy revision';
    END IF;
    IF NEW.is_active IS NOT DISTINCT FROM OLD.is_active THEN
        RAISE EXCEPTION 'team escalation policy revision update must change is_active';
    END IF;
    RETURN NEW;
END;
$$;

-- protect_terminal_approval() (function)
CREATE FUNCTION public.protect_terminal_approval() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.status IN ('approved', 'denied', 'expired', 'cancelled') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal approval requests are immutable';
    END IF;
    RETURN NEW;
END $$;

-- protect_webhook_endpoint_revision() (function)
CREATE FUNCTION public.protect_webhook_endpoint_revision() RETURNS trigger
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
        OR NEW.name IS DISTINCT FROM OLD.name
        OR NEW.url IS DISTINCT FROM OLD.url
        OR NEW.created_by_actor_id IS DISTINCT FROM OLD.created_by_actor_id
        OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'only is_active may change on a webhook endpoint revision';
    END IF;
    IF NEW.is_active IS NOT DISTINCT FROM OLD.is_active THEN
        RAISE EXCEPTION 'webhook endpoint revision update must change is_active';
    END IF;
    RETURN NEW;
END;
$$;

-- register_management_actor() (function)
CREATE FUNCTION public.register_management_actor() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
 IF TG_TABLE_NAME = 'memberships' THEN
  INSERT INTO management_actors (id,organization_id,membership_id) VALUES (NEW.id,NEW.organization_id,NEW.id);
 ELSIF NEW.kind = 'organization' THEN
  INSERT INTO management_actors (id,organization_id,api_key_id) VALUES (NEW.id,NEW.organization_id,NEW.id);
 END IF;
 RETURN NEW;
END $$;

-- reject_record_mutation() (function)
CREATE FUNCTION public.reject_record_mutation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION '% records are append-only', TG_TABLE_NAME;
END $$;

-- remove_team_api_key_assignments() (function)
CREATE FUNCTION public.remove_team_api_key_assignments() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
 DELETE FROM role_assignments WHERE organization_id=OLD.organization_id AND principal_kind='api_key' AND scope_kind='team' AND scope_id=OLD.id::text;
 RETURN OLD;
END $$;

-- request_approval_block_content_hash(text, text, text, integer, jsonb, integer, integer) (function)
CREATE FUNCTION public.request_approval_block_content_hash(block_key_value text, name_value text, block_type_value text, config_version_value integer, config_value jsonb, timeout_ms_value integer, max_attempts_value integer) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    AS $$
    SELECT encode(digest(jsonb_build_object(
        'block_key', block_key_value,
        'name', name_value,
        'block_type', block_type_value,
        'config_version', config_version_value,
        'config', config_value,
        'timeout_ms', timeout_ms_value,
        'max_attempts', max_attempts_value
    )::text, 'sha256'), 'hex')
$$;

-- set_approval_decision_expiry() (function)
CREATE FUNCTION public.set_approval_decision_expiry() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.expires_at := CASE WHEN NEW.status = 'approved' THEN NEW.decided_at + interval '5 minutes' ELSE NULL END;
    RETURN NEW;
END;
$$;

-- set_request_approval_block_content_hash() (function)
CREATE FUNCTION public.set_request_approval_block_content_hash() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    calculated_hash text;
BEGIN
    calculated_hash := request_approval_block_content_hash(
        NEW.block_key,
        NEW.name,
        NEW.block_type,
        NEW.config_version,
        NEW.config,
        NEW.timeout_ms,
        NEW.max_attempts
    );
    IF NEW.content_hash IS NOT NULL AND NEW.content_hash <> calculated_hash THEN
        RAISE EXCEPTION 'request approval block content hash does not match its content';
    END IF;
    NEW.content_hash := calculated_hash;
    RETURN NEW;
END $$;

-- validate_request_approval_pipeline_blocks() (function)
CREATE FUNCTION public.validate_request_approval_pipeline_blocks() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    referenced_count bigint;
    distinct_id_count bigint;
    distinct_key_count bigint;
BEGIN
    SELECT count(DISTINCT block_id)
      INTO distinct_id_count
      FROM unnest(NEW.block_ids) AS block_id;
    IF distinct_id_count <> cardinality(NEW.block_ids) THEN
        RAISE EXCEPTION 'request approval pipeline contains duplicate block IDs';
    END IF;

    SELECT count(*), count(DISTINCT block_key)
      INTO referenced_count, distinct_key_count
      FROM request_approval_blocks
     WHERE organization_id = NEW.organization_id
       AND id = ANY(NEW.block_ids);
    IF referenced_count <> cardinality(NEW.block_ids) THEN
        RAISE EXCEPTION 'request approval pipeline contains missing or cross-organization block references';
    END IF;
    IF distinct_key_count <> cardinality(NEW.block_ids) THEN
        RAISE EXCEPTION 'request approval pipeline contains duplicate block keys';
    END IF;
    RETURN NULL;
END $$;


SET default_tablespace = '';

SET default_table_access_method = heap;

-- membership_teams (table)
CREATE TABLE public.membership_teams (
    membership_id uuid NOT NULL,
    team_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    source_key text DEFAULT ''::text NOT NULL
);

-- teams (table)
CREATE TABLE public.teams (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone
);

-- active_membership_teams (view)
CREATE VIEW public.active_membership_teams AS
 SELECT mt.membership_id,
    mt.team_id,
    mt.organization_id,
    mt.source,
    mt.source_key
   FROM (public.membership_teams mt
     JOIN public.teams t ON (((t.id = mt.team_id) AND (t.organization_id = mt.organization_id))))
  WHERE (t.archived_at IS NULL);

-- agent_credentials (table)
CREATE TABLE public.agent_credentials (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    agent_instance_id uuid NOT NULL,
    secret_hash bytea NOT NULL,
    issued_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    last_used_at timestamp with time zone,
    revoked_at timestamp with time zone,
    rotated_from_id uuid
);

-- agent_enrollments (table)
CREATE TABLE public.agent_enrollments (
    id uuid NOT NULL,
    enrollment_code_hash bytea NOT NULL,
    user_code text NOT NULL,
    instance_name text NOT NULL,
    instance_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    scopes text[] DEFAULT '{}'::text[] NOT NULL,
    organization_id uuid,
    membership_id uuid,
    agent_id uuid,
    agent_instance_id uuid,
    status text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    authorized_at timestamp with time zone,
    consumed_at timestamp with time zone,
    runtime text NOT NULL,
    requested_agent_slug text,
    CONSTRAINT agent_enrollments_scopes_check CHECK ((scopes <@ ARRAY['request.create'::text, 'request.read'::text])),
    CONSTRAINT agent_enrollments_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'authorized'::text, 'consumed'::text])))
);

-- agent_instances (table)
CREATE TABLE public.agent_instances (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    name text NOT NULL,
    status text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone NOT NULL,
    CONSTRAINT agent_instances_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
);

-- agent_provisioners (table)
CREATE TABLE public.agent_provisioners (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    created_by_actor_id uuid NOT NULL,
    secret_hash bytea NOT NULL,
    issued_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    last_used_at timestamp with time zone,
    revoked_at timestamp with time zone
);

-- agents (table)
CREATE TABLE public.agents (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    created_by_actor_id uuid NOT NULL,
    name text NOT NULL,
    status text NOT NULL,
    scopes text[] DEFAULT '{}'::text[] NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    archived_at timestamp with time zone,
    slug text NOT NULL,
    CONSTRAINT agents_archived_status_check CHECK (((status = 'archived'::text) = (archived_at IS NOT NULL))),
    CONSTRAINT agents_scopes_check CHECK ((scopes <@ ARRAY['request.create'::text, 'request.read'::text])),
    CONSTRAINT agents_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text)),
    CONSTRAINT agents_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'archived'::text])))
);

-- api_key_creations (table)
CREATE TABLE public.api_key_creations (
    organization_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    fingerprint bytea NOT NULL,
    api_key_id uuid NOT NULL,
    kind text DEFAULT 'organization'::text NOT NULL,
    CONSTRAINT api_key_creations_kind_check CHECK ((kind = 'organization'::text))
);

-- api_keys (table)
CREATE TABLE public.api_keys (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    kind text NOT NULL,
    membership_id uuid,
    name text NOT NULL,
    permissions text[],
    secret_hash bytea NOT NULL,
    assurance_at_issue text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone,
    last_used_at timestamp with time zone,
    revoked_at timestamp with time zone,
    revoked_by_membership_id uuid,
    revocation_reason text,
    created_by_membership_id uuid,
    oauth_client_id uuid,
    CONSTRAINT api_keys_check CHECK (((kind = 'personal'::text) = (membership_id IS NOT NULL))),
    CONSTRAINT api_keys_check1 CHECK (((revoked_at IS NULL) = (revocation_reason IS NULL))),
    CONSTRAINT api_keys_kind_check CHECK ((kind = ANY (ARRAY['personal'::text, 'organization'::text]))),
    CONSTRAINT api_keys_organization_shape CHECK (((kind <> 'organization'::text) OR ((created_by_membership_id IS NOT NULL) AND (permissions IS NULL)))),
    CONSTRAINT api_keys_revocation_reason_check CHECK ((revocation_reason = ANY (ARRAY['manual'::text, 'membership_deprovisioned'::text])))
);

-- approval_request_claims (table)
CREATE TABLE public.approval_request_claims (
    id uuid NOT NULL,
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    claimed_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    released_at timestamp with time zone,
    release_reason text,
    CONSTRAINT approval_request_claims_check CHECK (((released_at IS NULL) = (release_reason IS NULL))),
    CONSTRAINT approval_request_claims_check1 CHECK ((expires_at > claimed_at)),
    CONSTRAINT approval_request_claims_release_reason_check CHECK ((release_reason = ANY (ARRAY['released'::text, 'lapsed'::text, 'decided'::text, 'expired'::text, 'cancelled'::text])))
);

-- approval_request_escalation_state (table)
CREATE TABLE public.approval_request_escalation_state (
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    sequence bigint NOT NULL,
    phase text NOT NULL,
    urgency text NOT NULL,
    current_node_id uuid,
    level_ordinal integer DEFAULT 0 NOT NULL,
    level_count integer DEFAULT 0 NOT NULL,
    repeat integer DEFAULT 0 NOT NULL,
    next_at timestamp with time zone,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT approval_request_escalation_state_level_count_check CHECK ((level_count >= 0)),
    CONSTRAINT approval_request_escalation_state_level_ordinal_check CHECK ((level_ordinal >= 0)),
    CONSTRAINT approval_request_escalation_state_phase_check CHECK ((phase = ANY (ARRAY['level'::text, 'deferred'::text, 'repeat_wait'::text, 'paused'::text, 'exhausted'::text]))),
    CONSTRAINT approval_request_escalation_state_repeat_check CHECK ((repeat >= 0)),
    CONSTRAINT approval_request_escalation_state_sequence_check CHECK ((sequence >= 0)),
    CONSTRAINT approval_request_escalation_state_urgency_check CHECK ((urgency = ANY (ARRAY['standard'::text, 'interrupt'::text])))
);

-- approval_request_escalation_team_state (table)
CREATE TABLE public.approval_request_escalation_team_state (
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    team_id uuid NOT NULL,
    phase text NOT NULL,
    current_node_id uuid,
    level_ordinal integer DEFAULT 0 NOT NULL,
    level_count integer DEFAULT 0 NOT NULL,
    repeat integer DEFAULT 0 NOT NULL,
    next_at timestamp with time zone,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT approval_request_escalation_team_state_level_count_check CHECK ((level_count >= 0)),
    CONSTRAINT approval_request_escalation_team_state_level_ordinal_check CHECK ((level_ordinal >= 0)),
    CONSTRAINT approval_request_escalation_team_state_phase_check CHECK ((phase = ANY (ARRAY['level'::text, 'deferred'::text, 'repeat_wait'::text, 'paused'::text, 'exhausted'::text]))),
    CONSTRAINT approval_request_escalation_team_state_repeat_check CHECK ((repeat >= 0))
);

-- approval_request_notifications (table)
CREATE TABLE public.approval_request_notifications (
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    notified_at timestamp with time zone NOT NULL
);

-- approval_request_pipeline_suspensions (table)
CREATE TABLE public.approval_request_pipeline_suspensions (
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    pipeline_evaluation_id uuid NOT NULL,
    organization_pipeline_id uuid,
    agent_pipeline_id uuid,
    resume_position integer NOT NULL,
    previous_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    attempted_block_count integer NOT NULL,
    total_attempts integer NOT NULL,
    total_cost bigint NOT NULL,
    started_at timestamp with time zone NOT NULL,
    suspended_at timestamp with time zone NOT NULL,
    resumed_at timestamp with time zone,
    CONSTRAINT approval_request_pipeline_suspensio_attempted_block_count_check CHECK ((attempted_block_count >= 0)),
    CONSTRAINT approval_request_pipeline_suspensions_check CHECK (((organization_pipeline_id IS NOT NULL) OR (agent_pipeline_id IS NOT NULL))),
    CONSTRAINT approval_request_pipeline_suspensions_previous_metadata_check CHECK ((jsonb_typeof(previous_metadata) = 'object'::text)),
    CONSTRAINT approval_request_pipeline_suspensions_resume_position_check CHECK ((resume_position >= 0)),
    CONSTRAINT approval_request_pipeline_suspensions_total_attempts_check CHECK ((total_attempts >= 0)),
    CONSTRAINT approval_request_pipeline_suspensions_total_cost_check CHECK ((total_cost >= 0))
);

-- approval_request_reviewers (table)
CREATE TABLE public.approval_request_reviewers (
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    added_by_membership_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

-- approval_request_routing (table)
CREATE TABLE public.approval_request_routing (
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    outcome text NOT NULL,
    reason text NOT NULL,
    escalation_path_key text,
    escalation_path_revision_id uuid,
    pipeline_revision_id uuid,
    block_id uuid,
    urgency text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    facts jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT approval_request_routing_check CHECK (((outcome = 'escalated'::text) = (escalation_path_revision_id IS NOT NULL))),
    CONSTRAINT approval_request_routing_check1 CHECK (((outcome = 'escalated'::text) = (escalation_path_key IS NOT NULL))),
    CONSTRAINT approval_request_routing_check2 CHECK (((block_id IS NULL) OR (reason = 'block_escalation'::text))),
    CONSTRAINT approval_request_routing_escalation_path_key_check CHECK (((escalation_path_key IS NULL) OR (escalation_path_key ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text))),
    CONSTRAINT approval_request_routing_facts_check CHECK ((jsonb_typeof(facts) = 'object'::text)),
    CONSTRAINT approval_request_routing_outcome_check CHECK ((outcome = ANY (ARRAY['escalated'::text, 'default_queue'::text]))),
    CONSTRAINT approval_request_routing_reason_check CHECK ((reason = ANY (ARRAY['block_escalation'::text, 'pipeline_default'::text, 'no_escalation'::text, 'path_inactive'::text, 'evaluation_error'::text]))),
    CONSTRAINT approval_request_routing_urgency_check CHECK ((urgency = ANY (ARRAY['standard'::text, 'interrupt'::text])))
);

-- approval_request_routing_team_policies (table)
CREATE TABLE public.approval_request_routing_team_policies (
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    team_id uuid NOT NULL,
    policy_revision_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

-- approval_requests (table)
CREATE TABLE public.approval_requests (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    agent_instance_id uuid NOT NULL,
    agent_name text NOT NULL,
    agent_instance_name text NOT NULL,
    agent_slug text NOT NULL,
    tool text NOT NULL,
    arguments jsonb NOT NULL,
    agent_reasoning text DEFAULT ''::text NOT NULL,
    context jsonb,
    idempotency_key text NOT NULL,
    semantic_hash text NOT NULL,
    status text NOT NULL,
    deadline_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    timeout text NOT NULL,
    server text DEFAULT ''::text NOT NULL,
    CONSTRAINT approval_requests_agent_slug_check CHECK ((agent_slug ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text)),
    CONSTRAINT approval_requests_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'denied'::text, 'expired'::text, 'cancelled'::text]))),
    CONSTRAINT approval_requests_timeout_nonempty CHECK ((length(timeout) > 0))
);

-- contact_methods (table)
CREATE TABLE public.contact_methods (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    value text NOT NULL,
    verified_at timestamp with time zone,
    verification_code_hash bytea,
    verification_expires_at timestamp with time zone,
    verification_attempts bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    organization_id uuid,
    label text DEFAULT ''::text NOT NULL,
    slack_installation_id uuid,
    CONSTRAINT contact_methods_check CHECK (((verification_code_hash IS NULL) = (verification_expires_at IS NULL))),
    CONSTRAINT contact_methods_check1 CHECK (((verified_at IS NULL) OR (verification_code_hash IS NULL))),
    CONSTRAINT contact_methods_scope_check CHECK (((kind = ANY (ARRAY['slack'::text, 'discord'::text])) = (organization_id IS NOT NULL))),
    CONSTRAINT contact_methods_verification_attempts_check CHECK ((verification_attempts >= 0))
);

-- decision_operations (table)
CREATE TABLE public.decision_operations (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    request_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    intent_hash text NOT NULL,
    status text NOT NULL,
    error_code text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    decision_id uuid,
    created_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT decision_operations_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'applied'::text, 'rejected'::text])))
);

-- decisions (table)
CREATE TABLE public.decisions (
    id uuid NOT NULL,
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    membership_id uuid,
    source text NOT NULL,
    status text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    channel text NOT NULL,
    reviewer_display_name text DEFAULT ''::text NOT NULL,
    reviewer_email text DEFAULT ''::text NOT NULL,
    auth_method text DEFAULT ''::text NOT NULL,
    auth_assurance text DEFAULT ''::text NOT NULL,
    authenticated_at timestamp with time zone,
    session_id uuid,
    credential_id uuid,
    idempotency_key text NOT NULL,
    decided_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone,
    CONSTRAINT decisions_approval_expiry CHECK ((((status = 'approved'::text) AND (expires_at IS NOT NULL) AND (expires_at = (decided_at + '00:05:00'::interval))) OR ((status <> 'approved'::text) AND (expires_at IS NULL)))),
    CONSTRAINT decisions_source_check CHECK ((source = ANY (ARRAY['human'::text, 'pipeline'::text, 'system'::text, 'agent'::text]))),
    CONSTRAINT decisions_status_check CHECK ((status = ANY (ARRAY['approved'::text, 'denied'::text, 'expired'::text, 'cancelled'::text])))
);

-- directory_connections (table)
CREATE TABLE public.directory_connections (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    adapter_key text NOT NULL,
    provider_resource_id text NOT NULL,
    state text NOT NULL,
    safe_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    reconciliation_status text DEFAULT 'pending'::text NOT NULL,
    last_successful_sync_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- directory_object_links (table)
CREATE TABLE public.directory_object_links (
    directory_connection_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    provider_object_type text NOT NULL,
    provider_object_id text NOT NULL,
    membership_id uuid,
    team_id uuid,
    provider_version text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    role_id uuid,
    scope_kind text,
    scope_id text,
    CONSTRAINT directory_object_links_check CHECK ((((provider_object_type = 'user'::text) AND (membership_id IS NOT NULL) AND (team_id IS NULL)) OR ((provider_object_type = 'group'::text) AND (membership_id IS NULL) AND (team_id IS NOT NULL)))),
    CONSTRAINT directory_object_links_grant_check CHECK ((((role_id IS NULL) AND (scope_kind IS NULL) AND (scope_id IS NULL)) OR ((provider_object_type = 'group'::text) AND (role_id IS NOT NULL) AND (scope_kind IS NOT NULL) AND (scope_id IS NOT NULL)))),
    CONSTRAINT directory_object_links_provider_object_type_check CHECK ((provider_object_type = ANY (ARRAY['user'::text, 'group'::text]))),
    CONSTRAINT directory_object_links_scope_kind_check CHECK ((scope_kind = ANY (ARRAY['organization'::text, 'team'::text, 'agent'::text])))
);

-- discord_connections (table)
CREATE TABLE public.discord_connections (
    organization_id uuid NOT NULL,
    guild_id text NOT NULL,
    guild_name text NOT NULL,
    installed_by_membership_id uuid NOT NULL,
    installed_at timestamp with time zone NOT NULL
);

-- discord_oauth_states (table)
CREATE TABLE public.discord_oauth_states (
    state_hash bytea NOT NULL,
    purpose text NOT NULL,
    organization_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    user_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT discord_oauth_states_purpose_check CHECK ((purpose = ANY (ARRAY['install'::text, 'link'::text])))
);

-- escalation_paths (table)
CREATE TABLE public.escalation_paths (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    path_key text NOT NULL,
    revision bigint NOT NULL,
    is_active boolean DEFAULT false NOT NULL,
    name text NOT NULL,
    document jsonb NOT NULL,
    document_version integer NOT NULL,
    created_by_actor_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    archived_at timestamp with time zone,
    CONSTRAINT escalation_paths_document_check CHECK ((jsonb_typeof(document) = 'object'::text)),
    CONSTRAINT escalation_paths_document_version_check CHECK ((document_version > 0)),
    CONSTRAINT escalation_paths_name_check CHECK ((btrim(name) <> ''::text)),
    CONSTRAINT escalation_paths_path_key_check CHECK ((path_key ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text)),
    CONSTRAINT escalation_paths_revision_check CHECK ((revision > 0))
);

-- external_identities (table)
CREATE TABLE public.external_identities (
    user_id uuid NOT NULL,
    provider_key text NOT NULL,
    issuer text NOT NULL,
    subject text NOT NULL,
    last_observed_email text DEFAULT ''::text NOT NULL,
    email_verified boolean DEFAULT false NOT NULL,
    linked_at timestamp with time zone NOT NULL,
    last_authenticated_at timestamp with time zone NOT NULL
);

-- invitations (table)
CREATE TABLE public.invitations (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    email text NOT NULL,
    roles text[] DEFAULT '{}'::text[],
    invited_by_membership_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    status text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    accepted_membership_id uuid,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    grants jsonb DEFAULT '[]'::jsonb NOT NULL,
    CONSTRAINT invitations_grants_check CHECK ((jsonb_typeof(grants) = 'array'::text)),
    CONSTRAINT invitations_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'accepted'::text, 'revoked'::text, 'expired'::text])))
);

-- local_credentials (table)
CREATE TABLE public.local_credentials (
    user_id uuid NOT NULL,
    password_hash text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

-- login_tickets (table)
CREATE TABLE public.login_tickets (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    secret_hash bytea NOT NULL,
    issued_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    auth_method text NOT NULL,
    assurance text NOT NULL,
    client text DEFAULT 'web'::text NOT NULL,
    client_context jsonb DEFAULT '{}'::jsonb NOT NULL
);

-- management_actors (table)
CREATE TABLE public.management_actors (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    membership_id uuid,
    api_key_id uuid,
    api_key_kind text DEFAULT 'organization'::text NOT NULL,
    CONSTRAINT management_actors_api_key_kind_check CHECK ((api_key_kind = 'organization'::text)),
    CONSTRAINT management_actors_check CHECK ((((membership_id IS NOT NULL) AND (api_key_id IS NULL) AND (id = membership_id)) OR ((membership_id IS NULL) AND (api_key_id IS NOT NULL) AND (id = api_key_id))))
);

-- membership_roles (table)
CREATE TABLE public.membership_roles (
    membership_id uuid NOT NULL,
    role text NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    source_key text DEFAULT ''::text NOT NULL,
    CONSTRAINT membership_roles_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'admin'::text, 'reviewer'::text, 'auditor'::text])))
);

-- memberships (table)
CREATE TABLE public.memberships (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    status text NOT NULL,
    admission_source text NOT NULL,
    directory_managed boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT memberships_admission_source_check CHECK ((admission_source = ANY (ARRAY['manual'::text, 'invite'::text, 'signup'::text, 'sso_jit'::text, 'directory'::text]))),
    CONSTRAINT memberships_status_check CHECK ((status = ANY (ARRAY['active'::text, 'suspended'::text, 'deprovisioned'::text])))
);

-- notification_rules (table)
CREATE TABLE public.notification_rules (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    urgency text NOT NULL,
    "position" integer NOT NULL,
    delay_seconds integer NOT NULL,
    contact_method_id uuid,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT notification_rules_delay_seconds_check CHECK (((delay_seconds >= 0) AND (delay_seconds <= 86400))),
    CONSTRAINT notification_rules_position_check CHECK (("position" >= 0)),
    CONSTRAINT notification_rules_urgency_check CHECK ((urgency = ANY (ARRAY['standard'::text, 'interrupt'::text])))
);

-- oauth_authorization_codes (table)
CREATE TABLE public.oauth_authorization_codes (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    api_key_id uuid NOT NULL,
    code_hash bytea NOT NULL,
    sealed_token bytea NOT NULL,
    redirect_uri text NOT NULL,
    code_challenge text NOT NULL,
    scope text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone
);

-- oauth_clients (table)
CREATE TABLE public.oauth_clients (
    id uuid NOT NULL,
    name text NOT NULL,
    redirect_uris text[] NOT NULL,
    client_uri text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone NOT NULL
);

-- onboarding_sessions (table)
CREATE TABLE public.onboarding_sessions (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    status text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    setup_code_hash bytea,
    setup_code_expires_at timestamp with time zone NOT NULL,
    credential_secret_hash bytea,
    machine jsonb DEFAULT '{}'::jsonb NOT NULL,
    detections jsonb DEFAULT '[]'::jsonb NOT NULL,
    selected_runtimes text[] DEFAULT '{}'::text[] NOT NULL,
    discovered_tools jsonb DEFAULT '{}'::jsonb NOT NULL,
    discovery_errors jsonb DEFAULT '{}'::jsonb NOT NULL,
    generated jsonb DEFAULT '{}'::jsonb NOT NULL,
    enrollments jsonb DEFAULT '[]'::jsonb NOT NULL,
    install_results jsonb DEFAULT '[]'::jsonb NOT NULL,
    test_commands jsonb DEFAULT '{}'::jsonb NOT NULL,
    last_error jsonb,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    test_request_id uuid,
    tested_at timestamp with time zone,
    channels_set_at timestamp with time zone,
    rules_set_at timestamp with time zone,
    assistant_messages jsonb DEFAULT '[]'::jsonb NOT NULL,
    assistant_context jsonb DEFAULT '[]'::jsonb NOT NULL,
    CONSTRAINT onboarding_sessions_check CHECK (((status = ANY (ARRAY['completed'::text, 'skipped'::text])) = (completed_at IS NOT NULL))),
    CONSTRAINT onboarding_sessions_status_check CHECK ((status = ANY (ARRAY['awaiting_cli'::text, 'connected'::text, 'detected'::text, 'agents_selected'::text, 'tools_discovered'::text, 'provisioning'::text, 'enrolled'::text, 'installed'::text, 'tested'::text, 'completed'::text, 'skipped'::text])))
);

-- organization_policies (table)
CREATE TABLE public.organization_policies (
    organization_id uuid NOT NULL,
    admission_mode text DEFAULT 'invite_only'::text NOT NULL,
    mfa_mode text DEFAULT 'optional'::text NOT NULL,
    allow_self_approval boolean DEFAULT false NOT NULL,
    approval_reauth_window_seconds bigint DEFAULT 86400 NOT NULL,
    session_max_age_seconds bigint DEFAULT 86400 NOT NULL,
    adapter_policy jsonb DEFAULT '{"tools": []}'::jsonb NOT NULL,
    claim_retrigger_seconds bigint DEFAULT 900 NOT NULL,
    unrouted_requests text DEFAULT 'any_reviewer'::text NOT NULL,
    request_visibility text DEFAULT 'all_readers'::text NOT NULL,
    personal_api_keys_allowed boolean DEFAULT true NOT NULL,
    organization_api_keys_allowed boolean DEFAULT true NOT NULL,
    CONSTRAINT organization_policies_admission_mode_check CHECK ((admission_mode = ANY (ARRAY['invite_only'::text, 'jit'::text, 'directory_only'::text]))),
    CONSTRAINT organization_policies_approval_reauth_window_seconds_check CHECK ((approval_reauth_window_seconds > 0)),
    CONSTRAINT organization_policies_claim_retrigger_seconds_check CHECK (((claim_retrigger_seconds > 0) AND (claim_retrigger_seconds <= 86400))),
    CONSTRAINT organization_policies_mfa_mode_check CHECK ((mfa_mode = ANY (ARRAY['optional'::text, 'reviewers_required'::text, 'everyone_required'::text]))),
    CONSTRAINT organization_policies_request_visibility_check CHECK ((request_visibility = ANY (ARRAY['all_readers'::text, 'routed_only'::text]))),
    CONSTRAINT organization_policies_session_max_age_seconds_check CHECK ((session_max_age_seconds > 0)),
    CONSTRAINT organization_policies_unrouted_requests_check CHECK ((unrouted_requests = ANY (ARRAY['any_reviewer'::text, 'break_glass_only'::text])))
);

-- organizations (table)
CREATE TABLE public.organizations (
    id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    status text NOT NULL,
    status_reason text DEFAULT ''::text NOT NULL,
    suspended_at timestamp with time zone,
    deletion_scheduled_for timestamp with time zone,
    onboarding_completed_at timestamp with time zone,
    CONSTRAINT organizations_status_check CHECK ((status = ANY (ARRAY['active'::text, 'suspended'::text, 'pending_deletion'::text])))
);

-- outbox_events (table)
CREATE TABLE public.outbox_events (
    sequence bigint NOT NULL,
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    topic text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    published_at timestamp with time zone
);

-- outbox_events_sequence_seq (sequence)
ALTER TABLE public.outbox_events ALTER COLUMN sequence ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.outbox_events_sequence_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

-- password_resets (table)
CREATE TABLE public.password_resets (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    secret_hash bytea NOT NULL,
    issued_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone
);

-- permission_policies (table)
CREATE TABLE public.permission_policies (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    CONSTRAINT permission_policies_revision_check CHECK ((revision > 0))
);

-- permission_policy_commands (table)
CREATE TABLE public.permission_policy_commands (
    organization_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    intent_hash text NOT NULL,
    result jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL
);

-- request_approval_blocks (table)
CREATE TABLE public.request_approval_blocks (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    block_key text NOT NULL,
    name text NOT NULL,
    block_type text NOT NULL,
    config_version integer NOT NULL,
    config jsonb NOT NULL,
    timeout_ms integer NOT NULL,
    max_attempts integer NOT NULL,
    content_hash text NOT NULL,
    created_by_actor_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT request_approval_blocks_block_key_check CHECK ((block_key ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text)),
    CONSTRAINT request_approval_blocks_block_type_check CHECK ((block_type ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text)),
    CONSTRAINT request_approval_blocks_config_check CHECK ((jsonb_typeof(config) = 'object'::text)),
    CONSTRAINT request_approval_blocks_config_version_check CHECK ((config_version > 0)),
    CONSTRAINT request_approval_blocks_content_hash_check CHECK ((content_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT request_approval_blocks_max_attempts_check CHECK (((max_attempts >= 1) AND (max_attempts <= 10))),
    CONSTRAINT request_approval_blocks_name_check CHECK ((btrim(name) <> ''::text)),
    CONSTRAINT request_approval_blocks_timeout_ms_check CHECK (((timeout_ms >= 1) AND (timeout_ms <= 60000)))
);

-- request_approval_pipelines (table)
CREATE TABLE public.request_approval_pipelines (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    agent_slug text,
    revision bigint NOT NULL,
    is_active boolean DEFAULT false NOT NULL,
    block_ids uuid[] NOT NULL,
    created_by_actor_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    default_escalation_path text,
    archived_at timestamp with time zone,
    CONSTRAINT request_approval_pipelines_agent_slug_check CHECK ((agent_slug ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text)),
    CONSTRAINT request_approval_pipelines_default_escalation_path_check CHECK (((default_escalation_path IS NULL) OR (default_escalation_path ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text))),
    CONSTRAINT request_approval_pipelines_revision_check CHECK ((revision > 0))
);

-- role_assignments (table)
CREATE TABLE public.role_assignments (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    principal_kind text NOT NULL,
    principal_id uuid NOT NULL,
    role_id uuid NOT NULL,
    scope_kind text NOT NULL,
    scope_id text DEFAULT ''::text NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    source_key text DEFAULT ''::text NOT NULL,
    granted_by_actor_id uuid,
    created_at timestamp with time zone NOT NULL,
    permission_policy_id uuid,
    CONSTRAINT role_assignments_agent_scope_check CHECK (((scope_kind <> 'agent'::text) OR (scope_id ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text))),
    CONSTRAINT role_assignments_check CHECK (((scope_kind = 'organization'::text) = (scope_id = ''::text))),
    CONSTRAINT role_assignments_check2 CHECK (((source = 'manual'::text) = (source_key = ''::text))),
    CONSTRAINT role_assignments_principal_kind_check CHECK ((principal_kind = ANY (ARRAY['membership'::text, 'team'::text, 'agent'::text, 'api_key'::text]))),
    CONSTRAINT role_assignments_scope_id_check CHECK (((scope_kind = 'organization'::text) = (scope_id = ''::text))),
    CONSTRAINT role_assignments_scope_kind_check CHECK ((scope_kind = ANY (ARRAY['organization'::text, 'team'::text, 'agent'::text]))),
    CONSTRAINT role_assignments_source_check CHECK ((source = ANY (ARRAY['manual'::text, 'directory'::text])))
);

-- roles (table)
CREATE TABLE public.roles (
    id uuid NOT NULL,
    organization_id uuid,
    key text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    permissions text[] DEFAULT '{}'::text[] NOT NULL,
    bindable_scope_kinds text[] NOT NULL,
    built_in boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT roles_bindable_scope_kinds_check CHECK (((bindable_scope_kinds <@ ARRAY['organization'::text, 'team'::text, 'agent'::text]) AND ('organization'::text = ANY (bindable_scope_kinds)))),
    CONSTRAINT roles_check CHECK ((built_in = (organization_id IS NULL))),
    CONSTRAINT roles_key_check CHECK ((key ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text))
);

-- sessions (table)
CREATE TABLE public.sessions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    secret_hash bytea NOT NULL,
    auth_method text NOT NULL,
    assurance text NOT NULL,
    authenticated_at timestamp with time zone NOT NULL,
    issued_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    client text DEFAULT 'web'::text NOT NULL,
    client_context jsonb DEFAULT '{}'::jsonb NOT NULL
);

-- slack_connections (table)
CREATE TABLE public.slack_connections (
    organization_id uuid NOT NULL,
    team_id text NOT NULL,
    team_name text NOT NULL,
    bot_user_id text NOT NULL,
    scopes text[] DEFAULT '{}'::text[] NOT NULL,
    sealed_bot_token bytea,
    installed_by_membership_id uuid NOT NULL,
    installed_at timestamp with time zone NOT NULL,
    installation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    status text DEFAULT 'connected'::text NOT NULL,
    disconnected_at timestamp with time zone,
    disconnect_reason text,
    CONSTRAINT slack_connections_state_check CHECK ((((status = 'connected'::text) AND (sealed_bot_token IS NOT NULL) AND (disconnected_at IS NULL) AND (disconnect_reason IS NULL)) OR ((status = 'disconnected'::text) AND (sealed_bot_token IS NULL) AND (disconnected_at IS NOT NULL) AND (disconnect_reason IS NOT NULL)))),
    CONSTRAINT slack_connections_status_check CHECK ((status = ANY (ARRAY['connected'::text, 'disconnected'::text])))
);

-- slack_email_fallbacks (table)
CREATE TABLE public.slack_email_fallbacks (
    organization_id uuid NOT NULL,
    request_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL
);

-- slack_event_inbox (table)
CREATE TABLE public.slack_event_inbox (
    id uuid NOT NULL,
    app_id text NOT NULL,
    external_event_id text NOT NULL,
    team_id text NOT NULL,
    event_type text NOT NULL,
    bot_user_ids text[] DEFAULT '{}'::text[] NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    received_at timestamp with time zone NOT NULL,
    applied_at timestamp with time zone,
    CONSTRAINT slack_event_inbox_event_type_check CHECK ((event_type = ANY (ARRAY['app_uninstalled'::text, 'tokens_revoked'::text])))
);

-- slack_install_states (table)
CREATE TABLE public.slack_install_states (
    state_hash bytea NOT NULL,
    organization_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL
);

-- team_escalation_policies (table)
CREATE TABLE public.team_escalation_policies (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    team_id uuid NOT NULL,
    revision bigint NOT NULL,
    is_active boolean DEFAULT false NOT NULL,
    document jsonb NOT NULL,
    document_version integer NOT NULL,
    created_by_actor_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT team_escalation_policies_document_check CHECK ((jsonb_typeof(document) = 'object'::text)),
    CONSTRAINT team_escalation_policies_document_version_check CHECK ((document_version > 0)),
    CONSTRAINT team_escalation_policies_revision_check CHECK ((revision > 0))
);

-- tool_catalog_sources (table)
CREATE TABLE public.tool_catalog_sources (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    agent_instance_id uuid,
    source_ref text,
    runtime text NOT NULL,
    discovery_context text NOT NULL,
    source text NOT NULL,
    server text NOT NULL,
    last_attempt_at timestamp with time zone NOT NULL,
    last_success_at timestamp with time zone,
    error text DEFAULT ''::text NOT NULL,
    source_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT tool_catalog_sources_owner_check CHECK (((agent_instance_id IS NULL) <> (source_ref IS NULL)))
);

-- tool_catalog_tools (table)
CREATE TABLE public.tool_catalog_tools (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    name text NOT NULL,
    display_name text NOT NULL,
    description text NOT NULL,
    input_schema jsonb,
    annotations jsonb,
    schema_fingerprint text NOT NULL,
    available boolean NOT NULL,
    last_seen_at timestamp with time zone NOT NULL
);

-- tool_presentations (table)
CREATE TABLE public.tool_presentations (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    runtime text,
    server text,
    tool text NOT NULL,
    spec jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT tool_presentations_runtime_check CHECK ((runtime ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text)),
    CONSTRAINT tool_presentations_server_check CHECK ((server ~ '^[A-Za-z0-9][A-Za-z0-9._-]*$'::text)),
    CONSTRAINT tool_presentations_tool_check CHECK ((tool <> ''::text))
);

-- users (table)
CREATE TABLE public.users (
    id uuid NOT NULL,
    display_name text NOT NULL,
    primary_email text NOT NULL,
    status text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT users_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
);

-- webhook_endpoint_revisions (table)
CREATE TABLE public.webhook_endpoint_revisions (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    endpoint_key text NOT NULL,
    revision bigint NOT NULL,
    is_active boolean DEFAULT false NOT NULL,
    name text NOT NULL,
    url text NOT NULL,
    created_by_actor_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT webhook_endpoint_revisions_name_check CHECK ((btrim(name) <> ''::text)),
    CONSTRAINT webhook_endpoint_revisions_revision_check CHECK ((revision > 0)),
    CONSTRAINT webhook_endpoint_revisions_url_check CHECK ((url ~ '^https?://'::text))
);

-- webhook_endpoint_secrets (table)
CREATE TABLE public.webhook_endpoint_secrets (
    organization_id uuid NOT NULL,
    endpoint_key text NOT NULL,
    sealed_signing_secret bytea NOT NULL,
    rotated_at timestamp with time zone NOT NULL,
    rotated_by_actor_id uuid NOT NULL,
    archived_at timestamp with time zone,
    CONSTRAINT webhook_endpoint_secrets_endpoint_key_check CHECK ((endpoint_key ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text))
);

-- agent_credentials agent_credentials_pkey (constraint)
ALTER TABLE ONLY public.agent_credentials
    ADD CONSTRAINT agent_credentials_pkey PRIMARY KEY (id);

-- agent_enrollments agent_enrollments_enrollment_code_hash_key (constraint)
ALTER TABLE ONLY public.agent_enrollments
    ADD CONSTRAINT agent_enrollments_enrollment_code_hash_key UNIQUE (enrollment_code_hash);

-- agent_enrollments agent_enrollments_pkey (constraint)
ALTER TABLE ONLY public.agent_enrollments
    ADD CONSTRAINT agent_enrollments_pkey PRIMARY KEY (id);

-- agent_enrollments agent_enrollments_user_code_key (constraint)
ALTER TABLE ONLY public.agent_enrollments
    ADD CONSTRAINT agent_enrollments_user_code_key UNIQUE (user_code);

-- agent_instances agent_instances_agent_id_name_key (constraint)
ALTER TABLE ONLY public.agent_instances
    ADD CONSTRAINT agent_instances_agent_id_name_key UNIQUE (agent_id, name);

-- agent_instances agent_instances_id_agent_id_organization_id_key (constraint)
ALTER TABLE ONLY public.agent_instances
    ADD CONSTRAINT agent_instances_id_agent_id_organization_id_key UNIQUE (id, agent_id, organization_id);

-- agent_instances agent_instances_id_organization_id_key (constraint)
ALTER TABLE ONLY public.agent_instances
    ADD CONSTRAINT agent_instances_id_organization_id_key UNIQUE (id, organization_id);

-- agent_instances agent_instances_pkey (constraint)
ALTER TABLE ONLY public.agent_instances
    ADD CONSTRAINT agent_instances_pkey PRIMARY KEY (id);

-- agent_provisioners agent_provisioners_pkey (constraint)
ALTER TABLE ONLY public.agent_provisioners
    ADD CONSTRAINT agent_provisioners_pkey PRIMARY KEY (id);

-- agents agents_id_organization_id_key (constraint)
ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_id_organization_id_key UNIQUE (id, organization_id);

-- agents agents_organization_id_slug_key (constraint)
ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_organization_id_slug_key UNIQUE (organization_id, slug);

-- agents agents_pkey (constraint)
ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_pkey PRIMARY KEY (id);

-- api_key_creations api_key_creations_pkey (constraint)
ALTER TABLE ONLY public.api_key_creations
    ADD CONSTRAINT api_key_creations_pkey PRIMARY KEY (organization_id, membership_id, idempotency_key);

-- api_keys api_keys_kind_identity (constraint)
ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_kind_identity UNIQUE (id, organization_id, kind);

-- api_keys api_keys_pkey (constraint)
ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);

-- approval_request_claims approval_request_claims_pkey (constraint)
ALTER TABLE ONLY public.approval_request_claims
    ADD CONSTRAINT approval_request_claims_pkey PRIMARY KEY (id);

-- approval_request_escalation_state approval_request_escalation_stat_request_id_organization_id_key (constraint)
ALTER TABLE ONLY public.approval_request_escalation_state
    ADD CONSTRAINT approval_request_escalation_stat_request_id_organization_id_key UNIQUE (request_id, organization_id);

-- approval_request_escalation_state approval_request_escalation_state_pkey (constraint)
ALTER TABLE ONLY public.approval_request_escalation_state
    ADD CONSTRAINT approval_request_escalation_state_pkey PRIMARY KEY (request_id);

-- approval_request_escalation_team_state approval_request_escalation_team_state_pkey (constraint)
ALTER TABLE ONLY public.approval_request_escalation_team_state
    ADD CONSTRAINT approval_request_escalation_team_state_pkey PRIMARY KEY (request_id, team_id);

-- approval_request_notifications approval_request_notifications_pkey (constraint)
ALTER TABLE ONLY public.approval_request_notifications
    ADD CONSTRAINT approval_request_notifications_pkey PRIMARY KEY (request_id, membership_id);

-- approval_request_pipeline_suspensions approval_request_pipeline_suspensions_pkey (constraint)
ALTER TABLE ONLY public.approval_request_pipeline_suspensions
    ADD CONSTRAINT approval_request_pipeline_suspensions_pkey PRIMARY KEY (request_id);

-- approval_request_reviewers approval_request_reviewers_pkey (constraint)
ALTER TABLE ONLY public.approval_request_reviewers
    ADD CONSTRAINT approval_request_reviewers_pkey PRIMARY KEY (request_id, membership_id);

-- approval_request_routing approval_request_routing_pkey (constraint)
ALTER TABLE ONLY public.approval_request_routing
    ADD CONSTRAINT approval_request_routing_pkey PRIMARY KEY (request_id);

-- approval_request_routing_team_policies approval_request_routing_team_policies_pkey (constraint)
ALTER TABLE ONLY public.approval_request_routing_team_policies
    ADD CONSTRAINT approval_request_routing_team_policies_pkey PRIMARY KEY (request_id, team_id);

-- approval_requests approval_requests_id_organization_id_key (constraint)
ALTER TABLE ONLY public.approval_requests
    ADD CONSTRAINT approval_requests_id_organization_id_key UNIQUE (id, organization_id);

-- approval_requests approval_requests_instance_idempotency_key (constraint)
ALTER TABLE ONLY public.approval_requests
    ADD CONSTRAINT approval_requests_instance_idempotency_key UNIQUE (organization_id, agent_instance_id, idempotency_key);

-- approval_requests approval_requests_pkey (constraint)
ALTER TABLE ONLY public.approval_requests
    ADD CONSTRAINT approval_requests_pkey PRIMARY KEY (id);

-- contact_methods contact_methods_pkey (constraint)
ALTER TABLE ONLY public.contact_methods
    ADD CONSTRAINT contact_methods_pkey PRIMARY KEY (id);

-- decision_operations decision_operations_organization_id_idempotency_key_key (constraint)
ALTER TABLE ONLY public.decision_operations
    ADD CONSTRAINT decision_operations_organization_id_idempotency_key_key UNIQUE (organization_id, idempotency_key);

-- decision_operations decision_operations_pkey (constraint)
ALTER TABLE ONLY public.decision_operations
    ADD CONSTRAINT decision_operations_pkey PRIMARY KEY (id);

-- decisions decisions_organization_id_idempotency_key_key (constraint)
ALTER TABLE ONLY public.decisions
    ADD CONSTRAINT decisions_organization_id_idempotency_key_key UNIQUE (organization_id, idempotency_key);

-- decisions decisions_pkey (constraint)
ALTER TABLE ONLY public.decisions
    ADD CONSTRAINT decisions_pkey PRIMARY KEY (id);

-- decisions decisions_request_id_key (constraint)
ALTER TABLE ONLY public.decisions
    ADD CONSTRAINT decisions_request_id_key UNIQUE (request_id);

-- directory_connections directory_connections_adapter_key_provider_resource_id_key (constraint)
ALTER TABLE ONLY public.directory_connections
    ADD CONSTRAINT directory_connections_adapter_key_provider_resource_id_key UNIQUE (adapter_key, provider_resource_id);

-- directory_connections directory_connections_id_organization_id_key (constraint)
ALTER TABLE ONLY public.directory_connections
    ADD CONSTRAINT directory_connections_id_organization_id_key UNIQUE (id, organization_id);

-- directory_connections directory_connections_pkey (constraint)
ALTER TABLE ONLY public.directory_connections
    ADD CONSTRAINT directory_connections_pkey PRIMARY KEY (id);

-- directory_object_links directory_object_links_pkey (constraint)
ALTER TABLE ONLY public.directory_object_links
    ADD CONSTRAINT directory_object_links_pkey PRIMARY KEY (directory_connection_id, provider_object_type, provider_object_id);

-- discord_connections discord_connections_pkey (constraint)
ALTER TABLE ONLY public.discord_connections
    ADD CONSTRAINT discord_connections_pkey PRIMARY KEY (organization_id);

-- discord_oauth_states discord_oauth_states_pkey (constraint)
ALTER TABLE ONLY public.discord_oauth_states
    ADD CONSTRAINT discord_oauth_states_pkey PRIMARY KEY (state_hash);

-- escalation_paths escalation_paths_id_organization_id_key (constraint)
ALTER TABLE ONLY public.escalation_paths
    ADD CONSTRAINT escalation_paths_id_organization_id_key UNIQUE (id, organization_id);

-- escalation_paths escalation_paths_organization_id_path_key_revision_key (constraint)
ALTER TABLE ONLY public.escalation_paths
    ADD CONSTRAINT escalation_paths_organization_id_path_key_revision_key UNIQUE (organization_id, path_key, revision);

-- escalation_paths escalation_paths_pkey (constraint)
ALTER TABLE ONLY public.escalation_paths
    ADD CONSTRAINT escalation_paths_pkey PRIMARY KEY (id);

-- external_identities external_identities_pkey (constraint)
ALTER TABLE ONLY public.external_identities
    ADD CONSTRAINT external_identities_pkey PRIMARY KEY (provider_key, issuer, subject);

-- invitations invitations_pkey (constraint)
ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_pkey PRIMARY KEY (id);

-- invitations invitations_token_hash_key (constraint)
ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_token_hash_key UNIQUE (token_hash);

-- local_credentials local_credentials_pkey (constraint)
ALTER TABLE ONLY public.local_credentials
    ADD CONSTRAINT local_credentials_pkey PRIMARY KEY (user_id);

-- login_tickets login_tickets_pkey (constraint)
ALTER TABLE ONLY public.login_tickets
    ADD CONSTRAINT login_tickets_pkey PRIMARY KEY (id);

-- management_actors management_actors_pkey (constraint)
ALTER TABLE ONLY public.management_actors
    ADD CONSTRAINT management_actors_pkey PRIMARY KEY (id, organization_id);

-- membership_roles membership_roles_pkey (constraint)
ALTER TABLE ONLY public.membership_roles
    ADD CONSTRAINT membership_roles_pkey PRIMARY KEY (membership_id, role, source, source_key);

-- membership_teams membership_teams_pkey (constraint)
ALTER TABLE ONLY public.membership_teams
    ADD CONSTRAINT membership_teams_pkey PRIMARY KEY (membership_id, team_id, source, source_key);

-- memberships memberships_id_organization_id_key (constraint)
ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_id_organization_id_key UNIQUE (id, organization_id);

-- memberships memberships_pkey (constraint)
ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_pkey PRIMARY KEY (id);

-- memberships memberships_user_id_organization_id_key (constraint)
ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_user_id_organization_id_key UNIQUE (user_id, organization_id);

-- notification_rules notification_rules_pkey (constraint)
ALTER TABLE ONLY public.notification_rules
    ADD CONSTRAINT notification_rules_pkey PRIMARY KEY (id);

-- notification_rules notification_rules_user_id_urgency_position_key (constraint)
ALTER TABLE ONLY public.notification_rules
    ADD CONSTRAINT notification_rules_user_id_urgency_position_key UNIQUE (user_id, urgency, "position");

-- oauth_authorization_codes oauth_authorization_codes_code_hash_key (constraint)
ALTER TABLE ONLY public.oauth_authorization_codes
    ADD CONSTRAINT oauth_authorization_codes_code_hash_key UNIQUE (code_hash);

-- oauth_authorization_codes oauth_authorization_codes_pkey (constraint)
ALTER TABLE ONLY public.oauth_authorization_codes
    ADD CONSTRAINT oauth_authorization_codes_pkey PRIMARY KEY (id);

-- oauth_clients oauth_clients_pkey (constraint)
ALTER TABLE ONLY public.oauth_clients
    ADD CONSTRAINT oauth_clients_pkey PRIMARY KEY (id);

-- onboarding_sessions onboarding_sessions_pkey (constraint)
ALTER TABLE ONLY public.onboarding_sessions
    ADD CONSTRAINT onboarding_sessions_pkey PRIMARY KEY (id);

-- organization_policies organization_policies_pkey (constraint)
ALTER TABLE ONLY public.organization_policies
    ADD CONSTRAINT organization_policies_pkey PRIMARY KEY (organization_id);

-- organizations organizations_pkey (constraint)
ALTER TABLE ONLY public.organizations
    ADD CONSTRAINT organizations_pkey PRIMARY KEY (id);

-- organizations organizations_slug_key (constraint)
ALTER TABLE ONLY public.organizations
    ADD CONSTRAINT organizations_slug_key UNIQUE (slug);

-- outbox_events outbox_events_id_key (constraint)
ALTER TABLE ONLY public.outbox_events
    ADD CONSTRAINT outbox_events_id_key UNIQUE (id);

-- outbox_events outbox_events_pkey (constraint)
ALTER TABLE ONLY public.outbox_events
    ADD CONSTRAINT outbox_events_pkey PRIMARY KEY (sequence);

-- password_resets password_resets_pkey (constraint)
ALTER TABLE ONLY public.password_resets
    ADD CONSTRAINT password_resets_pkey PRIMARY KEY (id);

-- permission_policies permission_policies_id_organization_id_key (constraint)
ALTER TABLE ONLY public.permission_policies
    ADD CONSTRAINT permission_policies_id_organization_id_key UNIQUE (id, organization_id);

-- permission_policies permission_policies_pkey (constraint)
ALTER TABLE ONLY public.permission_policies
    ADD CONSTRAINT permission_policies_pkey PRIMARY KEY (id);

-- permission_policy_commands permission_policy_commands_pkey (constraint)
ALTER TABLE ONLY public.permission_policy_commands
    ADD CONSTRAINT permission_policy_commands_pkey PRIMARY KEY (organization_id, membership_id, idempotency_key);

-- request_approval_blocks request_approval_blocks_id_organization_id_key (constraint)
ALTER TABLE ONLY public.request_approval_blocks
    ADD CONSTRAINT request_approval_blocks_id_organization_id_key UNIQUE (id, organization_id);

-- request_approval_blocks request_approval_blocks_organization_id_content_hash_key (constraint)
ALTER TABLE ONLY public.request_approval_blocks
    ADD CONSTRAINT request_approval_blocks_organization_id_content_hash_key UNIQUE (organization_id, content_hash);

-- request_approval_blocks request_approval_blocks_pkey (constraint)
ALTER TABLE ONLY public.request_approval_blocks
    ADD CONSTRAINT request_approval_blocks_pkey PRIMARY KEY (id);

-- request_approval_pipelines request_approval_pipelines_id_organization_id_key (constraint)
ALTER TABLE ONLY public.request_approval_pipelines
    ADD CONSTRAINT request_approval_pipelines_id_organization_id_key UNIQUE (id, organization_id);

-- request_approval_pipelines request_approval_pipelines_pkey (constraint)
ALTER TABLE ONLY public.request_approval_pipelines
    ADD CONSTRAINT request_approval_pipelines_pkey PRIMARY KEY (id);

-- request_approval_pipelines request_approval_pipelines_scope_revision_key (constraint)
ALTER TABLE ONLY public.request_approval_pipelines
    ADD CONSTRAINT request_approval_pipelines_scope_revision_key UNIQUE (organization_id, agent_slug, revision);

-- role_assignments role_assignments_pkey (constraint)
ALTER TABLE ONLY public.role_assignments
    ADD CONSTRAINT role_assignments_pkey PRIMARY KEY (id);

-- role_assignments role_assignments_principal_kind_principal_id_role_id_scope__key (constraint)
ALTER TABLE ONLY public.role_assignments
    ADD CONSTRAINT role_assignments_principal_kind_principal_id_role_id_scope__key UNIQUE (principal_kind, principal_id, role_id, scope_kind, scope_id, source, source_key);

-- roles roles_id_organization_id_key (constraint)
ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_id_organization_id_key UNIQUE (id, organization_id);

-- roles roles_pkey (constraint)
ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (id);

-- sessions sessions_pkey (constraint)
ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);

-- sessions sessions_secret_hash_key (constraint)
ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_secret_hash_key UNIQUE (secret_hash);

-- slack_connections slack_connections_pkey (constraint)
ALTER TABLE ONLY public.slack_connections
    ADD CONSTRAINT slack_connections_pkey PRIMARY KEY (organization_id);

-- slack_email_fallbacks slack_email_fallbacks_pkey (constraint)
ALTER TABLE ONLY public.slack_email_fallbacks
    ADD CONSTRAINT slack_email_fallbacks_pkey PRIMARY KEY (organization_id, request_id, user_id);

-- slack_event_inbox slack_event_inbox_app_id_external_event_id_key (constraint)
ALTER TABLE ONLY public.slack_event_inbox
    ADD CONSTRAINT slack_event_inbox_app_id_external_event_id_key UNIQUE (app_id, external_event_id);

-- slack_event_inbox slack_event_inbox_pkey (constraint)
ALTER TABLE ONLY public.slack_event_inbox
    ADD CONSTRAINT slack_event_inbox_pkey PRIMARY KEY (id);

-- slack_install_states slack_install_states_pkey (constraint)
ALTER TABLE ONLY public.slack_install_states
    ADD CONSTRAINT slack_install_states_pkey PRIMARY KEY (state_hash);

-- team_escalation_policies team_escalation_policies_id_organization_id_key (constraint)
ALTER TABLE ONLY public.team_escalation_policies
    ADD CONSTRAINT team_escalation_policies_id_organization_id_key UNIQUE (id, organization_id);

-- team_escalation_policies team_escalation_policies_id_organization_id_team_id_key (constraint)
ALTER TABLE ONLY public.team_escalation_policies
    ADD CONSTRAINT team_escalation_policies_id_organization_id_team_id_key UNIQUE (id, organization_id, team_id);

-- team_escalation_policies team_escalation_policies_organization_id_team_id_revision_key (constraint)
ALTER TABLE ONLY public.team_escalation_policies
    ADD CONSTRAINT team_escalation_policies_organization_id_team_id_revision_key UNIQUE (organization_id, team_id, revision);

-- team_escalation_policies team_escalation_policies_pkey (constraint)
ALTER TABLE ONLY public.team_escalation_policies
    ADD CONSTRAINT team_escalation_policies_pkey PRIMARY KEY (id);

-- teams teams_id_organization_id_key (constraint)
ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_id_organization_id_key UNIQUE (id, organization_id);

-- teams teams_pkey (constraint)
ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_pkey PRIMARY KEY (id);

-- tool_catalog_sources tool_catalog_sources_id_organization_id_key (constraint)
ALTER TABLE ONLY public.tool_catalog_sources
    ADD CONSTRAINT tool_catalog_sources_id_organization_id_key UNIQUE (id, organization_id);

-- tool_catalog_sources tool_catalog_sources_pkey (constraint)
ALTER TABLE ONLY public.tool_catalog_sources
    ADD CONSTRAINT tool_catalog_sources_pkey PRIMARY KEY (id);

-- tool_catalog_tools tool_catalog_tools_organization_id_source_id_name_key (constraint)
ALTER TABLE ONLY public.tool_catalog_tools
    ADD CONSTRAINT tool_catalog_tools_organization_id_source_id_name_key UNIQUE (organization_id, source_id, name);

-- tool_catalog_tools tool_catalog_tools_pkey (constraint)
ALTER TABLE ONLY public.tool_catalog_tools
    ADD CONSTRAINT tool_catalog_tools_pkey PRIMARY KEY (id);

-- tool_presentations tool_presentations_pkey (constraint)
ALTER TABLE ONLY public.tool_presentations
    ADD CONSTRAINT tool_presentations_pkey PRIMARY KEY (id);

-- users users_pkey (constraint)
ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

-- webhook_endpoint_revisions webhook_endpoint_revisions_id_organization_id_key (constraint)
ALTER TABLE ONLY public.webhook_endpoint_revisions
    ADD CONSTRAINT webhook_endpoint_revisions_id_organization_id_key UNIQUE (id, organization_id);

-- webhook_endpoint_revisions webhook_endpoint_revisions_organization_id_endpoint_key_rev_key (constraint)
ALTER TABLE ONLY public.webhook_endpoint_revisions
    ADD CONSTRAINT webhook_endpoint_revisions_organization_id_endpoint_key_rev_key UNIQUE (organization_id, endpoint_key, revision);

-- webhook_endpoint_revisions webhook_endpoint_revisions_pkey (constraint)
ALTER TABLE ONLY public.webhook_endpoint_revisions
    ADD CONSTRAINT webhook_endpoint_revisions_pkey PRIMARY KEY (id);

-- webhook_endpoint_secrets webhook_endpoint_secrets_pkey (constraint)
ALTER TABLE ONLY public.webhook_endpoint_secrets
    ADD CONSTRAINT webhook_endpoint_secrets_pkey PRIMARY KEY (organization_id, endpoint_key);

-- agent_credentials_instance_issued_idx (index)
CREATE INDEX agent_credentials_instance_issued_idx ON public.agent_credentials USING btree (organization_id, agent_instance_id, issued_at DESC);

-- agent_instances_agent_seen_idx (index)
CREATE INDEX agent_instances_agent_seen_idx ON public.agent_instances USING btree (organization_id, agent_id, last_seen_at DESC, created_at DESC, id DESC);

-- agent_provisioners_agent_issued_idx (index)
CREATE INDEX agent_provisioners_agent_issued_idx ON public.agent_provisioners USING btree (organization_id, agent_id, issued_at DESC, id DESC);

-- agents_live_name_idx (index)
CREATE UNIQUE INDEX agents_live_name_idx ON public.agents USING btree (organization_id, name) WHERE (archived_at IS NULL);

-- api_keys_membership_idx (index)
CREATE INDEX api_keys_membership_idx ON public.api_keys USING btree (organization_id, membership_id);

-- api_keys_oauth_client_idx (index)
CREATE INDEX api_keys_oauth_client_idx ON public.api_keys USING btree (oauth_client_id) WHERE (oauth_client_id IS NOT NULL);

-- api_keys_organization_idx (index)
CREATE INDEX api_keys_organization_idx ON public.api_keys USING btree (organization_id, created_at DESC);

-- approval_request_claims_open (index)
CREATE UNIQUE INDEX approval_request_claims_open ON public.approval_request_claims USING btree (organization_id, request_id) WHERE (released_at IS NULL);

-- approval_request_notifications_membership_idx (index)
CREATE INDEX approval_request_notifications_membership_idx ON public.approval_request_notifications USING btree (organization_id, membership_id);

-- approval_request_pipeline_suspensions_open_idx (index)
CREATE INDEX approval_request_pipeline_suspensions_open_idx ON public.approval_request_pipeline_suspensions USING btree (organization_id, suspended_at) WHERE (resumed_at IS NULL);

-- approval_request_reviewers_membership_idx (index)
CREATE INDEX approval_request_reviewers_membership_idx ON public.approval_request_reviewers USING btree (organization_id, membership_id);

-- approval_requests_agent_created_idx (index)
CREATE INDEX approval_requests_agent_created_idx ON public.approval_requests USING btree (organization_id, agent_id, created_at DESC, id DESC);

-- approval_requests_agent_slug_history_idx (index)
CREATE INDEX approval_requests_agent_slug_history_idx ON public.approval_requests USING btree (organization_id, agent_slug, created_at DESC, id DESC);

-- approval_requests_instance_created_idx (index)
CREATE INDEX approval_requests_instance_created_idx ON public.approval_requests USING btree (organization_id, agent_instance_id, created_at DESC);

-- approval_requests_queue (index)
CREATE INDEX approval_requests_queue ON public.approval_requests USING btree (organization_id, status, created_at DESC);

-- approval_requests_semantic_hash (index)
CREATE INDEX approval_requests_semantic_hash ON public.approval_requests USING btree (organization_id, semantic_hash, created_at DESC);

-- contact_methods_org_identity_idx (index)
CREATE UNIQUE INDEX contact_methods_org_identity_idx ON public.contact_methods USING btree (organization_id, kind, value) WHERE ((organization_id IS NOT NULL) AND (kind <> 'slack'::text));

-- contact_methods_org_link_idx (index)
CREATE UNIQUE INDEX contact_methods_org_link_idx ON public.contact_methods USING btree (user_id, organization_id, kind) WHERE (organization_id IS NOT NULL);

-- contact_methods_slack_identity_idx (index)
CREATE UNIQUE INDEX contact_methods_slack_identity_idx ON public.contact_methods USING btree (organization_id, slack_installation_id, value) WHERE ((kind = 'slack'::text) AND (slack_installation_id IS NOT NULL));

-- contact_methods_user_value_idx (index)
CREATE UNIQUE INDEX contact_methods_user_value_idx ON public.contact_methods USING btree (user_id, kind, lower(value));

-- contact_methods_verified_idx (index)
CREATE INDEX contact_methods_verified_idx ON public.contact_methods USING btree (user_id) WHERE (verified_at IS NOT NULL);

-- discord_connections_guild_idx (index)
CREATE UNIQUE INDEX discord_connections_guild_idx ON public.discord_connections USING btree (guild_id);

-- discord_oauth_states_expires_idx (index)
CREATE INDEX discord_oauth_states_expires_idx ON public.discord_oauth_states USING btree (expires_at);

-- escalation_paths_active_revision_idx (index)
CREATE UNIQUE INDEX escalation_paths_active_revision_idx ON public.escalation_paths USING btree (organization_id, path_key) WHERE is_active;

-- escalation_paths_live_idx (index)
CREATE INDEX escalation_paths_live_idx ON public.escalation_paths USING btree (organization_id, path_key) WHERE (archived_at IS NULL);

-- invitations_organization_status_idx (index)
CREATE INDEX invitations_organization_status_idx ON public.invitations USING btree (organization_id, status, created_at DESC);

-- invitations_pending_email_idx (index)
CREATE UNIQUE INDEX invitations_pending_email_idx ON public.invitations USING btree (organization_id, lower(email)) WHERE (status = 'pending'::text);

-- login_tickets_expiry_idx (index)
CREATE INDEX login_tickets_expiry_idx ON public.login_tickets USING btree (expires_at);

-- notification_rules_user_idx (index)
CREATE INDEX notification_rules_user_idx ON public.notification_rules USING btree (user_id, urgency, "position");

-- oauth_authorization_codes_expiry_idx (index)
CREATE INDEX oauth_authorization_codes_expiry_idx ON public.oauth_authorization_codes USING btree (expires_at);

-- onboarding_sessions_open_member_idx (index)
CREATE UNIQUE INDEX onboarding_sessions_open_member_idx ON public.onboarding_sessions USING btree (organization_id, membership_id) WHERE (status <> ALL (ARRAY['completed'::text, 'skipped'::text]));

-- onboarding_sessions_setup_code_idx (index)
CREATE INDEX onboarding_sessions_setup_code_idx ON public.onboarding_sessions USING btree (setup_code_hash) WHERE (setup_code_hash IS NOT NULL);

-- outbox_events_unpublished_topic_idx (index)
CREATE INDEX outbox_events_unpublished_topic_idx ON public.outbox_events USING btree (topic, sequence) WHERE (published_at IS NULL);

-- password_resets_expires_at_idx (index)
CREATE INDEX password_resets_expires_at_idx ON public.password_resets USING btree (expires_at);

-- request_approval_pipelines_active_revision_idx (index)
CREATE UNIQUE INDEX request_approval_pipelines_active_revision_idx ON public.request_approval_pipelines USING btree (organization_id, agent_slug) WHERE is_active;

-- request_approval_pipelines_live_scope_idx (index)
CREATE INDEX request_approval_pipelines_live_scope_idx ON public.request_approval_pipelines USING btree (organization_id, agent_slug) WHERE (archived_at IS NULL);

-- request_approval_pipelines_organization_scope_active_idx (index)
CREATE UNIQUE INDEX request_approval_pipelines_organization_scope_active_idx ON public.request_approval_pipelines USING btree (organization_id) WHERE (is_active AND (agent_slug IS NULL));

-- request_approval_pipelines_organization_scope_revision_idx (index)
CREATE UNIQUE INDEX request_approval_pipelines_organization_scope_revision_idx ON public.request_approval_pipelines USING btree (organization_id, revision) WHERE (agent_slug IS NULL);

-- role_assignments_policy_idx (index)
CREATE INDEX role_assignments_policy_idx ON public.role_assignments USING btree (organization_id, permission_policy_id);

-- role_assignments_principal_idx (index)
CREATE INDEX role_assignments_principal_idx ON public.role_assignments USING btree (organization_id, principal_kind, principal_id);

-- role_assignments_role_idx (index)
CREATE INDEX role_assignments_role_idx ON public.role_assignments USING btree (role_id);

-- role_assignments_scope_idx (index)
CREATE INDEX role_assignments_scope_idx ON public.role_assignments USING btree (organization_id, scope_kind, scope_id);

-- roles_built_in_key_idx (index)
CREATE UNIQUE INDEX roles_built_in_key_idx ON public.roles USING btree (key) WHERE (organization_id IS NULL);

-- roles_organization_key_idx (index)
CREATE UNIQUE INDEX roles_organization_key_idx ON public.roles USING btree (organization_id, key) WHERE (organization_id IS NOT NULL);

-- slack_connections_team_idx (index)
CREATE UNIQUE INDEX slack_connections_team_idx ON public.slack_connections USING btree (team_id) WHERE (status = 'connected'::text);

-- slack_event_inbox_pending_idx (index)
CREATE INDEX slack_event_inbox_pending_idx ON public.slack_event_inbox USING btree (received_at) WHERE (applied_at IS NULL);

-- slack_install_states_expires_idx (index)
CREATE INDEX slack_install_states_expires_idx ON public.slack_install_states USING btree (expires_at);

-- team_escalation_policies_active_revision_idx (index)
CREATE UNIQUE INDEX team_escalation_policies_active_revision_idx ON public.team_escalation_policies USING btree (organization_id, team_id) WHERE is_active;

-- teams_live_name_idx (index)
CREATE UNIQUE INDEX teams_live_name_idx ON public.teams USING btree (organization_id, name) WHERE (archived_at IS NULL);

-- tool_catalog_sources_instance_context_idx (index)
CREATE UNIQUE INDEX tool_catalog_sources_instance_context_idx ON public.tool_catalog_sources USING btree (organization_id, agent_instance_id, runtime, discovery_context, source, server) WHERE (agent_instance_id IS NOT NULL);

-- tool_catalog_sources_source_ref_idx (index)
CREATE UNIQUE INDEX tool_catalog_sources_source_ref_idx ON public.tool_catalog_sources USING btree (organization_id, source_ref) WHERE (source_ref IS NOT NULL);

-- tool_catalog_tools_source_idx (index)
CREATE INDEX tool_catalog_tools_source_idx ON public.tool_catalog_tools USING btree (source_id, organization_id);

-- tool_presentations_binding_idx (index)
CREATE UNIQUE INDEX tool_presentations_binding_idx ON public.tool_presentations USING btree (organization_id, COALESCE(runtime, ''::text), COALESCE(server, ''::text), tool);

-- users_primary_email_ci (index)
CREATE UNIQUE INDEX users_primary_email_ci ON public.users USING btree (lower(primary_email));

-- webhook_endpoint_revisions_active_idx (index)
CREATE UNIQUE INDEX webhook_endpoint_revisions_active_idx ON public.webhook_endpoint_revisions USING btree (organization_id, endpoint_key) WHERE is_active;

-- api_keys api_keys_management_actor (trigger)
CREATE TRIGGER api_keys_management_actor AFTER INSERT ON public.api_keys FOR EACH ROW EXECUTE FUNCTION public.register_management_actor();

-- approval_request_notifications approval_request_notifications_append_only (trigger)
CREATE TRIGGER approval_request_notifications_append_only BEFORE DELETE OR UPDATE ON public.approval_request_notifications FOR EACH ROW EXECUTE FUNCTION public.reject_record_mutation();

-- approval_request_reviewers approval_request_reviewers_append_only (trigger)
CREATE TRIGGER approval_request_reviewers_append_only BEFORE DELETE OR UPDATE ON public.approval_request_reviewers FOR EACH ROW EXECUTE FUNCTION public.reject_record_mutation();

-- approval_request_routing approval_request_routing_append_only (trigger)
CREATE TRIGGER approval_request_routing_append_only BEFORE DELETE OR UPDATE ON public.approval_request_routing FOR EACH ROW EXECUTE FUNCTION public.reject_record_mutation();

-- approval_request_routing_team_policies approval_request_routing_team_policies_append_only (trigger)
CREATE TRIGGER approval_request_routing_team_policies_append_only BEFORE DELETE OR UPDATE ON public.approval_request_routing_team_policies FOR EACH ROW EXECUTE FUNCTION public.reject_record_mutation();

-- approval_requests approval_requests_terminal_state (trigger)
CREATE TRIGGER approval_requests_terminal_state BEFORE UPDATE ON public.approval_requests FOR EACH ROW EXECUTE FUNCTION public.protect_terminal_approval();

-- decisions decisions_append_only (trigger)
CREATE TRIGGER decisions_append_only BEFORE DELETE OR UPDATE ON public.decisions FOR EACH ROW EXECUTE FUNCTION public.reject_record_mutation();

-- decisions decisions_set_expiry (trigger)
CREATE TRIGGER decisions_set_expiry BEFORE INSERT ON public.decisions FOR EACH ROW EXECUTE FUNCTION public.set_approval_decision_expiry();

-- escalation_paths escalation_paths_monotonic_revision (trigger)
CREATE TRIGGER escalation_paths_monotonic_revision BEFORE INSERT ON public.escalation_paths FOR EACH ROW EXECUTE FUNCTION public.enforce_escalation_path_revision();

-- escalation_paths escalation_paths_protect_revision (trigger)
CREATE TRIGGER escalation_paths_protect_revision BEFORE DELETE OR UPDATE ON public.escalation_paths FOR EACH ROW EXECUTE FUNCTION public.protect_escalation_path_revision();

-- membership_roles membership_roles_last_owner (trigger)
CREATE TRIGGER membership_roles_last_owner AFTER DELETE ON public.membership_roles FOR EACH ROW EXECUTE FUNCTION public.protect_last_owner();

-- memberships memberships_last_owner (trigger)
CREATE TRIGGER memberships_last_owner BEFORE UPDATE OF status ON public.memberships FOR EACH ROW EXECUTE FUNCTION public.protect_owner_membership_state();

-- memberships memberships_management_actor (trigger)
CREATE TRIGGER memberships_management_actor AFTER INSERT ON public.memberships FOR EACH ROW EXECUTE FUNCTION public.register_management_actor();

-- request_approval_blocks request_approval_blocks_append_only (trigger)
CREATE TRIGGER request_approval_blocks_append_only BEFORE DELETE OR UPDATE ON public.request_approval_blocks FOR EACH ROW EXECUTE FUNCTION public.reject_record_mutation();

-- request_approval_blocks request_approval_blocks_content_hash (trigger)
CREATE TRIGGER request_approval_blocks_content_hash BEFORE INSERT ON public.request_approval_blocks FOR EACH ROW EXECUTE FUNCTION public.set_request_approval_block_content_hash();

-- request_approval_pipelines request_approval_pipelines_immutable_revision (trigger)
CREATE TRIGGER request_approval_pipelines_immutable_revision BEFORE DELETE OR UPDATE ON public.request_approval_pipelines FOR EACH ROW EXECUTE FUNCTION public.protect_request_approval_pipeline_revision();

-- request_approval_pipelines request_approval_pipelines_monotonic_revision (trigger)
CREATE TRIGGER request_approval_pipelines_monotonic_revision BEFORE INSERT ON public.request_approval_pipelines FOR EACH ROW EXECUTE FUNCTION public.enforce_request_approval_pipeline_revision();

-- request_approval_pipelines request_approval_pipelines_scope_active (trigger)
CREATE CONSTRAINT TRIGGER request_approval_pipelines_scope_active AFTER UPDATE OF is_active ON public.request_approval_pipelines DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN ((old.is_active AND (NOT new.is_active))) EXECUTE FUNCTION public.enforce_request_approval_pipeline_scope_active();

-- request_approval_pipelines request_approval_pipelines_validate_blocks (trigger)
CREATE CONSTRAINT TRIGGER request_approval_pipelines_validate_blocks AFTER INSERT ON public.request_approval_pipelines DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION public.validate_request_approval_pipeline_blocks();

-- role_assignments role_assignments_check (trigger)
CREATE TRIGGER role_assignments_check BEFORE INSERT OR UPDATE ON public.role_assignments FOR EACH ROW EXECUTE FUNCTION public.check_role_assignment();

-- role_assignments role_assignments_last_owner (trigger)
CREATE TRIGGER role_assignments_last_owner AFTER DELETE OR UPDATE OF role_id, scope_kind, principal_id, principal_kind ON public.role_assignments FOR EACH ROW EXECUTE FUNCTION public.protect_last_owner_assignment();

-- role_assignments role_assignments_policy_identity (trigger)
CREATE TRIGGER role_assignments_policy_identity BEFORE INSERT ON public.role_assignments FOR EACH ROW EXECUTE FUNCTION public.identify_permission_policy();

-- team_escalation_policies team_escalation_policies_monotonic_revision (trigger)
CREATE TRIGGER team_escalation_policies_monotonic_revision BEFORE INSERT ON public.team_escalation_policies FOR EACH ROW EXECUTE FUNCTION public.enforce_team_escalation_policy_revision();

-- team_escalation_policies team_escalation_policies_protect_revision (trigger)
CREATE TRIGGER team_escalation_policies_protect_revision BEFORE DELETE OR UPDATE ON public.team_escalation_policies FOR EACH ROW EXECUTE FUNCTION public.protect_team_escalation_policy_revision();

-- teams teams_archive_api_key_assignments (trigger)
CREATE TRIGGER teams_archive_api_key_assignments AFTER UPDATE OF archived_at ON public.teams FOR EACH ROW WHEN (((old.archived_at IS NULL) AND (new.archived_at IS NOT NULL))) EXECUTE FUNCTION public.remove_team_api_key_assignments();

-- teams teams_remove_api_key_assignments (trigger)
CREATE TRIGGER teams_remove_api_key_assignments BEFORE DELETE ON public.teams FOR EACH ROW EXECUTE FUNCTION public.remove_team_api_key_assignments();

-- webhook_endpoint_revisions webhook_endpoint_revisions_monotonic_revision (trigger)
CREATE TRIGGER webhook_endpoint_revisions_monotonic_revision BEFORE INSERT ON public.webhook_endpoint_revisions FOR EACH ROW EXECUTE FUNCTION public.enforce_webhook_endpoint_revision();

-- webhook_endpoint_revisions webhook_endpoint_revisions_protect_revision (trigger)
CREATE TRIGGER webhook_endpoint_revisions_protect_revision BEFORE DELETE OR UPDATE ON public.webhook_endpoint_revisions FOR EACH ROW EXECUTE FUNCTION public.protect_webhook_endpoint_revision();

-- agent_credentials agent_credentials_agent_instance_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_credentials
    ADD CONSTRAINT agent_credentials_agent_instance_id_organization_id_fkey FOREIGN KEY (agent_instance_id, organization_id) REFERENCES public.agent_instances(id, organization_id);

-- agent_credentials agent_credentials_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_credentials
    ADD CONSTRAINT agent_credentials_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- agent_credentials agent_credentials_rotated_from_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_credentials
    ADD CONSTRAINT agent_credentials_rotated_from_id_fkey FOREIGN KEY (rotated_from_id) REFERENCES public.agent_credentials(id);

-- agent_enrollments agent_enrollments_agent_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_enrollments
    ADD CONSTRAINT agent_enrollments_agent_id_organization_id_fkey FOREIGN KEY (agent_id, organization_id) REFERENCES public.agents(id, organization_id);

-- agent_enrollments agent_enrollments_agent_instance_id_agent_id_organization__fkey (fk constraint)
ALTER TABLE ONLY public.agent_enrollments
    ADD CONSTRAINT agent_enrollments_agent_instance_id_agent_id_organization__fkey FOREIGN KEY (agent_instance_id, agent_id, organization_id) REFERENCES public.agent_instances(id, agent_id, organization_id);

-- agent_enrollments agent_enrollments_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_enrollments
    ADD CONSTRAINT agent_enrollments_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- agent_enrollments agent_enrollments_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_enrollments
    ADD CONSTRAINT agent_enrollments_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- agent_instances agent_instances_agent_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_instances
    ADD CONSTRAINT agent_instances_agent_id_organization_id_fkey FOREIGN KEY (agent_id, organization_id) REFERENCES public.agents(id, organization_id);

-- agent_instances agent_instances_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_instances
    ADD CONSTRAINT agent_instances_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- agent_provisioners agent_provisioners_agent_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_provisioners
    ADD CONSTRAINT agent_provisioners_agent_id_organization_id_fkey FOREIGN KEY (agent_id, organization_id) REFERENCES public.agents(id, organization_id);

-- agent_provisioners agent_provisioners_created_by_actor_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_provisioners
    ADD CONSTRAINT agent_provisioners_created_by_actor_id_organization_id_fkey FOREIGN KEY (created_by_actor_id, organization_id) REFERENCES public.management_actors(id, organization_id);

-- agent_provisioners agent_provisioners_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agent_provisioners
    ADD CONSTRAINT agent_provisioners_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- agents agents_created_by_actor_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_created_by_actor_id_organization_id_fkey FOREIGN KEY (created_by_actor_id, organization_id) REFERENCES public.management_actors(id, organization_id);

-- agents agents_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- api_key_creations api_key_creations_api_key_id_organization_id_kind_fkey (fk constraint)
ALTER TABLE ONLY public.api_key_creations
    ADD CONSTRAINT api_key_creations_api_key_id_organization_id_kind_fkey FOREIGN KEY (api_key_id, organization_id, kind) REFERENCES public.api_keys(id, organization_id, kind) ON DELETE CASCADE;

-- api_key_creations api_key_creations_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.api_key_creations
    ADD CONSTRAINT api_key_creations_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- api_key_creations api_key_creations_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.api_key_creations
    ADD CONSTRAINT api_key_creations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- api_keys api_keys_creator_fk (fk constraint)
ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_creator_fk FOREIGN KEY (created_by_membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- api_keys api_keys_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- api_keys api_keys_oauth_client_id_fkey (fk constraint)
ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_oauth_client_id_fkey FOREIGN KEY (oauth_client_id) REFERENCES public.oauth_clients(id);

-- api_keys api_keys_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- api_keys api_keys_revoked_by_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_revoked_by_membership_id_organization_id_fkey FOREIGN KEY (revoked_by_membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- approval_request_claims approval_request_claims_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_claims
    ADD CONSTRAINT approval_request_claims_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- approval_request_claims approval_request_claims_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_claims
    ADD CONSTRAINT approval_request_claims_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- approval_request_claims approval_request_claims_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_claims
    ADD CONSTRAINT approval_request_claims_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- approval_request_escalation_state approval_request_escalation_sta_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_escalation_state
    ADD CONSTRAINT approval_request_escalation_sta_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- approval_request_escalation_state approval_request_escalation_state_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_escalation_state
    ADD CONSTRAINT approval_request_escalation_state_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- approval_request_escalation_team_state approval_request_escalation_tea_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_escalation_team_state
    ADD CONSTRAINT approval_request_escalation_tea_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_request_escalation_state(request_id, organization_id);

-- approval_request_escalation_team_state approval_request_escalation_team_state_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_escalation_team_state
    ADD CONSTRAINT approval_request_escalation_team_state_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- approval_request_notifications approval_request_notification_membership_id_organization_i_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_notifications
    ADD CONSTRAINT approval_request_notification_membership_id_organization_i_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- approval_request_notifications approval_request_notifications_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_notifications
    ADD CONSTRAINT approval_request_notifications_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- approval_request_notifications approval_request_notifications_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_notifications
    ADD CONSTRAINT approval_request_notifications_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- approval_request_pipeline_suspensions approval_request_pipeline_sus_agent_type_pipeline_id_organ_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_pipeline_suspensions
    ADD CONSTRAINT approval_request_pipeline_sus_agent_type_pipeline_id_organ_fkey FOREIGN KEY (agent_pipeline_id, organization_id) REFERENCES public.request_approval_pipelines(id, organization_id);

-- approval_request_pipeline_suspensions approval_request_pipeline_sus_organization_pipeline_id_org_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_pipeline_suspensions
    ADD CONSTRAINT approval_request_pipeline_sus_organization_pipeline_id_org_fkey FOREIGN KEY (organization_pipeline_id, organization_id) REFERENCES public.request_approval_pipelines(id, organization_id);

-- approval_request_pipeline_suspensions approval_request_pipeline_suspe_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_pipeline_suspensions
    ADD CONSTRAINT approval_request_pipeline_suspe_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- approval_request_pipeline_suspensions approval_request_pipeline_suspensions_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_pipeline_suspensions
    ADD CONSTRAINT approval_request_pipeline_suspensions_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- approval_request_reviewers approval_request_reviewers_added_by_membership_id_organiza_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_reviewers
    ADD CONSTRAINT approval_request_reviewers_added_by_membership_id_organiza_fkey FOREIGN KEY (added_by_membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- approval_request_reviewers approval_request_reviewers_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_reviewers
    ADD CONSTRAINT approval_request_reviewers_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- approval_request_reviewers approval_request_reviewers_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_reviewers
    ADD CONSTRAINT approval_request_reviewers_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- approval_request_reviewers approval_request_reviewers_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_reviewers
    ADD CONSTRAINT approval_request_reviewers_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- approval_request_routing approval_request_routing_block_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_routing
    ADD CONSTRAINT approval_request_routing_block_id_organization_id_fkey FOREIGN KEY (block_id, organization_id) REFERENCES public.request_approval_blocks(id, organization_id);

-- approval_request_routing approval_request_routing_escalation_path_revision_id_organ_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_routing
    ADD CONSTRAINT approval_request_routing_escalation_path_revision_id_organ_fkey FOREIGN KEY (escalation_path_revision_id, organization_id) REFERENCES public.escalation_paths(id, organization_id);

-- approval_request_routing approval_request_routing_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_routing
    ADD CONSTRAINT approval_request_routing_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- approval_request_routing approval_request_routing_pipeline_revision_id_organization_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_routing
    ADD CONSTRAINT approval_request_routing_pipeline_revision_id_organization_fkey FOREIGN KEY (pipeline_revision_id, organization_id) REFERENCES public.request_approval_pipelines(id, organization_id);

-- approval_request_routing approval_request_routing_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_routing
    ADD CONSTRAINT approval_request_routing_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- approval_request_routing_team_policies approval_request_routing_team_p_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_routing_team_policies
    ADD CONSTRAINT approval_request_routing_team_p_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- approval_request_routing_team_policies approval_request_routing_team_policies_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_routing_team_policies
    ADD CONSTRAINT approval_request_routing_team_policies_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- approval_request_routing_team_policies approval_request_routing_team_policy_revision_id_organizat_fkey (fk constraint)
ALTER TABLE ONLY public.approval_request_routing_team_policies
    ADD CONSTRAINT approval_request_routing_team_policy_revision_id_organizat_fkey FOREIGN KEY (policy_revision_id, organization_id, team_id) REFERENCES public.team_escalation_policies(id, organization_id, team_id);

-- approval_requests approval_requests_agent_instance_id_agent_id_organization__fkey (fk constraint)
ALTER TABLE ONLY public.approval_requests
    ADD CONSTRAINT approval_requests_agent_instance_id_agent_id_organization__fkey FOREIGN KEY (agent_instance_id, agent_id, organization_id) REFERENCES public.agent_instances(id, agent_id, organization_id);

-- approval_requests approval_requests_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.approval_requests
    ADD CONSTRAINT approval_requests_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- contact_methods contact_methods_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.contact_methods
    ADD CONSTRAINT contact_methods_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- contact_methods contact_methods_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.contact_methods
    ADD CONSTRAINT contact_methods_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- decision_operations decision_operations_decision_id_fkey (fk constraint)
ALTER TABLE ONLY public.decision_operations
    ADD CONSTRAINT decision_operations_decision_id_fkey FOREIGN KEY (decision_id) REFERENCES public.decisions(id);

-- decision_operations decision_operations_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.decision_operations
    ADD CONSTRAINT decision_operations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- decision_operations decision_operations_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.decision_operations
    ADD CONSTRAINT decision_operations_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- decisions decisions_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.decisions
    ADD CONSTRAINT decisions_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- decisions decisions_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.decisions
    ADD CONSTRAINT decisions_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- decisions decisions_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.decisions
    ADD CONSTRAINT decisions_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- directory_connections directory_connections_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.directory_connections
    ADD CONSTRAINT directory_connections_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- directory_object_links directory_object_links_directory_connection_id_organizatio_fkey (fk constraint)
ALTER TABLE ONLY public.directory_object_links
    ADD CONSTRAINT directory_object_links_directory_connection_id_organizatio_fkey FOREIGN KEY (directory_connection_id, organization_id) REFERENCES public.directory_connections(id, organization_id);

-- directory_object_links directory_object_links_membership_id_fkey (fk constraint)
ALTER TABLE ONLY public.directory_object_links
    ADD CONSTRAINT directory_object_links_membership_id_fkey FOREIGN KEY (membership_id) REFERENCES public.memberships(id);

-- directory_object_links directory_object_links_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.directory_object_links
    ADD CONSTRAINT directory_object_links_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- directory_object_links directory_object_links_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.directory_object_links
    ADD CONSTRAINT directory_object_links_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- directory_object_links directory_object_links_role_id_fkey (fk constraint)
ALTER TABLE ONLY public.directory_object_links
    ADD CONSTRAINT directory_object_links_role_id_fkey FOREIGN KEY (role_id) REFERENCES public.roles(id);

-- directory_object_links directory_object_links_team_id_fkey (fk constraint)
ALTER TABLE ONLY public.directory_object_links
    ADD CONSTRAINT directory_object_links_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id);

-- directory_object_links directory_object_links_team_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.directory_object_links
    ADD CONSTRAINT directory_object_links_team_id_organization_id_fkey FOREIGN KEY (team_id, organization_id) REFERENCES public.teams(id, organization_id);

-- discord_connections discord_connections_installed_by_membership_id_organizatio_fkey (fk constraint)
ALTER TABLE ONLY public.discord_connections
    ADD CONSTRAINT discord_connections_installed_by_membership_id_organizatio_fkey FOREIGN KEY (installed_by_membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- discord_connections discord_connections_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.discord_connections
    ADD CONSTRAINT discord_connections_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- discord_oauth_states discord_oauth_states_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.discord_oauth_states
    ADD CONSTRAINT discord_oauth_states_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- discord_oauth_states discord_oauth_states_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.discord_oauth_states
    ADD CONSTRAINT discord_oauth_states_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- discord_oauth_states discord_oauth_states_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.discord_oauth_states
    ADD CONSTRAINT discord_oauth_states_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- escalation_paths escalation_paths_created_by_actor_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.escalation_paths
    ADD CONSTRAINT escalation_paths_created_by_actor_id_organization_id_fkey FOREIGN KEY (created_by_actor_id, organization_id) REFERENCES public.management_actors(id, organization_id);

-- escalation_paths escalation_paths_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.escalation_paths
    ADD CONSTRAINT escalation_paths_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- external_identities external_identities_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.external_identities
    ADD CONSTRAINT external_identities_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- invitations invitations_accepted_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_accepted_membership_id_organization_id_fkey FOREIGN KEY (accepted_membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- invitations invitations_invited_by_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_invited_by_membership_id_organization_id_fkey FOREIGN KEY (invited_by_membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- invitations invitations_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- local_credentials local_credentials_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.local_credentials
    ADD CONSTRAINT local_credentials_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- login_tickets login_tickets_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.login_tickets
    ADD CONSTRAINT login_tickets_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

-- management_actors management_actors_api_key_id_organization_id_api_key_kind_fkey (fk constraint)
ALTER TABLE ONLY public.management_actors
    ADD CONSTRAINT management_actors_api_key_id_organization_id_api_key_kind_fkey FOREIGN KEY (api_key_id, organization_id, api_key_kind) REFERENCES public.api_keys(id, organization_id, kind);

-- management_actors management_actors_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.management_actors
    ADD CONSTRAINT management_actors_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- management_actors management_actors_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.management_actors
    ADD CONSTRAINT management_actors_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- membership_roles membership_roles_membership_id_fkey (fk constraint)
ALTER TABLE ONLY public.membership_roles
    ADD CONSTRAINT membership_roles_membership_id_fkey FOREIGN KEY (membership_id) REFERENCES public.memberships(id);

-- membership_teams membership_teams_membership_id_fkey (fk constraint)
ALTER TABLE ONLY public.membership_teams
    ADD CONSTRAINT membership_teams_membership_id_fkey FOREIGN KEY (membership_id) REFERENCES public.memberships(id);

-- membership_teams membership_teams_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.membership_teams
    ADD CONSTRAINT membership_teams_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- membership_teams membership_teams_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.membership_teams
    ADD CONSTRAINT membership_teams_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- membership_teams membership_teams_team_id_fkey (fk constraint)
ALTER TABLE ONLY public.membership_teams
    ADD CONSTRAINT membership_teams_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id);

-- membership_teams membership_teams_team_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.membership_teams
    ADD CONSTRAINT membership_teams_team_id_organization_id_fkey FOREIGN KEY (team_id, organization_id) REFERENCES public.teams(id, organization_id);

-- memberships memberships_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- memberships memberships_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- notification_rules notification_rules_contact_method_id_fkey (fk constraint)
ALTER TABLE ONLY public.notification_rules
    ADD CONSTRAINT notification_rules_contact_method_id_fkey FOREIGN KEY (contact_method_id) REFERENCES public.contact_methods(id) ON DELETE CASCADE;

-- notification_rules notification_rules_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.notification_rules
    ADD CONSTRAINT notification_rules_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- oauth_authorization_codes oauth_authorization_codes_api_key_id_fkey (fk constraint)
ALTER TABLE ONLY public.oauth_authorization_codes
    ADD CONSTRAINT oauth_authorization_codes_api_key_id_fkey FOREIGN KEY (api_key_id) REFERENCES public.api_keys(id);

-- oauth_authorization_codes oauth_authorization_codes_client_id_fkey (fk constraint)
ALTER TABLE ONLY public.oauth_authorization_codes
    ADD CONSTRAINT oauth_authorization_codes_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.oauth_clients(id);

-- oauth_authorization_codes oauth_authorization_codes_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.oauth_authorization_codes
    ADD CONSTRAINT oauth_authorization_codes_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- onboarding_sessions onboarding_sessions_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.onboarding_sessions
    ADD CONSTRAINT onboarding_sessions_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- onboarding_sessions onboarding_sessions_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.onboarding_sessions
    ADD CONSTRAINT onboarding_sessions_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- organization_policies organization_policies_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.organization_policies
    ADD CONSTRAINT organization_policies_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- outbox_events outbox_events_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.outbox_events
    ADD CONSTRAINT outbox_events_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- password_resets password_resets_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.password_resets
    ADD CONSTRAINT password_resets_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

-- permission_policies permission_policies_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.permission_policies
    ADD CONSTRAINT permission_policies_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- permission_policy_commands permission_policy_commands_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.permission_policy_commands
    ADD CONSTRAINT permission_policy_commands_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- permission_policy_commands permission_policy_commands_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.permission_policy_commands
    ADD CONSTRAINT permission_policy_commands_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- request_approval_blocks request_approval_blocks_created_by_actor_id_organization_i_fkey (fk constraint)
ALTER TABLE ONLY public.request_approval_blocks
    ADD CONSTRAINT request_approval_blocks_created_by_actor_id_organization_i_fkey FOREIGN KEY (created_by_actor_id, organization_id) REFERENCES public.management_actors(id, organization_id);

-- request_approval_blocks request_approval_blocks_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.request_approval_blocks
    ADD CONSTRAINT request_approval_blocks_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- request_approval_pipelines request_approval_pipelines_created_by_actor_id_organizatio_fkey (fk constraint)
ALTER TABLE ONLY public.request_approval_pipelines
    ADD CONSTRAINT request_approval_pipelines_created_by_actor_id_organizatio_fkey FOREIGN KEY (created_by_actor_id, organization_id) REFERENCES public.management_actors(id, organization_id);

-- request_approval_pipelines request_approval_pipelines_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.request_approval_pipelines
    ADD CONSTRAINT request_approval_pipelines_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- role_assignments role_assignments_granted_by_actor_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.role_assignments
    ADD CONSTRAINT role_assignments_granted_by_actor_id_organization_id_fkey FOREIGN KEY (granted_by_actor_id, organization_id) REFERENCES public.management_actors(id, organization_id);

-- role_assignments role_assignments_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.role_assignments
    ADD CONSTRAINT role_assignments_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- role_assignments role_assignments_policy_fk (fk constraint)
ALTER TABLE ONLY public.role_assignments
    ADD CONSTRAINT role_assignments_policy_fk FOREIGN KEY (permission_policy_id, organization_id) REFERENCES public.permission_policies(id, organization_id);

-- role_assignments role_assignments_role_id_fkey (fk constraint)
ALTER TABLE ONLY public.role_assignments
    ADD CONSTRAINT role_assignments_role_id_fkey FOREIGN KEY (role_id) REFERENCES public.roles(id);

-- roles roles_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- sessions sessions_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- sessions sessions_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- slack_connections slack_connections_installed_by_membership_id_organization__fkey (fk constraint)
ALTER TABLE ONLY public.slack_connections
    ADD CONSTRAINT slack_connections_installed_by_membership_id_organization__fkey FOREIGN KEY (installed_by_membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- slack_connections slack_connections_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.slack_connections
    ADD CONSTRAINT slack_connections_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- slack_email_fallbacks slack_email_fallbacks_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.slack_email_fallbacks
    ADD CONSTRAINT slack_email_fallbacks_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- slack_email_fallbacks slack_email_fallbacks_request_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.slack_email_fallbacks
    ADD CONSTRAINT slack_email_fallbacks_request_id_organization_id_fkey FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id);

-- slack_email_fallbacks slack_email_fallbacks_user_id_fkey (fk constraint)
ALTER TABLE ONLY public.slack_email_fallbacks
    ADD CONSTRAINT slack_email_fallbacks_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- slack_install_states slack_install_states_membership_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.slack_install_states
    ADD CONSTRAINT slack_install_states_membership_id_organization_id_fkey FOREIGN KEY (membership_id, organization_id) REFERENCES public.memberships(id, organization_id);

-- slack_install_states slack_install_states_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.slack_install_states
    ADD CONSTRAINT slack_install_states_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- team_escalation_policies team_escalation_policies_created_by_actor_id_organization__fkey (fk constraint)
ALTER TABLE ONLY public.team_escalation_policies
    ADD CONSTRAINT team_escalation_policies_created_by_actor_id_organization__fkey FOREIGN KEY (created_by_actor_id, organization_id) REFERENCES public.management_actors(id, organization_id);

-- team_escalation_policies team_escalation_policies_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.team_escalation_policies
    ADD CONSTRAINT team_escalation_policies_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- teams teams_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- tool_catalog_sources tool_catalog_sources_agent_instance_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.tool_catalog_sources
    ADD CONSTRAINT tool_catalog_sources_agent_instance_id_organization_id_fkey FOREIGN KEY (agent_instance_id, organization_id) REFERENCES public.agent_instances(id, organization_id);

-- tool_catalog_sources tool_catalog_sources_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.tool_catalog_sources
    ADD CONSTRAINT tool_catalog_sources_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- tool_catalog_tools tool_catalog_tools_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.tool_catalog_tools
    ADD CONSTRAINT tool_catalog_tools_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- tool_catalog_tools tool_catalog_tools_source_id_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.tool_catalog_tools
    ADD CONSTRAINT tool_catalog_tools_source_id_organization_id_fkey FOREIGN KEY (source_id, organization_id) REFERENCES public.tool_catalog_sources(id, organization_id);

-- tool_presentations tool_presentations_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.tool_presentations
    ADD CONSTRAINT tool_presentations_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;

-- webhook_endpoint_revisions webhook_endpoint_revisions_created_by_actor_id_organizatio_fkey (fk constraint)
ALTER TABLE ONLY public.webhook_endpoint_revisions
    ADD CONSTRAINT webhook_endpoint_revisions_created_by_actor_id_organizatio_fkey FOREIGN KEY (created_by_actor_id, organization_id) REFERENCES public.management_actors(id, organization_id);

-- webhook_endpoint_revisions webhook_endpoint_revisions_organization_id_endpoint_key_fkey (fk constraint)
ALTER TABLE ONLY public.webhook_endpoint_revisions
    ADD CONSTRAINT webhook_endpoint_revisions_organization_id_endpoint_key_fkey FOREIGN KEY (organization_id, endpoint_key) REFERENCES public.webhook_endpoint_secrets(organization_id, endpoint_key);

-- webhook_endpoint_revisions webhook_endpoint_revisions_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.webhook_endpoint_revisions
    ADD CONSTRAINT webhook_endpoint_revisions_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- webhook_endpoint_secrets webhook_endpoint_secrets_organization_id_fkey (fk constraint)
ALTER TABLE ONLY public.webhook_endpoint_secrets
    ADD CONSTRAINT webhook_endpoint_secrets_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id);

-- webhook_endpoint_secrets webhook_endpoint_secrets_rotated_by_actor_id_organization__fkey (fk constraint)
ALTER TABLE ONLY public.webhook_endpoint_secrets
    ADD CONSTRAINT webhook_endpoint_secrets_rotated_by_actor_id_organization__fkey FOREIGN KEY (rotated_by_actor_id, organization_id) REFERENCES public.management_actors(id, organization_id);


--
--

-- The built-in roles, whose identifiers domain/roles.go shares. The boot
-- reconciler overwrites their permissions from the registry.
INSERT INTO public.roles (id, organization_id, key, name, description, permissions, bindable_scope_kinds, built_in, created_at, updated_at) VALUES ('00000000-0000-4000-8000-000000000001', NULL, 'owner', 'Owner', 'Full control, including billing, deleting the organization, and deciding outside routing.', '{escalation_path.activate,pipeline.read,organization.read,team.member.write,audit.read,agent.credential.issue,webhook.read,team.write,connection.write,request.decide,organization.settings.write,pipeline.activate,webhook.write,permission_policy.read,request.read,request.decide.unrouted,request.claim,gateway.write,escalation_path.read,agent.credential.revoke,permission_policy.write,membership.invite,membership.write,agent.write,membership.read,gateway.read,organization.delete,escalation_path.write,pipeline.write,agent.read,team.read,connection.read}', '{organization}', true, '2026-09-24 13:18:15.119722+00', '2026-09-24 13:18:15.119722+00');
INSERT INTO public.roles (id, organization_id, key, name, description, permissions, bindable_scope_kinds, built_in, created_at, updated_at) VALUES ('00000000-0000-4000-8000-000000000002', NULL, 'admin', 'Admin', 'Connects agents, writes pipelines and paths, and manages members and teams.', '{escalation_path.activate,pipeline.read,organization.read,audit.read,team.member.write,agent.credential.issue,connection.write,webhook.read,team.write,organization.settings.write,pipeline.activate,webhook.write,permission_policy.read,gateway.write,escalation_path.read,agent.credential.revoke,permission_policy.write,membership.invite,agent.write,membership.write,membership.read,gateway.read,escalation_path.write,pipeline.write,agent.read,connection.read,team.read}', '{organization,team,agent}', true, '2026-09-24 13:18:15.119722+00', '2026-09-24 13:18:15.119722+00');
INSERT INTO public.roles (id, organization_id, key, name, description, permissions, bindable_scope_kinds, built_in, created_at, updated_at) VALUES ('00000000-0000-4000-8000-000000000003', NULL, 'reviewer', 'Reviewer', 'Reviews and decides the requests routed to them.', '{organization.read,request.decide,request.read,request.claim,agent.read,team.read}', '{organization}', true, '2026-09-24 13:18:15.119722+00', '2026-09-24 13:18:15.119722+00');
INSERT INTO public.roles (id, organization_id, key, name, description, permissions, bindable_scope_kinds, built_in, created_at, updated_at) VALUES ('00000000-0000-4000-8000-000000000004', NULL, 'auditor', 'Auditor', 'Reads requests, decisions, roles, and the audit log without changing anything.', '{organization.read,audit.read,permission_policy.read,request.read,agent.read,team.read}', '{organization}', true, '2026-09-24 13:18:15.119722+00', '2026-09-24 13:18:15.119722+00');
INSERT INTO public.roles (id, organization_id, key, name, description, permissions, bindable_scope_kinds, built_in, created_at, updated_at) VALUES ('00000000-0000-4000-8000-000000000005', NULL, 'agent', 'Agent', 'What every agent identity holds: asking for approval and reading its own requests.', '{request.create,request.read}', '{organization}', true, '2026-09-24 13:18:15.119722+00', '2026-09-24 13:18:15.119722+00');
