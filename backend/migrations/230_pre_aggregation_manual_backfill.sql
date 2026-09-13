-- 手动回填使用独立目标和游标，避免覆盖自动历史回填进度。
-- 状态表固定只有一行，本迁移不会扫描或改写 usage_logs。
ALTER TABLE usage_analytics_aggregation_state
    ADD COLUMN IF NOT EXISTS manual_backfill_start TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS manual_backfill_cursor TIMESTAMPTZ;

COMMENT ON COLUMN usage_analytics_aggregation_state.manual_backfill_start
    IS '管理员最近 N 天手动回填的 UTC 目标起点';
COMMENT ON COLUMN usage_analytics_aggregation_state.manual_backfill_cursor
    IS '手动回填从新到旧推进的 UTC 小时游标';
