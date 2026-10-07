-- The guided setup no longer has an assistant: the approval rules step
-- offers the person's own coding agent and a table of tools.
ALTER TABLE onboarding_sessions
    DROP COLUMN assistant_messages,
    DROP COLUMN assistant_context;
