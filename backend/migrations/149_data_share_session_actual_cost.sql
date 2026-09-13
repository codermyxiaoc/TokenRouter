-- 数据共享 session 的用户实际扣费积分；历史数据保持 NULL，避免未知扣费按 0 计入平均值。
ALTER TABLE data_share_sessions
    ADD COLUMN IF NOT EXISTS actual_cost DECIMAL(20, 10) NULL;
