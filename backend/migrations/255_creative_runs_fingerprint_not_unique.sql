-- 创作台幂等修复：request_fingerprint 不再全局唯一。
-- 幂等语义只要求 Idempotency-Key 级别（部分唯一索引已覆盖）：
-- 相同 Key + 相同请求体返回原任务，相同 Key + 不同请求体返回冲突。
-- 全局唯一会把"相同 prompt/素材的再次提交"永久判为冲突，阻断正常重试。

ALTER TABLE creative_runs DROP CONSTRAINT IF EXISTS creative_runs_request_fingerprint_key;
