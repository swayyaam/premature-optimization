-- Step 3 schema.
--
-- The unique index on code does two jobs. It turns a lookup by code into an
-- index scan instead of a read of the whole table, and it makes the database
-- the thing that decides whether a code is already taken.

CREATE TABLE IF NOT EXISTS links (
    code       TEXT        NOT NULL,
    url        TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS links_code_key ON links (code);
