-- 媒体任务的人工/超时最终决定与资金效果同事务落库，不能靠列表状态推测是否扣费。
ALTER TABLE media_tasks ADD COLUMN IF NOT EXISTS billing_review_retry_at TIMESTAMPTZ;
CREATE TABLE IF NOT EXISTS media_task_billing_resolutions (
    media_task_id BIGINT PRIMARY KEY REFERENCES media_tasks(id) ON DELETE RESTRICT,
    action VARCHAR(16) NOT NULL CHECK (action IN ('charge','release','expire')),
    actor_id BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    reason VARCHAR(500) NOT NULL,
    billing_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    actor_user_id BIGINT NOT NULL,
    api_key_id BIGINT NOT NULL,
    reserved_amount NUMERIC(30,10) NOT NULL CHECK (reserved_amount >= 0),
    amount NUMERIC(30,10) NOT NULL CHECK (amount >= 0),
    balance_amount NUMERIC(20,8) NOT NULL CHECK (balance_amount >= 0),
    subscription_amount NUMERIC(30,10) NOT NULL CHECK (subscription_amount >= 0),
    resolved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((action = 'expire' AND actor_id IS NULL) OR (action <> 'expire' AND actor_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_media_tasks_failed_billing_review
    ON media_tasks ((COALESCE(completed_at, updated_at)), id)
    WHERE status IN ('failed','cancelled','expired') AND source IN ('async_image','video');
