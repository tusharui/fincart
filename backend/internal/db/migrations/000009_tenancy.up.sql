-- Attach payment data to a merchant.
--
-- Rows created before tenancy existed belong to the default merchant, so an
-- existing single-tenant deployment keeps working. Card tokens and idempotency
-- keys become unique per merchant rather than globally: two tenants may
-- legitimately hold the same card or reuse the same client-chosen key.

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS merchant_id UUID;
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS merchant_id UUID;
ALTER TABLE idempotency_keys ADD COLUMN IF NOT EXISTS merchant_id UUID;
ALTER TABLE payment_events ADD COLUMN IF NOT EXISTS merchant_id UUID;

UPDATE accounts SET merchant_id = '00000000-0000-0000-0000-000000000001'
WHERE merchant_id IS NULL;
UPDATE transactions SET merchant_id = '00000000-0000-0000-0000-000000000001'
WHERE merchant_id IS NULL;
UPDATE idempotency_keys SET merchant_id = '00000000-0000-0000-0000-000000000001'
WHERE merchant_id IS NULL;
UPDATE payment_events SET merchant_id = '00000000-0000-0000-0000-000000000001'
WHERE merchant_id IS NULL;

ALTER TABLE accounts ALTER COLUMN merchant_id SET NOT NULL;
ALTER TABLE transactions ALTER COLUMN merchant_id SET NOT NULL;
ALTER TABLE idempotency_keys ALTER COLUMN merchant_id SET NOT NULL;
ALTER TABLE payment_events ALTER COLUMN merchant_id SET NOT NULL;

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS fk_accounts_merchant,
    ADD CONSTRAINT fk_accounts_merchant
        FOREIGN KEY (merchant_id) REFERENCES merchants(id);
ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS fk_transactions_merchant,
    ADD CONSTRAINT fk_transactions_merchant
        FOREIGN KEY (merchant_id) REFERENCES merchants(id);
ALTER TABLE idempotency_keys
    DROP CONSTRAINT IF EXISTS fk_idempotency_keys_merchant,
    ADD CONSTRAINT fk_idempotency_keys_merchant
        FOREIGN KEY (merchant_id) REFERENCES merchants(id);
ALTER TABLE payment_events
    DROP CONSTRAINT IF EXISTS fk_payment_events_merchant,
    ADD CONSTRAINT fk_payment_events_merchant
        FOREIGN KEY (merchant_id) REFERENCES merchants(id);

CREATE INDEX IF NOT EXISTS idx_accounts_merchant_id ON accounts(merchant_id);
CREATE INDEX IF NOT EXISTS idx_transactions_merchant_id ON transactions(merchant_id);
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_merchant_id ON idempotency_keys(merchant_id);
CREATE INDEX IF NOT EXISTS idx_payment_events_merchant_id ON payment_events(merchant_id);

-- A card belongs to a merchant, not to the whole bank.
DROP INDEX IF EXISTS uq_accounts_card_token;
CREATE UNIQUE INDEX IF NOT EXISTS uq_accounts_merchant_card_token
    ON accounts(merchant_id, card_token);

-- Idempotency keys are client-chosen, so they must be scoped per merchant.
ALTER TABLE idempotency_keys DROP CONSTRAINT IF EXISTS idempotency_keys_pkey;
ALTER TABLE idempotency_keys
    ADD CONSTRAINT idempotency_keys_pkey
        PRIMARY KEY (merchant_id, key, request_path);
