-- Personal agents: an agent is either a service the organization runs
-- (a support bot, shared by every instance) or a personal agent people
-- run for themselves (a coding assistant on a laptop). An instance of a
-- personal agent belongs to the member who enrolled it and acts on
-- their behalf; an instance of a service agent belongs to nobody.
ALTER TABLE agents ADD COLUMN kind text DEFAULT 'service' NOT NULL;
ALTER TABLE agents ALTER COLUMN kind DROP DEFAULT;
ALTER TABLE agents ADD CONSTRAINT agents_kind_check CHECK (kind IN ('service', 'personal'));

ALTER TABLE agent_instances ADD COLUMN membership_id uuid;
ALTER TABLE agent_instances
    ADD CONSTRAINT agent_instances_membership_id_organization_id_fkey
    FOREIGN KEY (membership_id, organization_id) REFERENCES memberships(id, organization_id);
CREATE INDEX agent_instances_owner_idx ON agent_instances (organization_id, membership_id)
    WHERE membership_id IS NOT NULL;

-- The kind is chosen once: instances, grants and the gateway's view of an
-- agent all follow from it, so it never flips under them.
CREATE FUNCTION enforce_agent_kind_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.kind IS DISTINCT FROM OLD.kind THEN
        RAISE EXCEPTION 'agent kind cannot change';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER agents_kind_immutable BEFORE UPDATE OF kind ON agents
    FOR EACH ROW EXECUTE FUNCTION enforce_agent_kind_immutable();

-- An owner exists exactly when the agent is personal.
CREATE FUNCTION enforce_agent_instance_owner() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    agent_kind text;
BEGIN
    SELECT kind INTO agent_kind FROM agents WHERE id = NEW.agent_id AND organization_id = NEW.organization_id;
    IF agent_kind = 'personal' AND NEW.membership_id IS NULL THEN
        RAISE EXCEPTION 'an instance of a personal agent needs an owner';
    END IF;
    IF agent_kind = 'service' AND NEW.membership_id IS NOT NULL THEN
        RAISE EXCEPTION 'an instance of a service agent has no owner';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER agent_instances_owner BEFORE INSERT OR UPDATE OF membership_id, agent_id ON agent_instances
    FOR EACH ROW EXECUTE FUNCTION enforce_agent_instance_owner();

-- A request made by a personal instance names the member it acts for,
-- copied from the instance when the request is created.
ALTER TABLE approval_requests ADD COLUMN requester_membership_id uuid;
ALTER TABLE approval_requests
    ADD CONSTRAINT approval_requests_requester_membership_id_organization_id_fkey
    FOREIGN KEY (requester_membership_id, organization_id) REFERENCES memberships(id, organization_id);
CREATE INDEX approval_requests_requester_idx ON approval_requests (organization_id, requester_membership_id)
    WHERE requester_membership_id IS NOT NULL;
