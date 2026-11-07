ALTER TABLE transactions
    ALTER COLUMN expires_at TYPE TIMESTAMP
    USING expires_at AT TIME ZONE current_setting('TimeZone');

DROP INDEX IF EXISTS idx_transactions_expiry_sweep;
