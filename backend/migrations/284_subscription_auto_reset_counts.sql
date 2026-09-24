-- 分别累计每份订阅日、周、月窗口的自动重置次数。
-- 历史记录缺少不可变的重置审计，统一从零开始，不推算迁移前次数。
ALTER TABLE user_subscriptions
    ADD COLUMN IF NOT EXISTS daily_reset_count BIGINT NOT NULL DEFAULT 0 CHECK (daily_reset_count >= 0),
    ADD COLUMN IF NOT EXISTS weekly_reset_count BIGINT NOT NULL DEFAULT 0 CHECK (weekly_reset_count >= 0),
    ADD COLUMN IF NOT EXISTS monthly_reset_count BIGINT NOT NULL DEFAULT 0 CHECK (monthly_reset_count >= 0);
