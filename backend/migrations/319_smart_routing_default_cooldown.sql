-- 只调整未来省略配置的新建记录；无法区分旧默认 60 与用户显式 60，故不回填存量值。
ALTER TABLE api_keys ALTER COLUMN smart_routing_cooldown_seconds SET DEFAULT 10;
