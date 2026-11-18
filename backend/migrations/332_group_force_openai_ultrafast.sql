-- 分组可对支持 Ultrafast 的模型（GPT-6 Astra、GPT-6.1 Sol）强制 service_tier=ultrafast（同步上游 v0.2.22 迁移 251）。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS force_openai_ultrafast BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.force_openai_ultrafast IS
    '对支持 Ultrafast 的模型强制本 OpenAI 分组请求使用 service_tier=ultrafast，在全局 Fast/Flex 策略之前生效，优先于 force_openai_fast';
