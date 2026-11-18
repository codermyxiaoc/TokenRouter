-- 让按 Key 统计 30 天 / 今日费用（API Key 列表用量列）的回落查询只读索引，
-- 不再为每次页面加载回表读取大量 usage_logs 行（同步上游 v0.2.22 迁移 249）。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_api_key_created_cost
    ON usage_logs (api_key_id, created_at)
    INCLUDE (actual_cost);
