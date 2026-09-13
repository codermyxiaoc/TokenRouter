-- 统一为已有及新建 Key 保存智能路由冷却配置，普通和复合 Key 不使用此值。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS smart_routing_cooldown_seconds INTEGER NOT NULL DEFAULT 60;

-- 只保存配置，运行时冷却截止时间由共享缓存维护；0 关闭冷却但保留跨组重试。
COMMENT ON COLUMN api_keys.smart_routing_cooldown_seconds IS '智能路由分组上游失败后的冷却秒数，0 关闭冷却';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'api_keys_smart_routing_cooldown_check') THEN
        ALTER TABLE api_keys ADD CONSTRAINT api_keys_smart_routing_cooldown_check
            CHECK (smart_routing_cooldown_seconds BETWEEN 0 AND 3600);
    END IF;
END $$;
