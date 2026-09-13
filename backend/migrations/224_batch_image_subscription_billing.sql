ALTER TABLE batch_image_jobs
    ADD COLUMN IF NOT EXISTS balance_hold_amount DECIMAL(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS subscription_hold_allocations JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS subscription_rate_multiplier DECIMAL(20,10) NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS balance_rate_multiplier DECIMAL(20,10) NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS plan_group_rate_multiplier_enabled BOOLEAN NOT NULL DEFAULT TRUE;

UPDATE batch_image_jobs
SET balance_hold_amount = COALESCE(balance_hold_amount, 0),
    subscription_hold_allocations = COALESCE(subscription_hold_allocations, '[]'::jsonb)
WHERE balance_hold_amount IS NULL
   OR subscription_hold_allocations IS NULL;

ALTER TABLE batch_image_jobs
    ALTER COLUMN balance_hold_amount SET DEFAULT 0,
    ALTER COLUMN balance_hold_amount SET NOT NULL,
    ALTER COLUMN subscription_hold_allocations SET DEFAULT '[]'::jsonb,
    ALTER COLUMN subscription_hold_allocations SET NOT NULL;

-- 迁移前的批量任务只会冻结余额，必须回填拆分结果以保证在途任务继续结算或释放。
UPDATE batch_image_jobs
SET balance_hold_amount = GREATEST(COALESCE(hold_amount, estimated_cost, 0), 0)
WHERE balance_hold_amount = 0
  AND COALESCE(hold_amount, estimated_cost, 0) > 0;

COMMENT ON COLUMN batch_image_jobs.balance_hold_amount IS '批量图片提交时实际冻结的按量余额';
COMMENT ON COLUMN batch_image_jobs.subscription_hold_allocations IS '批量图片提交时预占的订阅额度明细';
COMMENT ON COLUMN batch_image_jobs.subscription_rate_multiplier IS '订阅未配置套餐分组倍率时使用的默认倍率';
COMMENT ON COLUMN batch_image_jobs.balance_rate_multiplier IS '订阅额度不足后使用的按量倍率';
COMMENT ON COLUMN batch_image_jobs.plan_group_rate_multiplier_enabled IS '是否允许套餐分组倍率覆盖订阅默认倍率';
