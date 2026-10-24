-- 掺水检测直接绑定上游账号，既有分组、渠道与用户账务不受此表影响。
-- 配置代次只追踪会改变检测身份/人工停用意图的字段，阻止停用再恢复的 ABA 旧结果。
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS chanshui_config_revision BIGINT NOT NULL DEFAULT 0;
CREATE OR REPLACE FUNCTION bump_chanshui_account_config_revision() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.credentials IS DISTINCT FROM OLD.credentials
       OR NEW.status IS DISTINCT FROM OLD.status
       OR NEW.schedulable IS DISTINCT FROM OLD.schedulable
       OR NEW.proxy_id IS DISTINCT FROM OLD.proxy_id
       OR NEW.parent_account_id IS DISTINCT FROM OLD.parent_account_id
       OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
       OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at
       OR NEW.platform IS DISTINCT FROM OLD.platform
       OR NEW.type IS DISTINCT FROM OLD.type
       OR (NEW.extra->'openai_text_route_mode') IS DISTINCT FROM (OLD.extra->'openai_text_route_mode') THEN
        NEW.chanshui_config_revision := OLD.chanshui_config_revision + 1;
    ELSE
        NEW.chanshui_config_revision := OLD.chanshui_config_revision;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_chanshui_account_config_revision
    BEFORE UPDATE OF credentials,status,schedulable,proxy_id,parent_account_id,expires_at,deleted_at,platform,type,extra ON accounts
    FOR EACH ROW EXECUTE FUNCTION bump_chanshui_account_config_revision();

CREATE TABLE IF NOT EXISTS chanshui_test_configs (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id),
    model VARCHAR(255) NOT NULL,
    protocol VARCHAR(32) NOT NULL CHECK (protocol IN ('auto','chat','anthropic','openai')),
    sections TEXT[] NOT NULL CHECK (cardinality(sections) BETWEEN 1 AND 8),
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 10 AND 600),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    schedule_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (interval_minutes BETWEEN 5 AND 10080),
    disable_on_adulteration BOOLEAN NOT NULL DEFAULT FALSE,
    minimum_score DOUBLE PRECISION CHECK (minimum_score >= 0 AND minimum_score <= 100),
    next_run_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(account_id,model,protocol)
);
CREATE TABLE IF NOT EXISTS chanshui_test_runs (
    id VARCHAR(64) PRIMARY KEY,
    config_id BIGINT NOT NULL REFERENCES chanshui_test_configs(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL CHECK (status IN ('queued','submitting','running','completed','error','unknown')),
    remote_id VARCHAR(256) NOT NULL DEFAULT '',
    remote_expires_at TIMESTAMPTZ,
    -- 报告与轻量摘要分列，历史列表不反复读取大报告。
    record JSONB NOT NULL,
    report JSONB,
    account_snapshot VARCHAR(128) NOT NULL DEFAULT '',
    account_revision BIGINT NOT NULL DEFAULT 0,
    account_updated_at TIMESTAMPTZ,
    config_updated_at TIMESTAMPTZ NOT NULL,
    lease_token VARCHAR(64) NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    next_poll_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_chanshui_one_active ON chanshui_test_runs(config_id)
    WHERE status IN ('queued','submitting','running');
CREATE INDEX IF NOT EXISTS idx_chanshui_due ON chanshui_test_runs(next_poll_at,lease_until)
    WHERE status IN ('queued','submitting','running');
CREATE INDEX IF NOT EXISTS idx_chanshui_history ON chanshui_test_runs(config_id,created_at DESC,id);
-- 所有实例共享一个提交闸门，远端每分钟最多一次创建请求；锁不跨越网络调用。
CREATE TABLE IF NOT EXISTS chanshui_submit_gate (
    id INTEGER PRIMARY KEY CHECK (id=1),
    next_allowed_at TIMESTAMPTZ NOT NULL
);
INSERT INTO chanshui_submit_gate(id,next_allowed_at) VALUES(1,'epoch') ON CONFLICT(id) DO NOTHING;
