-- Step 2 schema.
--
-- A table and nothing else. No primary key, no unique constraint, no index.
-- This is the table you get when the goal is only to make the application work.

CREATE TABLE IF NOT EXISTS links (
    code       TEXT        NOT NULL,
    url        TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
