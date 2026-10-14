-- 创作台额度平台随任务受理时冻结；空值保留历史任务的原分组回退规则，不猜测补写。
ALTER TABLE creative_runs
    ADD COLUMN IF NOT EXISTS quota_platform VARCHAR(32) NOT NULL DEFAULT '';

COMMENT ON COLUMN creative_runs.quota_platform IS '任务创建时的额度平台，空值表示历史任务按分组兼容解析';
