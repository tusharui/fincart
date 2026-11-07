-- Support the authorization expiry sweep: find active holds that have passed
-- their expiry time.
CREATE INDEX IF NOT EXISTS idx_transactions_expiry_sweep
    ON transactions(expires_at)
    WHERE type = 'AUTH_HOLD' AND status = 'ACTIVE';
