-- The agent-owner route: a human exit may ask the member who owns the
-- personal agent instance that raised the request, with an escalation
-- path as the fallback for requests that have no owner able to decide.
-- Owners always may decide their own agents' requests, so the policy
-- that once forbade it goes.
ALTER TABLE organization_policies DROP COLUMN allow_self_approval;

-- A revision's default may ask the agent's owner; the default path is
-- then the fallback, so it is required.
ALTER TABLE request_approval_pipelines ADD COLUMN default_ask_agent_owner boolean DEFAULT false NOT NULL;
ALTER TABLE request_approval_pipelines ADD CONSTRAINT request_approval_pipelines_default_ask_agent_owner_check
    CHECK (NOT default_ask_agent_owner OR default_escalation_path IS NOT NULL);

CREATE OR REPLACE FUNCTION public.protect_request_approval_pipeline_revision() RETURNS trigger
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
       OR NEW.default_ask_agent_owner IS DISTINCT FROM OLD.default_ask_agent_owner
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

-- An agent_owner snapshot names the owner it admits and still pins the
-- fallback path (its key always, its revision when the path had an
-- active one), so a later handoff runs the document active when the
-- request arrived. agent_owner_fallback says why an owner route did not
-- reach the owner at routing time.
ALTER TABLE approval_request_routing ADD COLUMN agent_owner_membership_id uuid;
ALTER TABLE approval_request_routing ADD COLUMN agent_owner_fallback text;
ALTER TABLE approval_request_routing
    ADD CONSTRAINT approval_request_routing_agent_owner_membership_fkey
    FOREIGN KEY (agent_owner_membership_id, organization_id) REFERENCES memberships(id, organization_id);
ALTER TABLE approval_request_routing DROP CONSTRAINT approval_request_routing_check;
ALTER TABLE approval_request_routing DROP CONSTRAINT approval_request_routing_check1;
ALTER TABLE approval_request_routing DROP CONSTRAINT approval_request_routing_outcome_check;
ALTER TABLE approval_request_routing ADD CONSTRAINT approval_request_routing_outcome_check
    CHECK (outcome IN ('agent_owner', 'escalated', 'default_queue'));
ALTER TABLE approval_request_routing ADD CONSTRAINT approval_request_routing_path_check
    CHECK (CASE outcome
        WHEN 'escalated' THEN escalation_path_key IS NOT NULL AND escalation_path_revision_id IS NOT NULL
        WHEN 'default_queue' THEN escalation_path_key IS NULL AND escalation_path_revision_id IS NULL
        ELSE escalation_path_key IS NOT NULL
    END);
ALTER TABLE approval_request_routing ADD CONSTRAINT approval_request_routing_agent_owner_check
    CHECK ((outcome = 'agent_owner') = (agent_owner_membership_id IS NOT NULL));
ALTER TABLE approval_request_routing ADD CONSTRAINT approval_request_routing_agent_owner_fallback_check
    CHECK (agent_owner_fallback IS NULL
        OR (outcome <> 'agent_owner'
            AND agent_owner_fallback IN ('no_agent_owner', 'agent_owner_inactive', 'agent_owner_cannot_decide')));
CREATE INDEX approval_request_routing_agent_owner_idx ON approval_request_routing (organization_id, agent_owner_membership_id)
    WHERE agent_owner_membership_id IS NOT NULL;

-- A handoff moves an agent_owner request to its pinned fallback when the
-- owner is suspended or deprovisioned. The snapshot row stays as it was;
-- this row says it no longer reaches the owner.
CREATE TABLE approval_request_agent_owner_handoffs (
    request_id uuid NOT NULL PRIMARY KEY REFERENCES approval_request_routing(request_id),
    organization_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    FOREIGN KEY (request_id, organization_id) REFERENCES approval_requests(id, organization_id)
);
CREATE TRIGGER approval_request_agent_owner_handoffs_append_only BEFORE DELETE OR UPDATE ON approval_request_agent_owner_handoffs
    FOR EACH ROW EXECUTE FUNCTION public.reject_record_mutation();
