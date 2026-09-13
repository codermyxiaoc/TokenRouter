-- 为本地内容审计和 OpenAI Cyber 告警补充付款用户与团队归属。

ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS billing_user_id BIGINT,
    ADD COLUMN IF NOT EXISTS team_id BIGINT;

ALTER TABLE content_moderation_cyber_warnings
    ADD COLUMN IF NOT EXISTS billing_user_id BIGINT,
    ADD COLUMN IF NOT EXISTS team_id BIGINT;

-- API Key 仍然存在时补齐团队归属；已删除 Key 的历史记录保持未知。
UPDATE content_moderation_logs AS logs
SET team_id = keys.team_id
FROM api_keys AS keys
WHERE logs.api_key_id = keys.id
  AND logs.team_id IS NULL
  AND keys.team_id IS NOT NULL;

UPDATE content_moderation_cyber_warnings AS warnings
SET team_id = keys.team_id
FROM api_keys AS keys
WHERE warnings.api_key_id = keys.id
  AND warnings.team_id IS NULL
  AND keys.team_id IS NOT NULL;

-- 本地审计的历史 user_id 是当时的付款用户，可以直接回填付款归属。
UPDATE content_moderation_logs
SET billing_user_id = user_id
WHERE billing_user_id IS NULL AND user_id IS NOT NULL;

-- Cyber 的历史 user_id 已是实际成员，优先从同一请求的用量记录恢复真实付款归属。
UPDATE content_moderation_cyber_warnings AS warnings
SET billing_user_id = usage.billing_user_id,
    team_id = COALESCE(warnings.team_id, usage.team_id)
FROM usage_logs AS usage
WHERE warnings.request_id = usage.request_id
  AND warnings.api_key_id = usage.api_key_id
  AND warnings.billing_user_id IS NULL;

-- 只有仍能确认为个人 Key 时才用历史 user_id 回填；无法识别的团队记录保持未知。
UPDATE content_moderation_cyber_warnings AS warnings
SET billing_user_id = warnings.user_id
FROM api_keys AS keys
WHERE warnings.api_key_id = keys.id
  AND keys.team_id IS NULL
  AND warnings.billing_user_id IS NULL
  AND warnings.user_id IS NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'content_moderation_logs_billing_user_id_fkey') THEN
        ALTER TABLE content_moderation_logs
            ADD CONSTRAINT content_moderation_logs_billing_user_id_fkey
            FOREIGN KEY (billing_user_id) REFERENCES users(id) ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'content_moderation_logs_team_id_fkey') THEN
        ALTER TABLE content_moderation_logs
            ADD CONSTRAINT content_moderation_logs_team_id_fkey
            FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'content_moderation_cyber_warnings_billing_user_id_fkey') THEN
        ALTER TABLE content_moderation_cyber_warnings
            ADD CONSTRAINT content_moderation_cyber_warnings_billing_user_id_fkey
            FOREIGN KEY (billing_user_id) REFERENCES users(id) ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'content_moderation_cyber_warnings_team_id_fkey') THEN
        ALTER TABLE content_moderation_cyber_warnings
            ADD CONSTRAINT content_moderation_cyber_warnings_team_id_fkey
            FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE SET NULL;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_billing_user_created_at
    ON content_moderation_logs(billing_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_team_created_at
    ON content_moderation_logs(team_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_content_moderation_cyber_warnings_billing_user_created_at
    ON content_moderation_cyber_warnings(billing_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_content_moderation_cyber_warnings_team_created_at
    ON content_moderation_cyber_warnings(team_id, created_at DESC);
