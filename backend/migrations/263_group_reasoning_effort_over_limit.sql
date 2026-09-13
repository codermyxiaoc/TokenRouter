-- 为分组增加推理强度超限时的访问控制。
-- 空值历史配置按 downgrade 处理，保持已有分组的自动降档行为。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS max_reasoning_effort_over_limit VARCHAR(20) NOT NULL DEFAULT 'downgrade';

COMMENT ON COLUMN groups.max_reasoning_effort_over_limit IS
    '推理强度超过分组上限时的处理方式：downgrade 自动降档，deny 拒绝访问';
