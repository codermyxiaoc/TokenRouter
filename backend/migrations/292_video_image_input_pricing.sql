-- 视频参考图片按张附加费独立存储；旧价卡保持 NULL，不改变已有视频单价。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS video_image_input_pricing JSONB;

COMMENT ON COLUMN channel_model_pricing.video_image_input_pricing IS
    '视频参考图片附加价：free_images为免费张数，price为USD/张；NULL禁用，显式零价有效';

-- 分组逐模型价卡沿用 groups.model_pricing JSONB，不回填或改写存量配置。
