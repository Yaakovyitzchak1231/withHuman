-- Skipping the guided setup and finishing it both stamp
-- onboarding_completed_at, which stops the redirect to /welcome. Only a
-- finish stamps onboarding_finished_at, so an organization that skipped
-- can be offered the setup again until someone finishes it.

ALTER TABLE public.organizations ADD COLUMN onboarding_finished_at timestamp with time zone;

-- An organization finished when one of its setup sessions completed;
-- every other organization with the completion stamp skipped.
UPDATE public.organizations o
SET onboarding_finished_at = o.onboarding_completed_at
WHERE o.onboarding_completed_at IS NOT NULL
  AND EXISTS (SELECT 1 FROM public.onboarding_sessions s
              WHERE s.organization_id = o.id AND s.status = 'completed');
