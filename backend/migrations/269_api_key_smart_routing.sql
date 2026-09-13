-- 智能路由复用现有有序分组关联，旧 Key 默认保持原有路由方式。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS smart_routing BOOLEAN NOT NULL DEFAULT FALSE;

-- 三种分组模式互斥，避免直接写库产生无法确定选组语义的 Key。
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'api_keys_smart_routing_mode_check') THEN
        ALTER TABLE api_keys ADD CONSTRAINT api_keys_smart_routing_mode_check
            CHECK (NOT smart_routing OR (NOT is_composite AND group_id IS NULL));
    END IF;
END $$;
