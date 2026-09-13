-- ops_system_metrics 增加根分区磁盘空间占用快照。
ALTER TABLE ops_system_metrics
    ADD COLUMN IF NOT EXISTS disk_used_mb BIGINT,
    ADD COLUMN IF NOT EXISTS disk_total_mb BIGINT,
    ADD COLUMN IF NOT EXISTS disk_usage_percent DOUBLE PRECISION;

COMMENT ON COLUMN ops_system_metrics.disk_used_mb IS '根分区已使用磁盘空间（MB）。';
COMMENT ON COLUMN ops_system_metrics.disk_total_mb IS '根分区磁盘总空间（MB）。';
COMMENT ON COLUMN ops_system_metrics.disk_usage_percent IS '根分区磁盘使用率百分比。';
