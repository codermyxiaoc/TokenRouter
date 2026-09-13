ALTER TABLE data_share_sessions
    ADD COLUMN IF NOT EXISTS user_agent VARCHAR(512) NOT NULL DEFAULT '';

UPDATE data_share_sessions
SET user_agent = LEFT(COALESCE(NULLIF(meta->>'user_agent', ''), ''), 512)
WHERE user_agent = '';

CREATE INDEX IF NOT EXISTS idx_data_share_sessions_user_agent
    ON data_share_sessions (user_agent);
