ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS data_sharing_enabled BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_groups_data_sharing_enabled
    ON groups (data_sharing_enabled);

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS data_sharing_notice_version INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS data_sharing_confirmed_group_id BIGINT NULL,
    ADD COLUMN IF NOT EXISTS data_sharing_confirmed_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS idx_api_keys_data_sharing_confirmed_group_id
    ON api_keys (data_sharing_confirmed_group_id);

CREATE TABLE IF NOT EXISTS data_share_sessions (
    id BIGSERIAL PRIMARY KEY,
    trajectory_id VARCHAR(128) NOT NULL UNIQUE,
    session_id VARCHAR(256) NOT NULL,
    dataset VARCHAR(128) NOT NULL,
    provider VARCHAR(50) NOT NULL,
    model VARCHAR(100) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'completed',
    is_final_snapshot BOOLEAN NOT NULL DEFAULT TRUE,
    source_request_count INTEGER NOT NULL DEFAULT 0,
    system_prompt TEXT NULL,
    tools JSONB NOT NULL DEFAULT '[]'::jsonb,
    messages JSONB NOT NULL DEFAULT '[]'::jsonb,
    usage JSONB NOT NULL DEFAULT '{}'::jsonb,
    meta JSONB NOT NULL DEFAULT '{}'::jsonb,
    session_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    exportable BOOLEAN NOT NULL DEFAULT FALSE,
    quality_status VARCHAR(20) NOT NULL DEFAULT 'invalid',
    quality_errors JSONB NOT NULL DEFAULT '[]'::jsonb,
    storage_bytes BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_data_share_sessions_session_id
    ON data_share_sessions (session_id);
CREATE INDEX IF NOT EXISTS idx_data_share_sessions_user_id
    ON data_share_sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_data_share_sessions_api_key_id
    ON data_share_sessions (api_key_id);
CREATE INDEX IF NOT EXISTS idx_data_share_sessions_group_id
    ON data_share_sessions (group_id);
CREATE INDEX IF NOT EXISTS idx_data_share_sessions_provider
    ON data_share_sessions (provider);
CREATE INDEX IF NOT EXISTS idx_data_share_sessions_model
    ON data_share_sessions (model);
CREATE INDEX IF NOT EXISTS idx_data_share_sessions_exportable
    ON data_share_sessions (exportable);
CREATE INDEX IF NOT EXISTS idx_data_share_sessions_quality_status
    ON data_share_sessions (quality_status);
CREATE INDEX IF NOT EXISTS idx_data_share_sessions_created_at
    ON data_share_sessions (created_at);
CREATE INDEX IF NOT EXISTS idx_data_share_sessions_updated_at
    ON data_share_sessions (updated_at);
