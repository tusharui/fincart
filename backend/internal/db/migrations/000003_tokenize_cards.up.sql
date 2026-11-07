-- Tokenize card storage: replace raw PAN/CVV with provider token + last4
ALTER TABLE accounts ADD COLUMN card_token VARCHAR(64);
ALTER TABLE accounts ADD COLUMN last4 VARCHAR(4);
CREATE UNIQUE INDEX IF NOT EXISTS uq_accounts_card_token ON accounts(card_token);
ALTER TABLE accounts DROP COLUMN IF EXISTS account_number;
ALTER TABLE accounts DROP COLUMN IF EXISTS cvv;
