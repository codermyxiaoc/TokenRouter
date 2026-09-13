-- 为订阅套餐价格增加仅用于展示的 ISO 4217 币种标注；空值保持存量套餐展示不变。
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS currency VARCHAR(3) NOT NULL DEFAULT '';
