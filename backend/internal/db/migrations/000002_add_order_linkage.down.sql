-- Remove FicMart order/customer linkage from transactions
ALTER TABLE transactions DROP COLUMN IF EXISTS order_id;
ALTER TABLE transactions DROP COLUMN IF EXISTS customer_id;
