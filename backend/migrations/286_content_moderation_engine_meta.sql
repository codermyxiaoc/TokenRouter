-- 仅追加审核来源元数据；旧记录和旧版本写入继续允许为空。
ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS engine_meta JSONB;
