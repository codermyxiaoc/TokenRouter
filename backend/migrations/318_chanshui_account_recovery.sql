-- 自动恢复默认关闭；仅为本功能新自动停用的账号记录所有权，不接管历史人工停用。
ALTER TABLE chanshui_test_configs ADD COLUMN auto_recover BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE chanshui_test_configs ADD CONSTRAINT chk_chanshui_auto_recover
    CHECK (NOT auto_recover OR (enabled AND schedule_enabled AND 'fingerprint'=ANY(sections)
        AND (disable_on_adulteration OR minimum_score IS NOT NULL)));

CREATE TABLE chanshui_account_holds (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    account_revision BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- 账号下所有启用的保护模型必须在同一次停用之后重新通过，避免模型相互启停。
CREATE TABLE chanshui_recovery_checks (
    account_id BIGINT NOT NULL REFERENCES chanshui_account_holds(account_id) ON DELETE CASCADE,
    config_id BIGINT NOT NULL REFERENCES chanshui_test_configs(id) ON DELETE CASCADE,
    config_updated_at TIMESTAMPTZ NOT NULL,
    passed_at TIMESTAMPTZ,
    PRIMARY KEY(account_id,config_id)
);
