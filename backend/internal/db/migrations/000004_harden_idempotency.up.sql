-- Harden idempotency handling: bind cached responses to the request body and
-- reserve keys while a request is in flight.
ALTER TABLE idempotency_keys ADD COLUMN IF NOT EXISTS request_hash VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_created_at ON idempotency_keys(created_at);
