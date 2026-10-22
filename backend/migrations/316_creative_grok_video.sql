-- Grok 画布只保存发送防重与原任务关联；费用继续由 legacy_video_tasks 原账本处理。
CREATE TABLE IF NOT EXISTS creative_grok_video_executions (
    execution_id UUID PRIMARY KEY REFERENCES creative_canvas_executions(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id BIGINT NOT NULL CHECK (api_key_id > 0),
    group_id BIGINT NOT NULL CHECK (group_id > 0),
    phase VARCHAR(32) NOT NULL CHECK (phase IN ('provider_inflight','response_saved','accepted')),
    task_id VARCHAR(255) NOT NULL DEFAULT '',
    account_id BIGINT NOT NULL DEFAULT 0,
    http_status INTEGER NOT NULL DEFAULT 0 CHECK (http_status BETWEEN 0 AND 599),
    request_sent BOOLEAN NOT NULL DEFAULT FALSE,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((phase='accepted' AND task_id<>'' AND account_id>0) OR
           (phase<>'accepted' AND task_id='' AND account_id=0))
);
-- 同一个原 Key 和上游任务只能归属一个画布执行，避免重复归档或挪用别人的结果。
CREATE UNIQUE INDEX IF NOT EXISTS creative_grok_video_original_task_idx
    ON creative_grok_video_executions(api_key_id,task_id) WHERE task_id<>'';
