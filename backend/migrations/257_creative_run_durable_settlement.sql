-- 创作台 durable 状态机与 outbox：图片本体仍只保存于 Redis 临时存储。
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS provisioning_phase VARCHAR(32) NOT NULL DEFAULT 'created';
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS provider_result_recorded_at TIMESTAMPTZ;
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS settlement_attempt_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS release_attempt_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS next_reconcile_at TIMESTAMPTZ;
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS last_reconcile_error TEXT;
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS release_target_status VARCHAR(20) NOT NULL DEFAULT 'failed';

DROP INDEX IF EXISTS creative_runs_status_idx;
CREATE INDEX IF NOT EXISTS creative_runs_status_idx
    ON creative_runs (status)
    WHERE status IN ('queued', 'running', 'provider_succeeded', 'settlement_pending', 'release_pending');

CREATE TABLE IF NOT EXISTS creative_run_outbox (
    id BIGSERIAL PRIMARY KEY,
    run_id VARCHAR(64) NOT NULL REFERENCES creative_runs(run_id) ON DELETE CASCADE,
    operation VARCHAR(32) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_token VARCHAR(128),
    lease_until TIMESTAMPTZ,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT creative_run_outbox_operation_ck CHECK (operation IN ('provision', 'settle', 'release')),
    CONSTRAINT creative_run_outbox_status_ck CHECK (status IN ('pending', 'leased', 'done', 'cancelled')),
    CONSTRAINT creative_run_outbox_run_operation_uq UNIQUE (run_id, operation)
);

CREATE INDEX IF NOT EXISTS creative_run_outbox_claim_idx
    ON creative_run_outbox (available_at, id)
    WHERE status IN ('pending', 'leased');
CREATE INDEX IF NOT EXISTS creative_run_outbox_run_idx ON creative_run_outbox (run_id, status);

UPDATE creative_runs
SET provisioning_phase = CASE
    WHEN status IN ('succeeded', 'failed', 'cancelled', 'result_lost') THEN 'complete'
    ELSE 'enqueued'
END
WHERE provisioning_phase = 'created';

-- 创作台每个用户/分组只允许一个未软删除的托管 Key，防止并发首次创建产生重复隐藏 Key。
CREATE UNIQUE INDEX IF NOT EXISTS api_keys_creative_managed_user_group_uq
    ON api_keys (user_id, group_id, managed_by)
    WHERE deleted_at IS NULL AND managed_by = 'creative_studio' AND group_id IS NOT NULL;
