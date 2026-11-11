-- Multi-tenant authentication.
--
-- Merchants own API keys. A later migration attaches merchant_id to accounts,
-- transactions, idempotency records, and events. The default merchant owns any
-- data created before tenancy existed, so existing single-tenant deployments
-- keep working after the upgrade.

CREATE TABLE merchants (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO merchants (id, name)
VALUES ('00000000-0000-0000-0000-000000000001', 'default')
ON CONFLICT (id) DO NOTHING;

CREATE TABLE api_keys (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    key_prefix VARCHAR(16) NOT NULL,
    key_last4 VARCHAR(8) NOT NULL,
    -- SHA-256 hex digest of the raw key. The raw key is never stored.
    key_hash CHAR(64) NOT NULL,
    scopes TEXT[] NOT NULL DEFAULT ARRAY['payments']::TEXT[],
    -- Zero means "use the global default" from configuration.
    rate_limit_per_minute INT NOT NULL DEFAULT 0,
    -- Marks the key reconciled from AUTH_BOOTSTRAP_KEY at startup.
    bootstrap BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT api_keys_hash_unique UNIQUE (key_hash),
    CONSTRAINT api_keys_rate_limit_nonnegative CHECK (rate_limit_per_minute >= 0)
);

CREATE INDEX idx_api_keys_merchant_id ON api_keys(merchant_id);

-- At most one bootstrap key per merchant, so rotating AUTH_BOOTSTRAP_KEY
-- replaces the previous bootstrap credential instead of accumulating keys.
CREATE UNIQUE INDEX uq_api_keys_bootstrap
    ON api_keys (merchant_id)
    WHERE bootstrap;
