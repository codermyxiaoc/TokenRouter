-- OpenCode 与当前十个平台共用用户日、周、月额度语义。
-- 仅扩展允许值，保留 Qoder 等本地平台，不触及上游独有的监控或复合路由表。
-- 不回填存量用户，缺失额度记录继续表示无限额。
ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'qoder', 'grok',
                        'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));
