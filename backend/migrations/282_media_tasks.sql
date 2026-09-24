-- 媒体任务只保存可查询的状态投影；图片本体、提示词、上游凭据和原始响应不进入此表。
CREATE TABLE IF NOT EXISTS media_tasks (
    id BIGSERIAL PRIMARY KEY,
    source VARCHAR(32) NOT NULL CHECK (source IN ('async_image', 'grok_video', 'seedance_video')),
    task_id VARCHAR(255) NOT NULL,
    media_type VARCHAR(8) NOT NULL CHECK (media_type IN ('image', 'video')),
    platform VARCHAR(32) NOT NULL DEFAULT '',
    model VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL CHECK (status IN ('queued', 'processing', 'completed', 'failed', 'cancelled', 'expired')),
    upstream_status VARCHAR(64) NOT NULL DEFAULT '',
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    http_status INTEGER NOT NULL DEFAULT 0 CHECK (http_status BETWEEN 0 AND 599),
    error_message VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    UNIQUE (source, task_id, api_key_id),
    CHECK ((source = 'async_image') = (media_type = 'image'))
);
CREATE INDEX IF NOT EXISTS idx_media_tasks_user_created ON media_tasks(user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_media_tasks_created ON media_tasks(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_media_tasks_type_status ON media_tasks(media_type, status, created_at DESC);
