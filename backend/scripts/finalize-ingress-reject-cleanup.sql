-- 入口拒绝日志清理的发布后收尾脚本。
--
-- 不要在滚动部署期间执行。仅在满足以下条件后运行：
--   1. 所有应用实例都已升级到不再读写 deleted_api_key_audits 和已弃用
--      ops_error_logs 列的版本；
--   2. cleanup-ingress-reject-logs 已完成试运行，并按需正式执行；
--   3. 已验证数据库备份或恢复点。

BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

DROP TABLE IF EXISTS deleted_api_key_audits;

ALTER TABLE IF EXISTS ops_error_logs
    DROP COLUMN IF EXISTS attempted_key_prefix,
    DROP COLUMN IF EXISTS deleted_key_owner_user_id,
    DROP COLUMN IF EXISTS deleted_key_name;

COMMIT;

-- 在正常维护窗口中单独执行：
-- VACUUM (ANALYZE) ops_error_logs;
