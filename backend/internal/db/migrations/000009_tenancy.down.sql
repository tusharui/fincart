-- Restore single-tenant uniqueness. This fails if two merchants own the same
-- card token or idempotency key; tenancy cannot be removed without merging or
-- deleting that data first.
ALTER TABLE idempotency_keys DROP CONSTRAINT IF EXISTS idempotency_keys_pkey;
ALTER TABLE idempotency_keys
    ADD CONSTRAINT idempotency_keys_pkey
        PRIMARY KEY (key, request_path);

DROP INDEX IF EXISTS uq_accounts_merchant_card_token;
CREATE UNIQUE INDEX IF NOT EXISTS uq_accounts_card_token ON accounts(card_token);

DROP INDEX IF EXISTS idx_payment_events_merchant_id;
DROP INDEX IF EXISTS idx_idempotency_keys_merchant_id;
DROP INDEX IF EXISTS idx_transactions_merchant_id;
DROP INDEX IF EXISTS idx_accounts_merchant_id;

ALTER TABLE payment_events DROP CONSTRAINT IF EXISTS fk_payment_events_merchant;
ALTER TABLE idempotency_keys DROP CONSTRAINT IF EXISTS fk_idempotency_keys_merchant;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS fk_transactions_merchant;
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS fk_accounts_merchant;

ALTER TABLE payment_events DROP COLUMN IF EXISTS merchant_id;
ALTER TABLE idempotency_keys DROP COLUMN IF EXISTS merchant_id;
ALTER TABLE transactions DROP COLUMN IF EXISTS merchant_id;
ALTER TABLE accounts DROP COLUMN IF EXISTS merchant_id;
