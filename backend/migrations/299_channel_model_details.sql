-- 视频模型公开说明单独存储，不改写历史定价、模型映射或任务快照。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS model_details JSONB NOT NULL DEFAULT '{}'::jsonb;
