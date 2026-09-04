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

-- Step 7.
--
-- One row per redirect. Append only, and nothing reads it yet, so it has no
-- index. Rows arrive at whatever rate the redirect endpoint is serving.

CREATE TABLE IF NOT EXISTS clicks (
    code       TEXT        NOT NULL,
    clicked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
