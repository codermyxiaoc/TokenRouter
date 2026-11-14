-- 过期清理按 expires_at 扫描，手动清除按 kind 分批删除；CONCURRENTLY 避免锁住高写入的快照表。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ops_request_details_expires_at
    ON ops_request_details (expires_at)
    WHERE expires_at IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ops_request_details_kind_created_at
    ON ops_request_details (kind, created_at);
