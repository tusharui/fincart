DROP INDEX IF EXISTS idx_idempotency_keys_created_at;
ALTER TABLE idempotency_keys DROP COLUMN IF EXISTS request_hash;
