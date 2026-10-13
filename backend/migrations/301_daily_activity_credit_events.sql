-- 可选充值来源只从各自开关开启后的新入账记录开始，不回填历史兑换和管理员加款。
CREATE TABLE daily_activity_credit_events (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_usage_id BIGINT NOT NULL UNIQUE,
    source_type VARCHAR(24) NOT NULL CHECK (source_type IN ('redeem_balance','admin_balance')),
    amount NUMERIC(24,8) NOT NULL CHECK (amount > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- 不依赖兑换凭据或使用日志外键，事件保存实际到账金额与来源快照。
CREATE INDEX idx_daily_activity_credit_user_time ON daily_activity_credit_events(user_id,created_at);
CREATE INDEX IF NOT EXISTS idx_payment_orders_recharge_code_nonempty
    ON payment_orders (recharge_code) WHERE recharge_code <> '';
