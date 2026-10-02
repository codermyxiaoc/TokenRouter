-- 手动重置可以保留同一个日/周窗口起点，独立代次防止旧视频退款抵扣新用量。
-- 代次仅用于内部预留快照，不替代订阅已有的自然重置次数或改变窗口时间。
ALTER TABLE user_subscriptions
    ADD COLUMN IF NOT EXISTS daily_reset_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS weekly_reset_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS monthly_reset_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE user_platform_quotas
    ADD COLUMN IF NOT EXISTS daily_reset_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS weekly_reset_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS monthly_reset_generation BIGINT NOT NULL DEFAULT 0;
