-- 音频执行复用创作台已有任务、资金预占和结果账本；仅扩展画布执行类型。
ALTER TABLE creative_canvas_executions DROP CONSTRAINT IF EXISTS creative_canvas_executions_kind_check;
ALTER TABLE creative_canvas_executions ADD CONSTRAINT creative_canvas_executions_kind_check CHECK (kind IN ('image','video','audio'));
