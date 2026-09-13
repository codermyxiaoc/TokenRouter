-- 将 OpenAI 实验调度器迁移为按分组启用的通用高级调度器。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS scheduler_type VARCHAR(16) NOT NULL DEFAULT 'basic';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'groups_scheduler_type_check'
          AND conrelid = 'groups'::regclass
          AND contype = 'c'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_scheduler_type_check
            CHECK (scheduler_type IN ('basic', 'advanced'));
    END IF;
END $$;

COMMENT ON COLUMN groups.scheduler_type IS '分组调度器类型：basic 或 advanced';

-- 新增列的默认值已保证旧总开关关闭或不存在时，所有存量分组保持 basic。
-- 旧总开关开启时，只有原来实际使用 OpenAI 调度器的 OpenAI/Grok 分组升级为高级模式。
UPDATE groups
SET scheduler_type = 'advanced'
WHERE platform IN ('openai', 'grok')
  AND EXISTS (
      SELECT 1
      FROM settings
      WHERE key = 'openai_advanced_scheduler_enabled'
        AND LOWER(TRIM(value)) = 'true'
  );

-- 迁移原有覆盖值；总开关不再有对应的通用设置，由分组 scheduler_type 取代。
INSERT INTO settings (key, value, updated_at)
SELECT mapping.new_key, COALESCE(legacy.value, mapping.default_value), NOW()
FROM (
    VALUES
        ('advanced_scheduler_sticky_weighted_enabled', 'openai_advanced_scheduler_sticky_weighted_enabled', 'false'),
        ('advanced_scheduler_subscription_priority_enabled', 'openai_advanced_scheduler_subscription_priority_enabled', 'false'),
        ('advanced_scheduler_lb_top_k', 'openai_advanced_scheduler_lb_top_k', ''),
        ('advanced_scheduler_weight_priority', 'openai_advanced_scheduler_weight_priority', ''),
        ('advanced_scheduler_weight_load', 'openai_advanced_scheduler_weight_load', ''),
        ('advanced_scheduler_weight_queue', 'openai_advanced_scheduler_weight_queue', ''),
        ('advanced_scheduler_weight_error_rate', 'openai_advanced_scheduler_weight_error_rate', ''),
        ('advanced_scheduler_weight_ttft', 'openai_advanced_scheduler_weight_ttft', ''),
        ('advanced_scheduler_weight_reset', 'openai_advanced_scheduler_weight_reset', ''),
        ('advanced_scheduler_weight_quota_headroom', 'openai_advanced_scheduler_weight_quota_headroom', ''),
        ('advanced_scheduler_weight_previous_response', 'openai_advanced_scheduler_weight_previous_response', ''),
        ('advanced_scheduler_weight_session_sticky', 'openai_advanced_scheduler_weight_session_sticky', '')
) AS mapping(new_key, old_key, default_value)
LEFT JOIN settings AS legacy ON legacy.key = mapping.old_key
ON CONFLICT (key) DO NOTHING;

DELETE FROM settings
WHERE key IN (
    'openai_advanced_scheduler_enabled',
    'openai_advanced_scheduler_sticky_weighted_enabled',
    'openai_advanced_scheduler_subscription_priority_enabled',
    'openai_advanced_scheduler_lb_top_k',
    'openai_advanced_scheduler_weight_priority',
    'openai_advanced_scheduler_weight_load',
    'openai_advanced_scheduler_weight_queue',
    'openai_advanced_scheduler_weight_error_rate',
    'openai_advanced_scheduler_weight_ttft',
    'openai_advanced_scheduler_weight_reset',
    'openai_advanced_scheduler_weight_quota_headroom',
    'openai_advanced_scheduler_weight_previous_response',
    'openai_advanced_scheduler_weight_session_sticky'
);
