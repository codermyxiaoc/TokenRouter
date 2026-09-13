-- API Key 可选择保留历史自动结算、锁定指定订阅或仅使用余额。
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS billing_mode VARCHAR(32) NOT NULL DEFAULT 'auto';

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS preferred_subscription_id BIGINT NULL;

COMMENT ON COLUMN api_keys.billing_mode IS 'API Key 结算模式：auto、subscription 或 balance';
COMMENT ON COLUMN api_keys.preferred_subscription_id IS 'subscription 模式锁定使用的用户订阅 ID';

-- 批量图片任务冻结提交时的资金来源，避免异步结算读取到后续编辑后的 API Key 配置。
ALTER TABLE batch_image_jobs
    ADD COLUMN IF NOT EXISTS billing_mode VARCHAR(32) NOT NULL DEFAULT 'auto',
    ADD COLUMN IF NOT EXISTS preferred_subscription_id BIGINT NULL;

COMMENT ON COLUMN batch_image_jobs.billing_mode IS '批量图片提交时冻结的 API Key 结算模式';
COMMENT ON COLUMN batch_image_jobs.preferred_subscription_id IS '批量图片指定订阅结算时冻结的用户订阅 ID';
