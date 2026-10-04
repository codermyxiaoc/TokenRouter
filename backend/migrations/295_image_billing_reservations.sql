-- 普通异步图片的资金预占独立留存，不能随短期图片结果清理而丢失待核对账本。
-- 实际冻结余额继续使用 users.frozen_balance，订阅额度沿用现有分摊与窗口规则。
CREATE TABLE IF NOT EXISTS image_billing_reservations (
    request_id VARCHAR(255) NOT NULL,
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fingerprint VARCHAR(64) NOT NULL,
    state VARCHAR(24) NOT NULL CHECK (state IN ('reserved', 'captured', 'released', 'reconciliation')),
    snapshot JSONB NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    quote JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(quote) = 'object'),
    capture_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    result JSONB,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (request_id, api_key_id)
);
CREATE INDEX IF NOT EXISTS idx_image_billing_reservations_pending
    ON image_billing_reservations(expires_at, request_id, api_key_id) WHERE state = 'reserved';
CREATE INDEX IF NOT EXISTS idx_image_billing_reservations_user
    ON image_billing_reservations(user_id, created_at);
