-- 邮箱别名查重使用去点后的表达式探针；建立部分索引以避免公开发码入口
-- 随用户表增长退化为不可控的全表扫描。该索引不施加唯一约束，
-- 因为历史数据可能已经存在别名重复，且别名策略由服务逻辑负责。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email_dot_stripped
    ON users ((REPLACE(LOWER(TRIM(email)), '.', '')) text_pattern_ops)
    WHERE deleted_at IS NULL;
