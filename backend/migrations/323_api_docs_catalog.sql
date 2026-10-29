-- API 文档目录改为“内置 + 叠加”管理：只有管理员改动或新增的分类、文档才有行。
-- 可空字段表示沿用前端内置值（标题、所属分类、正文），未改动的内置文档随版本升级自动更新。
-- version 只随内容修改递增，用于多名管理员并发编辑时的冲突检测；排序调整不改变 version。
CREATE TABLE IF NOT EXISTS api_doc_categories (
    id          TEXT        PRIMARY KEY,
    title       TEXT,
    sort        INTEGER,
    hidden      BOOLEAN     NOT NULL DEFAULT FALSE,
    custom      BOOLEAN     NOT NULL DEFAULT FALSE,
    version     BIGINT      NOT NULL DEFAULT 1,
    updated_by  BIGINT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT api_doc_categories_id_format CHECK (id ~ '^[a-z0-9][a-z0-9_-]{0,127}$'),
    -- 自定义分类没有内置标题可回退，必须自带标题。
    CONSTRAINT api_doc_categories_custom_title CHECK (NOT custom OR (title IS NOT NULL AND btrim(title) <> ''))
);

CREATE TABLE IF NOT EXISTS api_docs (
    id           TEXT        PRIMARY KEY,
    category_id  TEXT,
    title        TEXT,
    markdown     TEXT,
    sort         INTEGER,
    hidden       BOOLEAN     NOT NULL DEFAULT FALSE,
    custom       BOOLEAN     NOT NULL DEFAULT FALSE,
    version      BIGINT      NOT NULL DEFAULT 1,
    updated_by   BIGINT,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT api_docs_id_format CHECK (id ~ '^[a-z0-9][a-z0-9_-]{0,127}$'),
    -- 自定义文档没有内置内容可回退，标题、分类、正文缺一不可。
    CONSTRAINT api_docs_custom_complete CHECK (NOT custom OR (
        title IS NOT NULL AND btrim(title) <> '' AND category_id IS NOT NULL
        AND markdown IS NOT NULL AND btrim(markdown) <> ''))
);

CREATE INDEX IF NOT EXISTS idx_api_docs_category ON api_docs (category_id) WHERE category_id IS NOT NULL;

-- 迁移旧版“逐篇改写正文”设置；旧设置键保留不删，便于回滚到上一版本。
-- 只接受合法 JSON 对象和合法文档 ID，损坏的旧值直接跳过，不阻断升级。
DO $$
DECLARE
    raw TEXT;
BEGIN
    SELECT value INTO raw FROM settings WHERE key = 'api_doc_overrides';
    IF raw IS NULL OR btrim(raw) = '' THEN
        RETURN;
    END IF;
    BEGIN
        INSERT INTO api_docs (id, markdown)
        SELECT e.key, e.value
        FROM jsonb_each_text(raw::jsonb) AS e
        WHERE e.key ~ '^[a-z0-9][a-z0-9_-]{0,127}$' AND btrim(e.value) <> ''
        ON CONFLICT (id) DO NOTHING;
    EXCEPTION WHEN invalid_text_representation OR invalid_parameter_value OR wrong_object_type THEN
        RAISE NOTICE 'skip invalid api_doc_overrides setting';
    END;
END $$;
