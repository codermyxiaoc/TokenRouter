-- 长上下文用户定价已统一归属模型与渠道，账号 extra 不再参与结算。
DROP TRIGGER IF EXISTS accounts_propagate_openai_long_context_billing_extra ON accounts;
DROP TRIGGER IF EXISTS accounts_enforce_openai_long_context_billing_extra ON accounts;

DROP FUNCTION IF EXISTS public.propagate_openai_long_context_billing_extra_to_shadows();
DROP FUNCTION IF EXISTS public.enforce_openai_long_context_billing_extra();

-- 清理全部历史账号中的废弃键，同时保留其它账号扩展数据。
UPDATE accounts
SET extra = COALESCE(extra, '{}'::jsonb) - 'openai_long_context_billing_enabled'
WHERE COALESCE(extra, '{}'::jsonb) ? 'openai_long_context_billing_enabled';
