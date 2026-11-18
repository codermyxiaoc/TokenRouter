-- OpenAI 缓存写入的 30m/12h 有效期与 Anthropic 5m/1h 分开记录与计价（同步上游 v0.2.22 迁移 250）。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS cache_creation_30m_tokens INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_creation_12h_tokens INTEGER NOT NULL DEFAULT 0;

ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS cache_write_12h_price NUMERIC(20,12);
COMMENT ON COLUMN channel_model_pricing.cache_write_12h_price IS
    'OpenAI 12 小时缓存写入单价；为空时沿用通用 cache_write_price';

ALTER TABLE channel_pricing_intervals
    ADD COLUMN IF NOT EXISTS cache_write_12h_price NUMERIC(20,12);
COMMENT ON COLUMN channel_pricing_intervals.cache_write_12h_price IS
    'OpenAI 12 小时缓存写入单价；为空时沿用通用 cache_write_price';

ALTER TABLE channel_account_stats_model_pricing
    ADD COLUMN IF NOT EXISTS cache_write_12h_price NUMERIC(20,12);
COMMENT ON COLUMN channel_account_stats_model_pricing.cache_write_12h_price IS
    'OpenAI 12 小时缓存写入单价；为空时沿用通用 cache_write_price';

ALTER TABLE channel_account_stats_pricing_intervals
    ADD COLUMN IF NOT EXISTS cache_write_12h_price NUMERIC(20,12);
COMMENT ON COLUMN channel_account_stats_pricing_intervals.cache_write_12h_price IS
    'OpenAI 12 小时缓存写入单价；为空时沿用通用 cache_write_price';
