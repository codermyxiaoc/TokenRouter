-- 渠道模型定价支持可选倍率；NULL 表示保持现有定价逻辑不变。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS price_multiplier NUMERIC(20,8);

-- 自定义账号统计规则复用渠道模型定价结构，因此同步支持相同倍率。
ALTER TABLE channel_account_stats_model_pricing
    ADD COLUMN IF NOT EXISTS price_multiplier NUMERIC(20,8);

COMMENT ON COLUMN channel_model_pricing.price_multiplier IS '最终模型定价倍率，NULL 表示不调整价格';
COMMENT ON COLUMN channel_account_stats_model_pricing.price_multiplier IS '最终账号统计定价倍率，NULL 表示不调整价格';
