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
