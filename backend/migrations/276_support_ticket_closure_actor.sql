-- 记录后续工单结束操作的真实身份；历史结束记录缺少可信操作者，保持未记录。
ALTER TABLE support_tickets
    ADD COLUMN IF NOT EXISTS closed_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS closed_by_role VARCHAR(10) NOT NULL DEFAULT '';

-- 系统或未记录状态不关联用户；用户删除后保留其操作时的角色快照。
ALTER TABLE support_tickets DROP CONSTRAINT IF EXISTS support_tickets_closed_by_role_check;
ALTER TABLE support_tickets ADD CONSTRAINT support_tickets_closed_by_role_check CHECK (
    closed_by_role IN ('', 'user', 'admin', 'system')
    AND (closed_by_role IN ('user', 'admin') OR closed_by IS NULL)
);
