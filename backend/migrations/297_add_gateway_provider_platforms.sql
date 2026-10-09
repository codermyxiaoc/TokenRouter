-- 新平台加入现有配额约束；完整保留 Qoder、Video 等当前平台，不移除数据库保护。
ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;
ALTER TABLE user_platform_quotas ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'qoder', 'grok',
        'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'video',
        'typesafe', 'cline', 'command_code'));
