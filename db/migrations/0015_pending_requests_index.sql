-- The pending backlog metric counts every organization's pending requests
-- and reads the oldest one's age on each collection. The queue index
-- leads with organization_id, so index the pending rows on their own.
CREATE INDEX approval_requests_pending_created_idx ON public.approval_requests USING btree (created_at) WHERE (status = 'pending'::text);
