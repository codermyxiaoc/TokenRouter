-- 持久化客户端显式提供的请求会话或对话标识，例如 session_id、
-- X-Session-Id 和 X-Conversation-ID 请求头，便于按会话关联用量记录。
--
-- 字段可空且没有默认值：PostgreSQL 11+ 只需修改元数据，不会重写可能很大的
-- usage_logs 表。缺失或无效的会话标识保持 NULL；prompt_cache_key 和从内容
-- 派生的粘性哈希不得写入该字段。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS session_id VARCHAR(255);

-- 批量图片用量会在原始 HTTP 请求结束后异步记录，因此在任务上保留同一份
-- 已清理的会话标识。
ALTER TABLE batch_image_jobs ADD COLUMN IF NOT EXISTS session_id VARCHAR(255);
