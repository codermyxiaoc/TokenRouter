-- 请求详情快照按"数据缓存"设置区分普通请求与错误请求：
-- kind 区分快照来源（历史数据都是失败请求，默认 error）；
-- user_id / api_key_id / group_id 记录请求归属，用户在使用记录中查看缓存时据此校验本人请求，分组用于排查；
-- expires_at 为空表示沿用错误日志保留天数，非空时到期后不再返回并由清理任务删除。
ALTER TABLE ops_request_details ADD COLUMN IF NOT EXISTS kind VARCHAR(8) NOT NULL DEFAULT 'error';
ALTER TABLE ops_request_details ADD COLUMN IF NOT EXISTS user_id BIGINT;
ALTER TABLE ops_request_details ADD COLUMN IF NOT EXISTS api_key_id BIGINT;
ALTER TABLE ops_request_details ADD COLUMN IF NOT EXISTS group_id BIGINT;
ALTER TABLE ops_request_details ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
