-- 为所有用户增加 API Key 数量上限；存量用户统一初始化为 100。
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS api_key_limit INTEGER NOT NULL DEFAULT 100;

-- 兼容列曾由预发布版本以可空形式创建的情况。
UPDATE users
SET api_key_limit = 100
WHERE api_key_limit IS NULL;

ALTER TABLE users
    ALTER COLUMN api_key_limit SET DEFAULT 100,
    ALTER COLUMN api_key_limit SET NOT NULL;

-- 使用命名约束便于后续迁移和测试稳定识别。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'users'::regclass
          AND conname = 'users_api_key_limit_non_negative'
    ) THEN
        ALTER TABLE users
            ADD CONSTRAINT users_api_key_limit_non_negative
            CHECK (api_key_limit >= 0) NOT VALID;
    END IF;
END
$$;

ALTER TABLE users
    VALIDATE CONSTRAINT users_api_key_limit_non_negative;
