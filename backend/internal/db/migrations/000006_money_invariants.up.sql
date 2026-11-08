-- Money invariants at the database boundary.
--
-- The application already guards every balance update, but these constraints
-- are the last line of defense against a bug or an out-of-band write leaving an
-- account in a state where settled funds or available funds are negative, or
-- where more money is available than the account actually holds.

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_balance_cents_nonnegative,
    ADD CONSTRAINT accounts_balance_cents_nonnegative
        CHECK (balance_cents >= 0),
    DROP CONSTRAINT IF EXISTS accounts_available_balance_cents_nonnegative,
    ADD CONSTRAINT accounts_available_balance_cents_nonnegative
        CHECK (available_balance_cents >= 0),
    DROP CONSTRAINT IF EXISTS accounts_available_balance_within_balance,
    ADD CONSTRAINT accounts_available_balance_within_balance
        CHECK (available_balance_cents <= balance_cents);

-- Ledger entries are always positive amounts; direction is encoded by type.
ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_amount_cents_positive,
    ADD CONSTRAINT transactions_amount_cents_positive
        CHECK (amount_cents > 0),
    DROP CONSTRAINT IF EXISTS transactions_type_valid,
    ADD CONSTRAINT transactions_type_valid
        CHECK (type IN ('AUTH_HOLD', 'CAPTURE', 'VOID', 'REFUND')),
    DROP CONSTRAINT IF EXISTS transactions_status_valid,
    ADD CONSTRAINT transactions_status_valid
        CHECK (status IN ('ACTIVE', 'COMPLETED', 'EXPIRED', 'VOIDED', 'REFUNDED'));
