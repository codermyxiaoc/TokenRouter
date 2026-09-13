-- 保存内部操作标识，确保幂等响应写入结果不明确时可恢复已提交的分组副本。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS duplicate_operation_id VARCHAR(64);

CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_duplicate_operation_id_active
    ON groups (duplicate_operation_id)
    WHERE duplicate_operation_id IS NOT NULL AND deleted_at IS NULL;
