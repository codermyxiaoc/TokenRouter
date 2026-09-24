-- 从本次切换开始按重置时间表计数，保留原日、周、月次数及全部实际额度窗口。
-- 现存记录统一以迁移事务时间建立水位，不回填无法可靠恢复的历史周期。
ALTER TABLE user_subscriptions
    ADD COLUMN IF NOT EXISTS reset_counted_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;

-- 支持后台按计数水位分批扫描；计数扫描不改变订阅实际使用量及窗口起点。
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_reset_counted_at
    ON user_subscriptions (reset_counted_at, id)
    WHERE deleted_at IS NULL AND reset_counted_at < expires_at;
