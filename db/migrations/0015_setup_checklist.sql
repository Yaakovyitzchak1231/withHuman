-- The sidebar's Finish setup card now follows what the organization has
-- set up (a connected agent, approval rules, a decided request) instead
-- of whether someone finished the guided flow, so the finish stamp has
-- no reader left.

ALTER TABLE public.organizations DROP COLUMN onboarding_finished_at;

-- The checklist asks on every /me whether a person has decided any of
-- the organization's requests.
CREATE INDEX decisions_human_organization_idx ON public.decisions USING btree (organization_id) WHERE (source = 'human'::text);
