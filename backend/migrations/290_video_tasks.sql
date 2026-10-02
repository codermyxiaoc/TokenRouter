-- 独立视频任务以 PostgreSQL 为权威状态，队列和展示缓存不承担账务归属。
CREATE TABLE IF NOT EXISTS video_tasks (
    id VARCHAR(64) PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
    account_id BIGINT NOT NULL REFERENCES accounts(id),
    group_id BIGINT NOT NULL REFERENCES groups(id),
    protocol VARCHAR(32) NOT NULL,
    upstream_task_id VARCHAR(255) NOT NULL DEFAULT '',
    idempotency_key VARCHAR(128),
    payload_hash VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'prepared',
    billing_status VARCHAR(32) NOT NULL DEFAULT 'pending',
    record JSONB NOT NULL,
    allowance_reserved BOOLEAN NOT NULL DEFAULT FALSE,
    balance_hold_amount NUMERIC(20,8) NOT NULL DEFAULT 0,
    subscription_hold_allocations JSONB NOT NULL DEFAULT '[]',
    hold_amount NUMERIC(20,10) NOT NULL DEFAULT 0,
    estimated_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    billing_result JSONB,
    platform_quota_hold JSONB,
    subscription_window_holds JSONB,
    effects_done BOOLEAN NOT NULL DEFAULT FALSE,
    lease_token VARCHAR(64) NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    next_poll_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(api_key_id,idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_video_tasks_ready ON video_tasks(next_poll_at,lease_until)
    WHERE effects_done=FALSE;
CREATE INDEX IF NOT EXISTS idx_video_tasks_native ON video_tasks(api_key_id,protocol,upstream_task_id);
CREATE INDEX IF NOT EXISTS idx_video_tasks_account_active ON video_tasks(account_id,status);
ALTER TABLE media_tasks DROP CONSTRAINT IF EXISTS media_tasks_source_check;
ALTER TABLE media_tasks ADD CONSTRAINT media_tasks_source_check
    CHECK (source IN ('async_image','grok_video','seedance_video','video'));
