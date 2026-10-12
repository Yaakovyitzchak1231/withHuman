-- The outcome note limit is 1000 characters, as the API validates it.
-- char_length counts bytes in a SQL_ASCII database, which is what the
-- embedded PostgreSQL initializes under a C locale, so a note of 1000
-- non-ASCII characters failed the check. Count the note's characters as
-- UTF-8 instead: convert_to is the identity in a UTF8 or SQL_ASCII
-- database and transcodes from any other server encoding.
ALTER TABLE public.approval_consumptions DROP CONSTRAINT approval_consumptions_outcome_note_check;
ALTER TABLE public.approval_consumptions ADD CONSTRAINT approval_consumptions_outcome_note_check
    CHECK (((outcome_note IS NULL) OR (length(convert_to(outcome_note, 'UTF8'), 'UTF8') BETWEEN 1 AND 1000)));
