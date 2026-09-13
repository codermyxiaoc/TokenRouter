-- 为 API Key 增加复合分组路由能力。

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS is_composite BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS api_key_composite_groups (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    prefix VARCHAR(32) NOT NULL,
    normalized_prefix VARCHAR(32) NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    data_sharing_notice_version INTEGER NOT NULL DEFAULT 0,
    data_sharing_confirmed_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_key_composite_groups_key_group
    ON api_key_composite_groups(api_key_id, group_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_key_composite_groups_key_prefix
    ON api_key_composite_groups(api_key_id, normalized_prefix);
CREATE INDEX IF NOT EXISTS idx_api_key_composite_groups_group_id
    ON api_key_composite_groups(group_id);
CREATE INDEX IF NOT EXISTS idx_api_key_composite_groups_api_key_sort
    ON api_key_composite_groups(api_key_id, sort_order, id);

ALTER TABLE batch_image_jobs
    ADD COLUMN IF NOT EXISTS group_id BIGINT,
    ADD COLUMN IF NOT EXISTS requested_model VARCHAR(512);

UPDATE batch_image_jobs
SET requested_model = model
WHERE requested_model IS NULL OR requested_model = '';

ALTER TABLE batch_image_jobs
    ALTER COLUMN requested_model SET DEFAULT '',
    ALTER COLUMN requested_model SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'batch_image_jobs_group_id_fkey'
    ) THEN
        ALTER TABLE batch_image_jobs
            ADD CONSTRAINT batch_image_jobs_group_id_fkey
            FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE SET NULL;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_batch_image_jobs_group_id
    ON batch_image_jobs(group_id);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'api_keys_composite_group_shape_check'
    ) THEN
        ALTER TABLE api_keys
            ADD CONSTRAINT api_keys_composite_group_shape_check
            CHECK (NOT is_composite OR group_id IS NULL);
    END IF;
END $$;

COMMENT ON COLUMN api_keys.is_composite IS '是否通过模型前缀在多个分组之间路由';
COMMENT ON TABLE api_key_composite_groups IS '复合 API Key 的分组前缀映射';
COMMENT ON COLUMN api_key_composite_groups.prefix IS '用户配置并用于展示的分组前缀';
COMMENT ON COLUMN api_key_composite_groups.normalized_prefix IS '用于大小写不敏感匹配的规范化前缀';
COMMENT ON COLUMN batch_image_jobs.group_id IS '批量图片提交时实际选择的分组';
COMMENT ON COLUMN batch_image_jobs.requested_model IS '客户端提交的模型名，复合 Key 场景保留分组前缀';
