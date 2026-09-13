-- 初版 271 已应用后保持不可变；约束范围修正与缓存失效扩展放入新迁移。
-- 仅在当前 api_keys 表缺少约束时补齐，不覆盖已保存的冷却配置。
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'api_keys_smart_routing_cooldown_check' AND conrelid = 'api_keys'::regclass) THEN
        ALTER TABLE api_keys ADD CONSTRAINT api_keys_smart_routing_cooldown_check
            CHECK (smart_routing_cooldown_seconds BETWEEN 0 AND 3600);
    END IF;
END $$;

-- 冷却配置进入事务 outbox，避免提交后即时 Redis 失效失败时其它实例继续使用旧值。
-- 保留 258 迁移的既有字段判断和触发器绑定，仅增加冷却配置的变更判断。
CREATE OR REPLACE FUNCTION enqueue_api_key_auth_cache_invalidation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM enqueue_auth_cache_invalidation(OLD.key);
        RETURN OLD;
    END IF;

    IF OLD.key IS DISTINCT FROM NEW.key
       OR OLD.status IS DISTINCT FROM NEW.status
       OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at
       OR OLD.user_id IS DISTINCT FROM NEW.user_id
       OR OLD.group_id IS DISTINCT FROM NEW.group_id
       OR OLD.ip_whitelist IS DISTINCT FROM NEW.ip_whitelist
       OR OLD.ip_blacklist IS DISTINCT FROM NEW.ip_blacklist
       OR OLD.expires_at IS DISTINCT FROM NEW.expires_at
       OR OLD.billing_mode IS DISTINCT FROM NEW.billing_mode
       OR OLD.preferred_subscription_id IS DISTINCT FROM NEW.preferred_subscription_id
       OR OLD.smart_routing_cooldown_seconds IS DISTINCT FROM NEW.smart_routing_cooldown_seconds THEN
        PERFORM enqueue_auth_cache_invalidation(OLD.key);
        IF NEW.deleted_at IS NULL AND NEW.key IS DISTINCT FROM OLD.key THEN
            PERFORM enqueue_auth_cache_invalidation(NEW.key);
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
