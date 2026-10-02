-- 视频 Token 单价按分辨率和参考视频条件独立存储，旧秒价及聊天 Token 价不做改写。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS video_prices JSONB NOT NULL DEFAULT '[]'::jsonb;

COMMENT ON COLUMN channel_model_pricing.video_prices IS
    '视频分辨率与has_reference_video价格矩阵；video为USD/s，video_token为USD/百万Token';

-- 分组逐模型价卡沿用 groups.model_pricing JSONB，无需重写存量配置。

-- 独立 Video 额度与其它平台隔离，不为已有用户生成无限额记录。
ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'qoder', 'grok',
                        'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'video'));
