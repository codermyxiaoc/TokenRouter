-- 仅保存已确认生成成功后的安全结算证据；不保存凭据、提示词、图片或上游响应。
ALTER TABLE image_billing_reservations ADD COLUMN IF NOT EXISTS settlement_evidence JSONB;
ALTER TABLE image_billing_reservations ADD COLUMN IF NOT EXISTS settlement_retry_at TIMESTAMPTZ;
ALTER TABLE image_billing_reservations ADD COLUMN IF NOT EXISTS settlement_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE image_billing_reservations ADD COLUMN IF NOT EXISTS settlement_completed_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_image_billing_settlement_recovery
    ON image_billing_reservations(settlement_retry_at, request_id, api_key_id)
    WHERE settlement_evidence IS NOT NULL AND settlement_completed_at IS NULL;
