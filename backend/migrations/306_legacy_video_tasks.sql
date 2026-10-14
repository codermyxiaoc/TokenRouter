-- 旧 Grok/Seedance 视频补齐持久恢复依据；不回填猜测价格、不修改余额或既有扣费账本。
CREATE TABLE IF NOT EXISTS legacy_video_tasks (
    id BIGSERIAL PRIMARY KEY,
    source VARCHAR(32) NOT NULL CHECK (source IN ('grok_video', 'seedance_video')),
    task_id VARCHAR(255) NOT NULL CHECK (task_id <> ''),
    user_id BIGINT NOT NULL REFERENCES users(id),
    billing_user_id BIGINT NOT NULL DEFAULT 0 CHECK (billing_user_id >= 0),
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
    group_id BIGINT NOT NULL REFERENCES groups(id),
    account_id BIGINT NOT NULL REFERENCES accounts(id),
    model VARCHAR(255) NOT NULL DEFAULT '',
    request_id VARCHAR(255) NOT NULL DEFAULT '',
    pending JSONB,
    status VARCHAR(16) NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'completed', 'failed', 'cancelled', 'expired')),
    billing_status VARCHAR(32) NOT NULL DEFAULT 'pending'
        CHECK (billing_status IN ('pending', 'settled', 'not_billable', 'reconciliation')),
    settlement_result JSONB,
    -- 实际资金分配回执由既有扣费事务写入，恢复器不能用报价替代或覆盖。
    billing_result JSONB,
    -- 上游删除成功不代表生成未计费，单独保留删除事实以停止无效查询。
    upstream_deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    next_poll_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_token VARCHAR(64) NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error VARCHAR(500) NOT NULL DEFAULT '',
    UNIQUE (source, task_id, api_key_id),
    CHECK (pending IS NULL OR jsonb_typeof(pending) = 'object'),
    CHECK (settlement_result IS NULL OR jsonb_typeof(settlement_result) = 'object'),
    CHECK (billing_result IS NULL OR jsonb_typeof(billing_result) = 'object')
);

-- 恢复器只领取待处理账务，生成成功但未结算的任务仍须继续恢复。
CREATE INDEX IF NOT EXISTS idx_legacy_video_tasks_due ON legacy_video_tasks(next_poll_at, id)
    WHERE billing_status IN ('pending', 'reconciliation');
CREATE INDEX IF NOT EXISTS idx_legacy_video_tasks_account ON legacy_video_tasks(account_id, created_at DESC);
