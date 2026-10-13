-- 每日礼包按北京时间分日，累计充值/消费不跨日清零；奖励账本不可作为充值本金。
INSERT INTO settings(key,value) VALUES ('daily_activity_config', '{"enabled":false,"started_at":null,"daily_gift":{"threshold":10,"reward_max":0.5},"recharge":{"threshold":50,"reward_max":0.8},"consumption":{"threshold":50,"reward_max":0.8}}') ON CONFLICT(key) DO NOTHING;

CREATE TABLE daily_activity_claim_batches (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    idempotency_key VARCHAR(100) NOT NULL,
    activity_type VARCHAR(20) NOT NULL CHECK (activity_type IN ('daily_gift','recharge','consumption')),
    claim_all BOOLEAN NOT NULL,
    result JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id,idempotency_key)
);
CREATE TABLE daily_activity_claims (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES daily_activity_claim_batches(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    activity_type VARCHAR(20) NOT NULL CHECK (activity_type IN ('daily_gift','recharge','consumption')),
    activity_day DATE NOT NULL,
    amount NUMERIC(20,2) NOT NULL CHECK (amount >= 0.10),
    threshold NUMERIC(20,2) NOT NULL CHECK (threshold > 0),
    reward_max NUMERIC(20,2) NOT NULL CHECK (reward_max >= 0.10),
    eligible_amount NUMERIC NOT NULL CHECK (eligible_amount >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX idx_daily_activity_daily_gift_once ON daily_activity_claims(user_id,activity_day) WHERE activity_type='daily_gift';
CREATE INDEX idx_daily_activity_claim_history ON daily_activity_claims(user_id,id DESC);
CREATE INDEX idx_daily_activity_claim_counts ON daily_activity_claims(user_id,activity_type,activity_day);

-- 真实结算事务写入消费事件；预留、释放、奖励入账不产生消费事件。
CREATE TABLE daily_activity_spending_events (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_key TEXT NOT NULL UNIQUE,
    amount NUMERIC(24,12) NOT NULL CHECK (amount >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_daily_activity_spending_user_time ON daily_activity_spending_events(user_id,created_at);

-- 与消费事件同事务更新累计额，领奖与页面查询不扫描用户全部历史请求。
CREATE TABLE daily_activity_spending_totals (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    amount NUMERIC(24,12) NOT NULL DEFAULT 0 CHECK (amount >= 0)
);
CREATE INDEX idx_daily_activity_payment_user_completed ON payment_orders(user_id,completed_at) WHERE order_type='balance';
