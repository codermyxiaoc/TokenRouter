-- 为渠道及账号统计定价增加独立的 1 小时缓存写入单价。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS cache_write_1h_price NUMERIC(20,12);

ALTER TABLE channel_pricing_intervals
    ADD COLUMN IF NOT EXISTS cache_write_1h_price NUMERIC(20,12);

ALTER TABLE channel_account_stats_model_pricing
    ADD COLUMN IF NOT EXISTS cache_write_1h_price NUMERIC(20,12);

ALTER TABLE channel_account_stats_pricing_intervals
    ADD COLUMN IF NOT EXISTS cache_write_1h_price NUMERIC(20,12);

COMMENT ON COLUMN channel_model_pricing.cache_write_1h_price IS
    '1 小时缓存写入单价（每 token）；NULL 时沿用旧 cache_write_price 语义';
COMMENT ON COLUMN channel_pricing_intervals.cache_write_1h_price IS
    '区间专用的 1 小时缓存写入单价（每 token）';
COMMENT ON COLUMN channel_account_stats_model_pricing.cache_write_1h_price IS
    '账号统计定价的 1 小时缓存写入单价（每 token）';
COMMENT ON COLUMN channel_account_stats_pricing_intervals.cache_write_1h_price IS
    '账号统计区间专用的 1 小时缓存写入单价（每 token）';
