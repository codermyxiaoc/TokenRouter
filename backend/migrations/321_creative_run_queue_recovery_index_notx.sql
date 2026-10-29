-- 数据库兜底扫描只读取非终态任务；部分索引避免扫描大量已结束历史记录，CONCURRENTLY 不阻塞写入。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_creative_runs_queue_recovery
    ON creative_runs (updated_at)
    WHERE status IN ('queued', 'running', 'provider_succeeded', 'settlement_pending', 'release_pending');
