ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS free_openai_fast BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.free_openai_fast IS
    'OpenAI/Composite 分组的 Fast/priority 请求按 Standard 价格向用户计费';
