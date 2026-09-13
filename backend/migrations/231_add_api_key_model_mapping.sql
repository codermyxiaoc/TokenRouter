-- API Key 模型重定向规则保存为来源模型到目标模型的映射对象。
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS model_mapping JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 兼容曾由开发版本创建为可空列的数据库，确保运行时无需处理 NULL。
UPDATE api_keys
SET model_mapping = '{}'::jsonb
WHERE model_mapping IS NULL;

ALTER TABLE api_keys
    ALTER COLUMN model_mapping SET DEFAULT '{}'::jsonb,
    ALTER COLUMN model_mapping SET NOT NULL;

COMMENT ON COLUMN api_keys.model_mapping
    IS 'API Key 自定义模型重定向规则，键为客户端模型，值为内部目标模型';
