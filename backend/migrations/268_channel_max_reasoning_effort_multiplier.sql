-- 为渠道 token 定价增加 max 推理档位倍率，NULL 表示沿用模型默认值。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS max_reasoning_effort_multiplier NUMERIC(20,8);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'channel_model_pricing_max_reasoning_effort_multiplier_positive'
          AND conrelid = 'channel_model_pricing'::regclass
    ) THEN
        ALTER TABLE channel_model_pricing
            ADD CONSTRAINT channel_model_pricing_max_reasoning_effort_multiplier_positive
            CHECK (max_reasoning_effort_multiplier IS NULL OR max_reasoning_effort_multiplier > 0);
    END IF;
END $$;

COMMENT ON COLUMN channel_model_pricing.max_reasoning_effort_multiplier IS
    '最终转发推理档位为 max 时的 token 计费倍率；NULL 表示沿用模型默认值';
