-- API 文档分类支持两级目录：parent_id 为空表示一级分类，否则为所属一级分类的 ID。
-- 内置二级分类（如视频下的 OpenAI Videos）同样只在被改动时才有叠加行；NULL 对内置条目表示沿用内置上级。
ALTER TABLE api_doc_categories ADD COLUMN IF NOT EXISTS parent_id TEXT;
ALTER TABLE api_doc_categories DROP CONSTRAINT IF EXISTS api_doc_categories_parent_format;
ALTER TABLE api_doc_categories ADD CONSTRAINT api_doc_categories_parent_format
    CHECK (parent_id IS NULL OR (parent_id ~ '^[a-z0-9][a-z0-9_-]{0,127}$' AND parent_id <> id));
