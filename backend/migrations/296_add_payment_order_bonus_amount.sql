-- 充值优惠快照，历史订单为零，不回填或改变既有权益。
ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS bonus_amount DECIMAL(20, 2) NOT NULL DEFAULT 0;
