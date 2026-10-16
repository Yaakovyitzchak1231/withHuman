-- One row per deployment: the random installation ID the heartbeat
-- reports, and what the last heartbeat answered about newer releases.
-- Every replica reads the same row, so a deployment counts once however
-- many processes it runs. The heartbeat claims a send by stamping
-- heartbeat_claimed_at and the version it reports, so only one replica
-- sends per day and an upgrade sends again straight away.

CREATE TABLE public.installations (
    id uuid DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    heartbeat_claimed_at timestamp with time zone,
    heartbeat_version text,
    checked_at timestamp with time zone,
    latest_version text,
    advisories jsonb DEFAULT '[]'::jsonb NOT NULL,
    CONSTRAINT installations_advisories_array CHECK (jsonb_typeof(advisories) = 'array')
);

CREATE UNIQUE INDEX installations_singleton_idx ON public.installations ((true));

INSERT INTO public.installations DEFAULT VALUES;
