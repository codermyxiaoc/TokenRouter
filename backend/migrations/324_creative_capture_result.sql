-- 创作台捕获资金时在同一事务内保存实际扣款回执（金额与资金分配）。
-- 结算后续步骤（日志、终态）失败重试时，以此识别“已扣款”，不再按交付情况重判或释放原预占。
-- 历史任务为 NULL，恢复逻辑会按预占快照核对后补齐，不猜测金额。
ALTER TABLE creative_runs ADD COLUMN IF NOT EXISTS capture_result JSONB;
