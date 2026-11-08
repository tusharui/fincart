-- Tokenize card storage: replace raw PAN/CVV with provider token + last4.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS card_token VARCHAR(64);
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS last4 VARCHAR(4);

-- Backfill tokens for rows created before tokenization (for example the demo
-- accounts seeded by 000001) so they remain usable after the raw PAN is
-- dropped. The token format must match security.HashToken in the application.
UPDATE accounts
SET card_token = encode(digest(account_number, 'sha256'), 'hex'),
    last4 = right(account_number, 4)
WHERE card_token IS NULL
  AND account_number IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_accounts_card_token ON accounts(card_token);
ALTER TABLE accounts DROP COLUMN IF EXISTS account_number;
ALTER TABLE accounts DROP COLUMN IF EXISTS cvv;
