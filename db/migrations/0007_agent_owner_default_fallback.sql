-- Asking the agent's owner no longer needs a path beside it: without one
-- the route falls back to the pipeline default, and an agent_owner
-- snapshot may pin no fallback path at all.
ALTER TABLE request_approval_pipelines DROP CONSTRAINT request_approval_pipelines_default_ask_agent_owner_check;

ALTER TABLE approval_request_routing DROP CONSTRAINT approval_request_routing_path_check;
ALTER TABLE approval_request_routing ADD CONSTRAINT approval_request_routing_path_check
    CHECK (CASE outcome
        WHEN 'escalated' THEN escalation_path_key IS NOT NULL AND escalation_path_revision_id IS NOT NULL
        WHEN 'default_queue' THEN escalation_path_key IS NULL AND escalation_path_revision_id IS NULL
        ELSE escalation_path_revision_id IS NULL OR escalation_path_key IS NOT NULL
    END);
