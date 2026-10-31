-- 创作台图片按 token 结算：渠道/分组定价为 token 的模型与 API 一致按上游用量扣费。
-- billing_mode 为空表示按张（历史任务保持不变），provider_usage 保存执行成功时算定的用量与费用。
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS billing_mode VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS provider_usage JSONB;
