-- 渠道模型定价支持 Fast/Flex 与上下文区间倍率；NULL 表示沿用默认价格。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS fast_multiplier NUMERIC(20,8),
    ADD COLUMN IF NOT EXISTS flex_multiplier NUMERIC(20,8);

ALTER TABLE channel_pricing_intervals
    ADD COLUMN IF NOT EXISTS input_multiplier NUMERIC(20,8),
    ADD COLUMN IF NOT EXISTS output_multiplier NUMERIC(20,8),
    ADD COLUMN IF NOT EXISTS cache_write_multiplier NUMERIC(20,8),
    ADD COLUMN IF NOT EXISTS cache_read_multiplier NUMERIC(20,8);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'channel_model_pricing_fast_multiplier_positive'
          AND conrelid = 'channel_model_pricing'::regclass
    ) THEN
        ALTER TABLE channel_model_pricing
            ADD CONSTRAINT channel_model_pricing_fast_multiplier_positive
            CHECK (fast_multiplier IS NULL OR fast_multiplier > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'channel_model_pricing_flex_multiplier_positive'
          AND conrelid = 'channel_model_pricing'::regclass
    ) THEN
        ALTER TABLE channel_model_pricing
            ADD CONSTRAINT channel_model_pricing_flex_multiplier_positive
            CHECK (flex_multiplier IS NULL OR flex_multiplier > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'channel_pricing_intervals_input_multiplier_positive'
          AND conrelid = 'channel_pricing_intervals'::regclass
    ) THEN
        ALTER TABLE channel_pricing_intervals
            ADD CONSTRAINT channel_pricing_intervals_input_multiplier_positive
            CHECK (input_multiplier IS NULL OR input_multiplier > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'channel_pricing_intervals_output_multiplier_positive'
          AND conrelid = 'channel_pricing_intervals'::regclass
    ) THEN
        ALTER TABLE channel_pricing_intervals
            ADD CONSTRAINT channel_pricing_intervals_output_multiplier_positive
            CHECK (output_multiplier IS NULL OR output_multiplier > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'channel_pricing_intervals_cache_write_multiplier_positive'
          AND conrelid = 'channel_pricing_intervals'::regclass
    ) THEN
        ALTER TABLE channel_pricing_intervals
            ADD CONSTRAINT channel_pricing_intervals_cache_write_multiplier_positive
            CHECK (cache_write_multiplier IS NULL OR cache_write_multiplier > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'channel_pricing_intervals_cache_read_multiplier_positive'
          AND conrelid = 'channel_pricing_intervals'::regclass
    ) THEN
        ALTER TABLE channel_pricing_intervals
            ADD CONSTRAINT channel_pricing_intervals_cache_read_multiplier_positive
            CHECK (cache_read_multiplier IS NULL OR cache_read_multiplier > 0);
    END IF;
END $$;

COMMENT ON COLUMN channel_model_pricing.fast_multiplier IS 'Fast/priority 服务层级相对普通价格的倍率';
COMMENT ON COLUMN channel_model_pricing.flex_multiplier IS 'Flex 服务层级相对普通价格的倍率';
