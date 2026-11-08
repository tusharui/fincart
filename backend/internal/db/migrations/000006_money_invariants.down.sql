ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_status_valid,
    DROP CONSTRAINT IF EXISTS transactions_type_valid,
    DROP CONSTRAINT IF EXISTS transactions_amount_cents_positive;

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_available_balance_within_balance,
    DROP CONSTRAINT IF EXISTS accounts_available_balance_cents_nonnegative,
    DROP CONSTRAINT IF EXISTS accounts_balance_cents_nonnegative;
