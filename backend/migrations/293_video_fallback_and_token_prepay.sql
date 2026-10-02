-- 用户视频价卡的缺省分辨率价格与固定秒价预扣合同，旧数据保持关闭。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS video_fallback_price NUMERIC(20,12),
    ADD COLUMN IF NOT EXISTS video_token_prepay JSONB;

COMMENT ON COLUMN channel_model_pricing.video_fallback_price IS '视频矩阵未配置请求分辨率时的回退价，单位由计费模式决定';
COMMENT ON COLUMN channel_model_pricing.video_token_prepay IS '视频 Token 预扣配置，price_per_second 为不参与倍率的原始美元秒价';

-- 分组价卡沿用 groups.model_pricing JSONB；不回填或改写旧价卡。
