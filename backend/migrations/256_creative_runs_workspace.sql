-- 创作台浏览器工作区隔离：任务历史与输出访问按用户 + 浏览器工作区划分。
-- 迁移前任务无法判断原浏览器归属，保留 NULL 并由用户侧作用域查询隐藏。

ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS workspace_id VARCHAR(64);

-- 幂等键从用户级改为用户 + 工作区级，避免跨浏览器重放到同一任务。
DROP INDEX IF EXISTS creative_runs_idempotency_key_uq;
CREATE UNIQUE INDEX IF NOT EXISTS creative_runs_user_workspace_idempotency_key_uq
    ON creative_runs (user_id, workspace_id, idempotency_key)
    WHERE workspace_id IS NOT NULL AND idempotency_key IS NOT NULL AND idempotency_key <> '';

CREATE INDEX IF NOT EXISTS creative_runs_user_workspace_created_at_idx
    ON creative_runs (user_id, workspace_id, created_at DESC);
