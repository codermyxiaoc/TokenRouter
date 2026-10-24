-- 图片执行独立持久预算；不修改音频任务、旧账本、掺水检测或分组权限。
ALTER TABLE creative_runs
    ADD COLUMN image_attempt_account_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN image_execution_deadline_at TIMESTAMPTZ;
ALTER TABLE creative_runs ADD CONSTRAINT creative_image_attempt_accounts_array
    CHECK (jsonb_typeof(image_attempt_account_ids) = 'array'
        AND jsonb_array_length(image_attempt_account_ids) <= 2);
-- 旧执行记住已知的最后账号，不能升级后再次选择它。
UPDATE creative_runs
SET image_attempt_account_ids = jsonb_build_array(account_id)
WHERE operation <> 'tts' AND account_id > 0
  AND provisioning_phase IN ('provider_inflight', 'provider_retry');
-- 旧版 provider_retry 不能证明未受理（网络/5xx 也曾授权重试），升级后保守转为核对。
-- 仅处理尚未结束的图片任务，不改旧成功结果、资金或音频任务。
UPDATE creative_runs
SET provisioning_phase = 'provider_inflight'
WHERE operation <> 'tts' AND provisioning_phase = 'provider_retry'
  AND status IN ('queued', 'running');
