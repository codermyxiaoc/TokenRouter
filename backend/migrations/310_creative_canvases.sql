-- 云端画布独立保存文档及素材元数据，不改动旧图片和视频账本。
CREATE TABLE IF NOT EXISTS creative_canvases (
    id UUID PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title VARCHAR(160) NOT NULL,
    document JSONB NOT NULL DEFAULT '{"nodes":[],"connections":[],"viewport":{"x":0,"y":0,"k":1},"backgroundMode":"lines"}',
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS creative_canvases_owner_idx ON creative_canvases(user_id, updated_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS creative_canvas_executions (
    id UUID PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    canvas_id UUID NOT NULL REFERENCES creative_canvases(id),
    node_id VARCHAR(128) NOT NULL,
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('image','video')),
    params JSONB NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    fingerprint VARCHAR(64) NOT NULL,
    task_id VARCHAR(160) NOT NULL DEFAULT '',
    status VARCHAR(24) NOT NULL DEFAULT 'queued',
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    error_message VARCHAR(512) NOT NULL DEFAULT '',
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    next_poll_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS creative_canvas_executions_owner_idx ON creative_canvas_executions(user_id,canvas_id,created_at DESC);
CREATE INDEX IF NOT EXISTS creative_canvas_executions_due_idx ON creative_canvas_executions(next_poll_at) WHERE status IN ('queued','running');

CREATE TABLE IF NOT EXISTS creative_canvas_assets (
    id UUID PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    canvas_id UUID NOT NULL REFERENCES creative_canvases(id),
    execution_id UUID REFERENCES creative_canvas_executions(id),
    output_index INTEGER NOT NULL DEFAULT 0,
    media_type VARCHAR(16) NOT NULL,
    mime_type VARCHAR(96) NOT NULL,
    byte_size BIGINT NOT NULL CHECK (byte_size >= 0),
    sha256 VARCHAR(64) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'staging' CHECK (status IN ('staging','ready','deleted','purged')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    UNIQUE(execution_id,output_index)
);
CREATE INDEX IF NOT EXISTS creative_canvas_assets_owner_idx ON creative_canvas_assets(user_id,canvas_id,created_at DESC);
CREATE INDEX IF NOT EXISTS creative_canvas_assets_expiry_idx ON creative_canvas_assets(expires_at) WHERE status <> 'purged';
