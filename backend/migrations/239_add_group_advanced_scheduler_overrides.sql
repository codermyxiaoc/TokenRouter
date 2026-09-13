-- 为高级调度分组保存稀疏参数覆盖；空对象表示全部继承网关通用设置。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS advanced_scheduler_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'groups_advanced_scheduler_overrides_object_check'
          AND conrelid = 'groups'::regclass
          AND contype = 'c'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_advanced_scheduler_overrides_object_check
            CHECK (jsonb_typeof(advanced_scheduler_overrides) = 'object');
    END IF;
END $$;

COMMENT ON COLUMN groups.advanced_scheduler_overrides IS '分组高级调度器稀疏覆盖；未设置字段继承网关通用设置';
