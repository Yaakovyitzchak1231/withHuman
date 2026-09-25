-- AAP asynchronous mode: an instance may carry a verified webhook receiver,
-- and every request of such an instance that becomes terminal records one
-- request.resolved event in the transaction that commits its decision. The
-- worker delivers pending events with backoff until the retry window ends.

CREATE TABLE public.agent_instance_deliveries (
    agent_instance_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    type text NOT NULL,
    url text NOT NULL,
    registration_id uuid NOT NULL,
    sealed_signing_secret bytea NOT NULL,
    verified_at timestamp with time zone NOT NULL,
    CONSTRAINT agent_instance_deliveries_pkey PRIMARY KEY (agent_instance_id),
    CONSTRAINT agent_instance_deliveries_type_check CHECK ((type = 'webhook'::text)),
    CONSTRAINT agent_instance_deliveries_agent_instance_id_organization_id_fkey
        FOREIGN KEY (agent_instance_id, organization_id) REFERENCES public.agent_instances(id, organization_id)
);

CREATE TABLE public.request_resolved_events (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    agent_instance_id uuid NOT NULL,
    request_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    retry_until timestamp with time zone NOT NULL,
    next_attempt_at timestamp with time zone NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    last_attempted_at timestamp with time zone,
    last_status_code integer,
    delivered_at timestamp with time zone,
    abandoned_at timestamp with time zone,
    CONSTRAINT request_resolved_events_pkey PRIMARY KEY (id),
    CONSTRAINT request_resolved_events_request_id_key UNIQUE (request_id),
    CONSTRAINT request_resolved_events_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT request_resolved_events_request_id_organization_id_fkey
        FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id),
    CONSTRAINT request_resolved_events_agent_instance_id_organization_id_fkey
        FOREIGN KEY (agent_instance_id, organization_id) REFERENCES public.agent_instances(id, organization_id)
);

CREATE INDEX request_resolved_events_due_idx ON public.request_resolved_events (next_attempt_at)
    WHERE delivered_at IS NULL AND abandoned_at IS NULL;

-- A provisioner can delete an instance through AAP. The row stays for the
-- record, disabled and with its credentials revoked; deleted_at is the
-- deletion record that makes a repeated DELETE succeed and a replayed
-- provisioning answer instance_deleted.
ALTER TABLE public.agent_instances ADD COLUMN deleted_at timestamp with time zone;
ALTER TABLE public.agent_instances ADD CONSTRAINT agent_instances_deleted_disabled
    CHECK (((deleted_at IS NULL) OR (status = 'disabled'::text)));

-- Idempotency for AAP instance provisioning, scoped to the provisioner.
-- The instance and credential IDs are reserved when a key is first
-- claimed, so a retry verifies and creates the same instance. The
-- credential secret is never stored: it is derived from the provisioner's
-- own secret and credential_salt, so only a caller presenting the same
-- provisioner token can recover it, and dropping the salt when the key
-- expires makes it unrecoverable. claimed_until is the lease of an
-- attempt in progress; completed_at marks the committed instance.
CREATE TABLE public.agent_instance_provisioning_keys (
    provisioner_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    organization_id uuid NOT NULL,
    intent_hash bytea NOT NULL,
    agent_instance_id uuid NOT NULL,
    credential_id uuid NOT NULL,
    credential_salt bytea NOT NULL,
    claimed_until timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT agent_instance_provisioning_keys_pkey PRIMARY KEY (provisioner_id, idempotency_key),
    CONSTRAINT agent_instance_provisioning_keys_key_check CHECK ((length(idempotency_key) BETWEEN 1 AND 255)),
    CONSTRAINT agent_instance_provisioning_keys_provisioner_id_fkey
        FOREIGN KEY (provisioner_id) REFERENCES public.agent_provisioners(id)
);

-- Idempotency for AAP instance updates, scoped to the provisioner, the
-- instance and the operation. A completed key keeps the response it
-- answered so a replay returns it without applying the update again.
CREATE TABLE public.agent_instance_update_keys (
    provisioner_id uuid NOT NULL,
    agent_instance_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    organization_id uuid NOT NULL,
    intent_hash bytea NOT NULL,
    claimed_until timestamp with time zone,
    response jsonb,
    completed_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT agent_instance_update_keys_pkey PRIMARY KEY (provisioner_id, agent_instance_id, idempotency_key),
    CONSTRAINT agent_instance_update_keys_key_check CHECK ((length(idempotency_key) BETWEEN 1 AND 255)),
    CONSTRAINT agent_instance_update_keys_completed_check CHECK (((completed_at IS NULL) = (response IS NULL))),
    CONSTRAINT agent_instance_update_keys_provisioner_id_fkey
        FOREIGN KEY (provisioner_id) REFERENCES public.agent_provisioners(id),
    CONSTRAINT agent_instance_update_keys_agent_instance_id_organization_id_fkey
        FOREIGN KEY (agent_instance_id, organization_id) REFERENCES public.agent_instances(id, organization_id)
);

CREATE INDEX agent_instance_provisioning_keys_expires_idx ON public.agent_instance_provisioning_keys (expires_at);
CREATE INDEX agent_instance_update_keys_expires_idx ON public.agent_instance_update_keys (expires_at);
