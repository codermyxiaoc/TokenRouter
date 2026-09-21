-- 管理端排障用请求详情，独立于用量和错误表，避免向普通用户接口暴露正文。
-- 正文、请求头和响应头由应用层脱敏并截断后写入。
CREATE TABLE IF NOT EXISTS ops_request_details (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    client_request_id TEXT,
    method TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    inbound_endpoint TEXT,
    upstream_endpoint TEXT,
    platform TEXT,
    model TEXT,
    status_code INTEGER NOT NULL DEFAULT 0,
    stream BOOLEAN NOT NULL DEFAULT FALSE,
    request_headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_body TEXT,
    response_headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    response_body TEXT,
    request_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    response_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ops_request_details_created_at
    ON ops_request_details (created_at DESC);
