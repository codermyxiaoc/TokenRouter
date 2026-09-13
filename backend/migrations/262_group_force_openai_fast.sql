-- 为分组增加 OpenAI Fast 强制策略开关。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS force_openai_fast BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.force_openai_fast IS
    '强制 OpenAI/Composite 分组请求使用 service_tier=priority，并继续接受全局 Fast/Flex 策略裁决';
