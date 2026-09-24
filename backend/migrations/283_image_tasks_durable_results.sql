-- 异步图片只保存紧凑结果和执行租约，不保存生成请求、密钥或图片 Base64。
-- 与媒体任务投影同事务写入；租约过期只登记中断，不能重放可能已经计费的生成请求。
CREATE TABLE IF NOT EXISTS image_tasks (
    id VARCHAR(255) PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL CHECK (status IN ('processing', 'completed', 'failed')),
    execution_id VARCHAR(64) NOT NULL DEFAULT '',
    lease_expires_at BIGINT NOT NULL DEFAULT 0,
    execution_deadline BIGINT NOT NULL DEFAULT 0,
    expires_at BIGINT NOT NULL,
    record JSONB NOT NULL CHECK (jsonb_typeof(record) = 'object'),
    maintained_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_image_tasks_processing_maintenance
    ON image_tasks(maintained_at, id) WHERE status = 'processing'
    OR (status = 'failed' AND record->'error'->>'type' = 'execution_interrupted');
CREATE INDEX IF NOT EXISTS idx_image_tasks_expires ON image_tasks(expires_at);
