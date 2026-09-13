-- 创作台（Creative Studio）第一阶段：任务元数据表与隐藏执行 Key。
-- 隐私红线：本迁移只保存任务元数据与计费快照；
-- 任何表都不得保存图片字节、mask、prompt 明文或 provider 原始响应。

-- api_keys 增加托管来源标记：创作台自动创建的隐藏执行 Key 使用 managed_by = 'creative_studio'。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS managed_by VARCHAR(32) NULL;
-- 仅允许已知的托管来源取值，避免未来误用普通 Key 的语义。
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_managed_by_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_managed_by_check CHECK (managed_by IS NULL OR managed_by = 'creative_studio');
CREATE INDEX IF NOT EXISTS api_keys_managed_by_idx ON api_keys (managed_by);

-- 创作台任务元数据表。
CREATE TABLE IF NOT EXISTS creative_runs (
    id BIGSERIAL PRIMARY KEY,
    run_id VARCHAR(64) NOT NULL UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES groups(id),
    -- 隐藏执行 Key（managed_by = 'creative_studio'），任务创建时供应。
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
    -- 执行阶段回填实际使用的上游账号。
    account_id BIGINT REFERENCES accounts(id),
    model VARCHAR(128) NOT NULL,
    -- requested_model 记录客户端提交值，model 记录计费/路由模型。
    requested_model VARCHAR(128) NOT NULL DEFAULT '',
    operation VARCHAR(16) NOT NULL,
    requested_output_count INTEGER NOT NULL DEFAULT 1,
    image_size VARCHAR(16) NOT NULL DEFAULT '1K',
    aspect_ratio VARCHAR(16) NOT NULL DEFAULT '',
    response_mime_type VARCHAR(64) NOT NULL DEFAULT 'image/png',
    -- prompt_hash 只保存不可逆 sha256，禁止保存 prompt 明文。
    prompt_hash VARCHAR(64) NOT NULL,
    -- request_fingerprint 用于幂等，sha256(canonical JSON)，同样不可逆。
    request_fingerprint VARCHAR(64) NOT NULL UNIQUE,
    idempotency_key VARCHAR(255),
    status VARCHAR(20) NOT NULL DEFAULT 'queued',
    estimated_cost DECIMAL(20,10) NOT NULL DEFAULT 0,
    hold_amount DECIMAL(20,10),
    actual_cost DECIMAL(20,10),
    -- 计费预占快照，仿 batch_image_jobs。
    balance_hold_amount DECIMAL(20,10) NOT NULL DEFAULT 0,
    subscription_hold_allocations JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 定价快照：基础单价与订阅/余额来源倍率。
    base_unit_price DECIMAL(20,10) NOT NULL DEFAULT 0,
    subscription_rate_multiplier DECIMAL(20,10) NOT NULL DEFAULT 1,
    balance_rate_multiplier DECIMAL(20,10) NOT NULL DEFAULT 1,
    plan_group_rate_multiplier_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    error_code VARCHAR(128),
    error_message TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    -- 乐观锁版本，状态转换时 +1。
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS creative_runs_user_created_at_idx ON creative_runs (user_id, created_at DESC);
-- 只索引活跃状态，worker 扫描 queued/running 时不触碰历史终态任务。
CREATE INDEX IF NOT EXISTS creative_runs_status_idx ON creative_runs (status) WHERE status IN ('queued', 'running');
CREATE UNIQUE INDEX IF NOT EXISTS creative_runs_idempotency_key_uq ON creative_runs (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';

-- 创作台任务输出描述表：只保存输出元数据，图片本体只存在临时 Redis 存储中。
CREATE TABLE IF NOT EXISTS creative_run_outputs (
    id BIGSERIAL PRIMARY KEY,
    run_id VARCHAR(64) NOT NULL REFERENCES creative_runs(run_id) ON DELETE CASCADE,
    output_index INTEGER NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    mime_type VARCHAR(128),
    byte_size BIGINT,
    -- 临时输出过期时间；过期后不再提供下载并转为 result_lost。
    transient_expires_at TIMESTAMPTZ,
    acked_at TIMESTAMPTZ,
    error_code VARCHAR(128),
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT creative_run_outputs_run_index_uq UNIQUE (run_id, output_index)
);

CREATE INDEX IF NOT EXISTS creative_run_outputs_run_status_idx ON creative_run_outputs (run_id, status);
CREATE INDEX IF NOT EXISTS creative_run_outputs_transient_expires_at_idx ON creative_run_outputs (transient_expires_at);
