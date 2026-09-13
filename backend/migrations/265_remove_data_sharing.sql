-- 数据共享功能已移除：先清理导出元数据，再删除完整会话载荷。
DROP TABLE IF EXISTS data_share_export_artifacts;
DROP TABLE IF EXISTS data_share_sessions;

DROP INDEX IF EXISTS idx_groups_data_sharing_enabled;
ALTER TABLE IF EXISTS groups
    DROP COLUMN IF EXISTS data_sharing_enabled;

DROP INDEX IF EXISTS idx_api_keys_data_sharing_confirmed_group_id;
ALTER TABLE IF EXISTS api_keys
    DROP COLUMN IF EXISTS data_sharing_notice_version,
    DROP COLUMN IF EXISTS data_sharing_confirmed_group_id,
    DROP COLUMN IF EXISTS data_sharing_confirmed_at;

ALTER TABLE IF EXISTS api_key_composite_groups
    DROP COLUMN IF EXISTS data_sharing_notice_version,
    DROP COLUMN IF EXISTS data_sharing_confirmed_at;

-- 仅移除数据共享专属设置，保留所有其他运行设置。
DELETE FROM settings
WHERE key IN (
    'data_sharing_enabled',
    'data_sharing_notice_content',
    'data_sharing_notice_version',
    'data_sharing_capture_skip_rules',
    'data_sharing_export_ticket_key',
    'data_sharing_export_remote_config',
    'data_sharing_export_remote_prefix',
    'data_sharing_storage_limit_bytes',
    'data_sharing_capture_runtime'
);

-- 历史值可能是无效 JSON，或是 JSONB 无法表示的合法 JSON；这些转换错误不能阻断破坏性迁移。
DO $$
DECLARE
    backup_content TEXT;
    backup_json JSONB;
BEGIN
    SELECT value
    INTO backup_content
    FROM settings
    WHERE key = 'backup_content_config'
    FOR UPDATE;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    BEGIN
        backup_json := backup_content::jsonb;
    EXCEPTION
        -- JSONB 转换只可能在此块内产生预期的数据异常；更新阶段的错误不在捕获范围内。
        WHEN data_exception OR program_limit_exceeded THEN
            RAISE NOTICE 'skip unusable backup_content_config while removing data sharing (SQLSTATE %)', SQLSTATE;
            RETURN;
    END;

    IF jsonb_typeof(backup_json) = 'object' THEN
        UPDATE settings
        SET value = (backup_json - 'include_data_share_sessions')::text,
            updated_at = NOW()
        WHERE key = 'backup_content_config';
    END IF;
END
$$;
