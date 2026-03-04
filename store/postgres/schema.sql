-- Idempotency keys table.
-- Apply once before starting the application:
--   psql $DATABASE_URL -f schema.sql

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key          TEXT        PRIMARY KEY,
    status       SMALLINT    NOT NULL DEFAULT 0,
    status_code  INTEGER,
    headers      JSONB,
    body         BYTEA,
    request_hash TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ
);

-- Speed up expiry-aware lookups and background cleanup.
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expires_at
    ON idempotency_keys (expires_at)
    WHERE expires_at IS NOT NULL;
