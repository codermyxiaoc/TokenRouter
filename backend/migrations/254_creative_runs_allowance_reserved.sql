-- 创作台计费修复：creative_runs 增加额度预记标记。
-- 复用批量图片余额预占 SQL 时，额度预记/回退需要在任务行上记录预记状态，
-- 语义与 batch_image_jobs.allowance_reserved 完全一致。

ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS allowance_reserved BOOLEAN NOT NULL DEFAULT false;
