-- AAP consume and outcome: the requesting instance marks an approved
-- request used right before it runs the call, so one approval runs one
-- call, and may then report how the call went. A consumption is a
-- separate row because terminal requests and decisions are immutable.
-- It is written once, may gain its outcome once, and never changes after
-- that. These are not reviewer claims (approval_request_claims), which
-- coordinate who reviews a pending request.
CREATE TABLE public.approval_consumptions (
    request_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    consumed_at timestamp with time zone NOT NULL,
    outcome_status text,
    outcome_note text,
    outcome_reported_at timestamp with time zone,
    CONSTRAINT approval_consumptions_pkey PRIMARY KEY (request_id),
    CONSTRAINT approval_consumptions_request_id_organization_id_fkey
        FOREIGN KEY (request_id, organization_id) REFERENCES public.approval_requests(id, organization_id),
    CONSTRAINT approval_consumptions_idempotency_key_check CHECK ((length(idempotency_key) > 0)),
    CONSTRAINT approval_consumptions_outcome_status_check
        CHECK ((outcome_status = ANY (ARRAY['succeeded'::text, 'failed'::text]))),
    CONSTRAINT approval_consumptions_outcome_note_check
        CHECK (((outcome_note IS NULL) OR (char_length(outcome_note) BETWEEN 1 AND 1000))),
    CONSTRAINT approval_consumptions_outcome_complete CHECK (
        ((outcome_status IS NULL) AND (outcome_note IS NULL) AND (outcome_reported_at IS NULL))
        OR ((outcome_status IS NOT NULL) AND (outcome_reported_at IS NOT NULL)))
);

-- Only an approved request can be consumed, a consumption only gains its
-- outcome, once, and rows are never deleted outside the tenant purge.
CREATE FUNCTION public.protect_approval_consumption() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'approval consumptions are append-only';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NOT EXISTS (SELECT 1 FROM approval_requests
                        WHERE id = NEW.request_id AND organization_id = NEW.organization_id AND status = 'approved') THEN
            RAISE EXCEPTION 'only an approved request can be consumed';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.outcome_status IS NOT NULL
        OR NEW.request_id IS DISTINCT FROM OLD.request_id
        OR NEW.organization_id IS DISTINCT FROM OLD.organization_id
        OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
        OR NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
        RAISE EXCEPTION 'an approval consumption only gains its outcome, once';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER approval_consumptions_protect BEFORE INSERT OR UPDATE OR DELETE ON public.approval_consumptions
    FOR EACH ROW EXECUTE FUNCTION public.protect_approval_consumption();

-- An approval of a request whose instance has webhook delivery at
-- decision time stays valid for 30 minutes: the adapter learns of it
-- from a delivery that may arrive late. Every other approval stays valid
-- for five minutes.
CREATE OR REPLACE FUNCTION public.set_approval_decision_expiry() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.status <> 'approved' THEN
        NEW.expires_at := NULL;
    ELSIF EXISTS (SELECT 1 FROM approval_requests r
                    JOIN agent_instance_deliveries ad
                      ON ad.agent_instance_id = r.agent_instance_id AND ad.organization_id = r.organization_id
                   WHERE r.id = NEW.request_id AND r.organization_id = NEW.organization_id) THEN
        NEW.expires_at := NEW.decided_at + interval '30 minutes';
    ELSE
        NEW.expires_at := NEW.decided_at + interval '5 minutes';
    END IF;
    RETURN NEW;
END;
$$;

ALTER TABLE public.decisions DROP CONSTRAINT decisions_approval_expiry;
ALTER TABLE public.decisions ADD CONSTRAINT decisions_approval_expiry CHECK (
    ((status = 'approved'::text) AND (expires_at IS NOT NULL)
        AND ((expires_at = (decided_at + '00:05:00'::interval)) OR (expires_at = (decided_at + '00:30:00'::interval))))
    OR ((status <> 'approved'::text) AND (expires_at IS NULL)));
