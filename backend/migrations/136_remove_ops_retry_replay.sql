-- 移除不再使用的 Ops 重试/重放存储。
-- 重试接口已经下线，继续保留请求体和重试审计行只会放大写入宽度、内存保留和数据库体积。

DROP TABLE IF EXISTS ops_retry_attempts CASCADE;

ALTER TABLE ops_error_logs
  DROP COLUMN IF EXISTS request_body,
  DROP COLUMN IF EXISTS request_headers,
  DROP COLUMN IF EXISTS request_body_truncated,
  DROP COLUMN IF EXISTS request_body_bytes,
  DROP COLUMN IF EXISTS is_retryable,
  DROP COLUMN IF EXISTS retry_count,
  DROP COLUMN IF EXISTS resolved_retry_id;

COMMENT ON TABLE ops_error_logs IS 'Ops 错误日志（vNext）。仅保存脱敏错误详情，已移除请求重放存储。';
