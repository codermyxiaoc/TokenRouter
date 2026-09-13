-- 账号调度统一使用 accounts.priority，移除未完整落地的分组关联优先级。
DROP INDEX IF EXISTS idx_account_groups_priority;
DROP INDEX IF EXISTS idx_account_groups_group_priority_account;
DROP INDEX IF EXISTS idx_account_groups_account_priority_group;

ALTER TABLE account_groups
    DROP COLUMN IF EXISTS priority;
