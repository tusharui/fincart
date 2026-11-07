-- Add FicMart order/customer linkage to transactions
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS order_id UUID;
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS customer_id UUID;

CREATE INDEX IF NOT EXISTS idx_transactions_order_id ON transactions(order_id);
CREATE INDEX IF NOT EXISTS idx_transactions_customer_id ON transactions(customer_id);
