-- 支持 KWD 等三位小数币种，避免实付、手续费和退款快照落库后被舍入。
-- 精度同步增加一位，保留原 decimal(20,2) 的十八位整数容量；不重算历史金额或权益。
ALTER TABLE payment_orders
    ALTER COLUMN amount TYPE DECIMAL(21,3),
    ALTER COLUMN pay_amount TYPE DECIMAL(21,3),
    ALTER COLUMN fee_fixed TYPE DECIMAL(21,3),
    ALTER COLUMN fee_rate_amount TYPE DECIMAL(21,3),
    ALTER COLUMN fee_amount TYPE DECIMAL(21,3),
    ALTER COLUMN bonus_amount TYPE DECIMAL(21,3),
    ALTER COLUMN refund_amount TYPE DECIMAL(21,3);
