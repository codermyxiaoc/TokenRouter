-- 模型分布改用去除复合 Key 前缀后的内部请求模型。
-- 旧聚合桶没有足够信息可靠还原该维度，因此清空可重建数据并让查询暂时回退原始记录。
TRUNCATE TABLE usage_analytics_hourly, usage_analytics_daily;

-- 重置覆盖状态后，实时任务会先重建当前窗口，再按既有预算从新到旧回填历史。
UPDATE usage_analytics_aggregation_state
SET live_watermark = TIMESTAMPTZ '1970-01-01 00:00:00+00',
    coverage_start = NULL,
    backfill_cursor = NULL,
    source_oldest_at = NULL,
    manual_backfill_start = NULL,
    manual_backfill_cursor = NULL,
    phase = 'idle',
    last_run_at = NULL,
    last_success_at = NULL,
    last_error_at = NULL,
    last_error = '',
    last_duration_ms = 0,
    updated_at = NOW()
WHERE id = 1;
