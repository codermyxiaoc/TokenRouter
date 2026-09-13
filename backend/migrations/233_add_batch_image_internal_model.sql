-- 异步结算需要保留渠道和账号映射前的内部模型，不能从最终上游模型反推。
ALTER TABLE batch_image_jobs
    ADD COLUMN IF NOT EXISTS internal_model VARCHAR(128) NOT NULL DEFAULT '';

COMMENT ON COLUMN batch_image_jobs.internal_model IS '复合 Key 选组和 API Key 模型重定向完成后、渠道与账号映射前的内部模型；空值表示迁移前任务';
