ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS native_compaction_v2 BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN usage_logs.native_compaction_v2 IS
    '仅当运行时识别为 OpenAI 原生远程 compaction v2 请求时为真';
