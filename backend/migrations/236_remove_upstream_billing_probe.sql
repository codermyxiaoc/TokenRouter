-- 移除上游声明倍率探测的账号快照与持久化设置；其它账号扩展和系统设置保持不变。
UPDATE accounts
SET extra = COALESCE(extra, '{}'::jsonb)
    - 'upstream_billing_probe'
    - 'upstream_billing_probe_enabled'
WHERE COALESCE(extra, '{}'::jsonb) ?| ARRAY[
    'upstream_billing_probe',
    'upstream_billing_probe_enabled'
];

DELETE FROM settings
WHERE key IN (
    'upstream_billing_probe_settings',
    'openai_low_upstream_rate_priority_enabled',
    'openai_oauth_scheduling_rate_multiplier',
    'openai_advanced_scheduler_weight_upstream_cost'
);
