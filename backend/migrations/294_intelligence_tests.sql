-- 降智检测独立保存配置与观测结果，不影响网关路由、计费或渠道可用性。
CREATE TABLE IF NOT EXISTS intelligence_test_configs (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES groups(id),
    model VARCHAR(255) NOT NULL,
    benchmark VARCHAR(16) NOT NULL CHECK (benchmark IN ('candy','drawing')),
    base_url TEXT NOT NULL,
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
    protocol VARCHAR(32) NOT NULL,
    reasoning_effort VARCHAR(32) NOT NULL DEFAULT '',
    service_tier VARCHAR(32) NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    schedule_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (interval_minutes BETWEEN 5 AND 10080),
    next_run_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(group_id,model,benchmark)
);
CREATE TABLE IF NOT EXISTS intelligence_test_runs (
    id VARCHAR(64) PRIMARY KEY,
    config_id BIGINT NOT NULL REFERENCES intelligence_test_configs(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL,
    remote_id VARCHAR(256) NOT NULL DEFAULT '',
    -- 列表摘要与大正文分列，读取状态无需解压 HTML 或答案的 TOAST 数据。
    record JSONB NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    lease_token VARCHAR(64) NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    next_poll_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- 每份配置最多一个在途检测，手动与定时请求共享此约束。
CREATE UNIQUE INDEX IF NOT EXISTS idx_intelligence_one_active ON intelligence_test_runs(config_id)
    WHERE status IN ('queued','submitting','running');
CREATE INDEX IF NOT EXISTS idx_intelligence_due ON intelligence_test_runs(next_poll_at,lease_until)
    WHERE status IN ('queued','submitting','running');
CREATE INDEX IF NOT EXISTS idx_intelligence_history ON intelligence_test_runs(config_id,created_at DESC,id);
