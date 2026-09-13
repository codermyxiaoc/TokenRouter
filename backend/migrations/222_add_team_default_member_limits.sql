-- 团队默认限额在新成员加入时复制到成员关系，已有成员保持原有自定义限额。
ALTER TABLE teams ADD COLUMN IF NOT EXISTS default_daily_limit_usd DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE teams ADD COLUMN IF NOT EXISTS default_weekly_limit_usd DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE teams ADD COLUMN IF NOT EXISTS default_monthly_limit_usd DECIMAL(20,8) NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'teams_default_member_limits_check'
    ) THEN
        ALTER TABLE teams ADD CONSTRAINT teams_default_member_limits_check CHECK (
            default_daily_limit_usd >= 0
            AND default_weekly_limit_usd >= 0
            AND default_monthly_limit_usd >= 0
        );
    END IF;
END $$;
