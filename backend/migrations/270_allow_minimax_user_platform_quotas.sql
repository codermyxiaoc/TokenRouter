-- MiniMax 与现有供应商采用同一用户平台额度语义。
-- 约束必须与 AllowedQuotaPlatforms 同步，否则注册默认额度的整批插入会失败。
-- 只扩展允许的平台，不回填存量用户，缺失额度继续保持现有无限额语义。
ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'qoder', 'grok',
                        'kimi', 'zhipu', 'deepseek', 'minimax'));
