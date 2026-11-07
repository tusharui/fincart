-- Restore raw card columns for rollback (token data is not reversible)
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS account_number VARCHAR(16);
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS cvv VARCHAR(3);
ALTER TABLE accounts DROP COLUMN IF EXISTS card_token;
ALTER TABLE accounts DROP COLUMN IF EXISTS last4;
