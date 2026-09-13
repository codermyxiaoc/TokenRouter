-- 记录请求是否实际应用长上下文价格，便于用量历史解释计费结果，无需反推费用。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS long_context_billing_applied BOOLEAN NOT NULL DEFAULT FALSE;
