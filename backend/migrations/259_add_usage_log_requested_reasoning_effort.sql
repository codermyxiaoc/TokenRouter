-- 保存客户端在分组策略和模型映射前请求的推理档位，兼容历史 usage_logs 行。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS requested_reasoning_effort VARCHAR(20);
