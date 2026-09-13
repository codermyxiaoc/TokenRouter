-- OpenAI cyber 风控警告事件表，仅记录新版本上线后的命中事件。

CREATE TABLE IF NOT EXISTS content_moderation_cyber_warnings (
    id                  BIGSERIAL PRIMARY KEY,
    request_id          VARCHAR(128) NOT NULL DEFAULT '',
    user_id             BIGINT REFERENCES users(id) ON DELETE SET NULL,
    user_email          VARCHAR(255) NOT NULL DEFAULT '',
    api_key_id          BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    api_key_name        VARCHAR(100) NOT NULL DEFAULT '',
    group_id            BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    group_name          VARCHAR(255) NOT NULL DEFAULT '',
    account_id          BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    account_name        VARCHAR(255) NOT NULL DEFAULT '',
    endpoint            VARCHAR(128) NOT NULL DEFAULT '',
    model               VARCHAR(255) NOT NULL DEFAULT '',
    upstream_status     INT NOT NULL DEFAULT 0,
    warning_text        TEXT NOT NULL DEFAULT '',
    violation_count     INT NOT NULL DEFAULT 0,
    auto_banned         BOOLEAN NOT NULL DEFAULT FALSE,
    email_sent          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_content_moderation_cyber_warnings_created_at
    ON content_moderation_cyber_warnings(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_content_moderation_cyber_warnings_user_created_at
    ON content_moderation_cyber_warnings(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_content_moderation_cyber_warnings_account_created_at
    ON content_moderation_cyber_warnings(account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_content_moderation_cyber_warnings_auto_banned_created_at
    ON content_moderation_cyber_warnings(auto_banned, created_at DESC);
