-- Creating an instance with AAP webhook delivery from the app takes two
-- steps, because the receiver must hold the signing secret before it can
-- answer the verification. The first step generates the registration ID
-- (the row's id) and the signing secret, shows both once, and keeps the
-- secret sealed here, bound to the agent and the actor who started it.
-- The instance ID is reserved up front, so every verification attempt
-- names the instance it would create. The second step verifies the
-- receiver and deletes the row in the transaction that creates the
-- instance. Expired rows are pruned with the rest of the AAP state.
CREATE TABLE public.agent_instance_registrations (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    agent_instance_id uuid NOT NULL,
    actor_type text NOT NULL,
    actor_id uuid NOT NULL,
    sealed_signing_secret bytea NOT NULL,
    created_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT agent_instance_registrations_pkey PRIMARY KEY (id),
    CONSTRAINT agent_instance_registrations_agent_instance_id_key UNIQUE (agent_instance_id),
    CONSTRAINT agent_instance_registrations_actor_type_check
        CHECK ((actor_type = ANY (ARRAY['human'::text, 'api_key'::text]))),
    CONSTRAINT agent_instance_registrations_expiry_check CHECK ((expires_at > created_at)),
    CONSTRAINT agent_instance_registrations_agent_id_organization_id_fkey
        FOREIGN KEY (agent_id, organization_id) REFERENCES public.agents(id, organization_id)
);

CREATE INDEX agent_instance_registrations_agent_idx ON public.agent_instance_registrations (agent_id, organization_id);
CREATE INDEX agent_instance_registrations_expires_idx ON public.agent_instance_registrations (expires_at);
