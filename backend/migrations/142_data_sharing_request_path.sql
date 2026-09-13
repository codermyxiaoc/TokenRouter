ALTER TABLE data_share_sessions
    ADD COLUMN IF NOT EXISTS request_path VARCHAR(128) NOT NULL DEFAULT '';

UPDATE data_share_sessions
SET request_path = COALESCE(NULLIF(meta->>'request_path', ''), NULLIF(meta->>'inbound_endpoint', ''), '')
WHERE request_path = '';

CREATE INDEX IF NOT EXISTS idx_data_share_sessions_request_path
    ON data_share_sessions (request_path);
