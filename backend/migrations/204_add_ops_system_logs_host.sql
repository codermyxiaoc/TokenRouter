-- 记录每条新系统日志的产生日志主机；历史数据保持 NULL。
ALTER TABLE ops_system_logs
  ADD COLUMN IF NOT EXISTS host VARCHAR(255);
