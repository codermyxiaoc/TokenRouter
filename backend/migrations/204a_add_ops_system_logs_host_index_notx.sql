-- 为主机与时间范围筛选建立并发索引，避免阻塞在线日志写入。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ops_system_logs_host_created_at
  ON ops_system_logs (host, created_at DESC);
