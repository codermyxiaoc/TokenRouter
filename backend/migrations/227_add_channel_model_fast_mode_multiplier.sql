-- 为 OpenAI 渠道模型定价增加独立的 Fast 模式收费倍率。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS fast_mode_multiplier NUMERIC(20,8);

COMMENT ON COLUMN channel_model_pricing.fast_mode_multiplier IS 'OpenAI Fast 模式收费倍率，NULL 表示沿用模型默认 Fast 定价';
