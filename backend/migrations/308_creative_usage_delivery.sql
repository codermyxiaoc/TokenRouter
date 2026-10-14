-- 记录使用日志已可靠投递的事实，避免日志正常清理后被后台反复补回。
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS usage_recorded_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS creative_runs_usage_recovery_idx
    ON creative_runs (id)
    WHERE usage_recorded_at IS NULL AND actual_cost IS NOT NULL
      AND status IN ('succeeded', 'result_lost', 'cancelled');
