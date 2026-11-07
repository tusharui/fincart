-- Support the authorization expiry sweep: find active holds that have passed
-- their expiry time.
CREATE INDEX IF NOT EXISTS idx_transactions_expiry_sweep
    ON transactions(expires_at)
    WHERE type = 'AUTH_HOLD' AND status = 'ACTIVE';

-- Store expires_at with timezone information. TIMESTAMP columns drop the
-- offset, so a value written by a Go process outside UTC round-trips as a
-- different instant and expiry decisions become incorrect.
ALTER TABLE transactions
    ALTER COLUMN expires_at TYPE TIMESTAMPTZ
    USING expires_at AT TIME ZONE current_setting('TimeZone');
