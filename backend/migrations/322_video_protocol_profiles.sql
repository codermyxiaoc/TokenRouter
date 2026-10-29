-- 声明式视频协议模板：每次保存生成新版本，同一键名最多一个启用版本；任务快照冻结整份规格，不引用本表。
CREATE TABLE IF NOT EXISTS video_protocol_profiles (
    id          BIGSERIAL PRIMARY KEY,
    profile_key TEXT        NOT NULL,
    version     INTEGER     NOT NULL,
    name        TEXT        NOT NULL,
    spec        JSONB       NOT NULL,
    spec_hash   TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'draft',
    created_by  BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT video_protocol_profiles_key_version UNIQUE (profile_key, version),
    CONSTRAINT video_protocol_profiles_status CHECK (status IN ('draft', 'active', 'disabled')),
    CONSTRAINT video_protocol_profiles_version_positive CHECK (version > 0)
);

-- 账号按键名解析当前启用版本；唯一部分索引保证同一键名只有一个启用版本。
CREATE UNIQUE INDEX IF NOT EXISTS idx_video_protocol_profiles_active
    ON video_protocol_profiles (profile_key)
    WHERE status = 'active';
