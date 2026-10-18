-- 文本任务复用正式 Token 结算，仅持久化私有输出、不可变账务证据与同事务资金回执。
ALTER TABLE creative_canvas_executions DROP CONSTRAINT IF EXISTS creative_canvas_executions_kind_check;
ALTER TABLE creative_canvas_executions ADD CONSTRAINT creative_canvas_executions_kind_check CHECK (kind IN ('image','video','audio','text'));
ALTER TABLE creative_canvas_executions ADD COLUMN IF NOT EXISTS output_text TEXT NOT NULL DEFAULT '';
ALTER TABLE creative_canvas_executions ADD COLUMN IF NOT EXISTS text_phase VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE creative_canvas_executions ADD COLUMN IF NOT EXISTS text_response_status INTEGER NOT NULL DEFAULT 0;
ALTER TABLE creative_canvas_executions ADD COLUMN IF NOT EXISTS text_billing_evidence JSONB;
ALTER TABLE creative_canvas_executions ADD COLUMN IF NOT EXISTS text_billing_result JSONB;
