-- Fills the table with 500,000 links so that lookups have to do real work.
-- Codes are seeded deterministically so the load test can ask for a known one.

INSERT INTO links (code, url)
SELECT
    'seed' || lpad(i::text, 6, '0'),
    'https://example.com/page/' || i
FROM generate_series(1, 500000) AS i;
