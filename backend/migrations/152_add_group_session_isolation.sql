ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS session_isolation_enabled BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_groups_session_isolation_enabled
    ON groups (session_isolation_enabled);
