-- 邀请返利系统：创建上游 user_affiliates / user_affiliate_ledger 结构，并清理 fork 旧 referral 字段。
-- 该文件合并了此前未提交的 139-144 号迁移，避免未发布功能拆出多段历史。

-- 规范历史全局返利比例配置：旧兼容逻辑允许 0<x<=1 表示小数比例，这里统一换算成百分比。
UPDATE settings
SET value = to_char((value::numeric * 100), 'FM999999990.########'),
    updated_at = NOW()
WHERE key = 'affiliate_rebate_rate'
  AND value ~ '^-?[0-9]+(\\.[0-9]+)?$'
  AND value::numeric > 0
  AND value::numeric <= 1;

CREATE TABLE IF NOT EXISTS user_affiliates (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    aff_code VARCHAR(32) NOT NULL UNIQUE,
    inviter_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    aff_count INTEGER NOT NULL DEFAULT 0,
    aff_quota DECIMAL(20,8) NOT NULL DEFAULT 0,
    aff_frozen_quota DECIMAL(20,8) NOT NULL DEFAULT 0,
    aff_history_quota DECIMAL(20,8) NOT NULL DEFAULT 0,
    aff_rebate_rate_percent DECIMAL(5,2),
    aff_code_custom BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE user_affiliates
    ADD COLUMN IF NOT EXISTS aff_frozen_quota DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE user_affiliates
    ADD COLUMN IF NOT EXISTS aff_rebate_rate_percent DECIMAL(5,2);
ALTER TABLE user_affiliates
    ADD COLUMN IF NOT EXISTS aff_code_custom BOOLEAN NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS idx_user_affiliates_inviter_id ON user_affiliates(inviter_id);
CREATE INDEX IF NOT EXISTS idx_user_affiliates_aff_quota ON user_affiliates(aff_quota);
CREATE INDEX IF NOT EXISTS idx_user_affiliates_admin_settings
    ON user_affiliates (updated_at)
    WHERE aff_code_custom = true OR aff_rebate_rate_percent IS NOT NULL;

COMMENT ON TABLE user_affiliates IS '用户邀请返利信息';
COMMENT ON COLUMN user_affiliates.aff_code IS '用户邀请代码';
COMMENT ON COLUMN user_affiliates.inviter_id IS '邀请人用户ID';
COMMENT ON COLUMN user_affiliates.aff_count IS '累计邀请人数';
COMMENT ON COLUMN user_affiliates.aff_quota IS '当前可提取返利金额';
COMMENT ON COLUMN user_affiliates.aff_frozen_quota IS '当前冻结中的返利金额';
COMMENT ON COLUMN user_affiliates.aff_history_quota IS '累计返利历史金额';
COMMENT ON COLUMN user_affiliates.aff_rebate_rate_percent IS '专属返利比例（百分比 0-100，NULL 表示沿用全局）';
COMMENT ON COLUMN user_affiliates.aff_code_custom IS '邀请码是否由管理员改写过（用于专属用户筛选）';

CREATE TABLE IF NOT EXISTS user_affiliate_ledger (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    action VARCHAR(32) NOT NULL,
    amount DECIMAL(20,8) NOT NULL,
    source_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    source_order_id BIGINT NULL REFERENCES payment_orders(id) ON DELETE SET NULL,
    frozen_until TIMESTAMPTZ NULL,
    balance_after DECIMAL(20,8) NULL,
    aff_quota_after DECIMAL(20,8) NULL,
    aff_frozen_quota_after DECIMAL(20,8) NULL,
    aff_history_quota_after DECIMAL(20,8) NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS source_order_id BIGINT NULL REFERENCES payment_orders(id) ON DELETE SET NULL;
ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS frozen_until TIMESTAMPTZ NULL;
ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS balance_after DECIMAL(20,8) NULL;
ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS aff_quota_after DECIMAL(20,8) NULL;
ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS aff_frozen_quota_after DECIMAL(20,8) NULL;
ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS aff_history_quota_after DECIMAL(20,8) NULL;

CREATE INDEX IF NOT EXISTS idx_user_affiliate_ledger_user_id ON user_affiliate_ledger(user_id);
CREATE INDEX IF NOT EXISTS idx_user_affiliate_ledger_action ON user_affiliate_ledger(action);
CREATE INDEX IF NOT EXISTS idx_ual_frozen_thaw
    ON user_affiliate_ledger (user_id, frozen_until)
    WHERE frozen_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_user_affiliate_ledger_source_order_id
    ON user_affiliate_ledger(source_order_id)
    WHERE source_order_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_user_affiliate_ledger_rebate_lookup
    ON user_affiliate_ledger(action, source_order_id, user_id, source_user_id, created_at)
    WHERE action = 'accrue';

COMMENT ON TABLE user_affiliate_ledger IS '邀请返利资金流水（累计/转入）';
COMMENT ON COLUMN user_affiliate_ledger.action IS '流水动作：accrue 表示返利累计，transfer 表示转入余额';
COMMENT ON COLUMN user_affiliate_ledger.source_user_id IS '产生返利的被邀请用户；转余额流水为 NULL';
COMMENT ON COLUMN user_affiliate_ledger.source_order_id IS '产生该返利流水的充值订单；转余额或无法可靠回填的历史数据为 NULL';
COMMENT ON COLUMN user_affiliate_ledger.frozen_until IS '返利冻结截止时间；NULL 表示未冻结或已解冻';
COMMENT ON COLUMN user_affiliate_ledger.balance_after IS '邀请返利转余额后的用户余额快照；无法取得时为 NULL';
COMMENT ON COLUMN user_affiliate_ledger.aff_quota_after IS '邀请返利转余额后的可用返利额度快照；无法取得时为 NULL';
COMMENT ON COLUMN user_affiliate_ledger.aff_frozen_quota_after IS '邀请返利转余额后的冻结返利额度快照；无法取得时为 NULL';
COMMENT ON COLUMN user_affiliate_ledger.aff_history_quota_after IS '邀请返利转余额后的历史返利总额快照；无法取得时为 NULL';

-- 清理历史重复审计动作，随后添加订单动作维度的唯一约束，避免返利重复发放。
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (PARTITION BY order_id, action ORDER BY id) AS rn
    FROM payment_audit_logs
)
DELETE FROM payment_audit_logs p
USING ranked r
WHERE p.id = r.id
  AND r.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_audit_logs_order_action_uniq
ON payment_audit_logs(order_id, action);

-- 为功能上线前已经完成的余额订单写入跳过标记，避免上线后对历史订单补发邀请返利。
INSERT INTO payment_audit_logs (order_id, action, detail, operator, created_at)
SELECT po.id::text,
       'AFFILIATE_REBATE_SKIPPED',
       '{"reason":"baseline before affiliate rebate idempotency rollout"}',
       'system',
       NOW()
FROM payment_orders po
WHERE po.order_type = 'balance'
  AND po.status = 'COMPLETED'
  AND NOT EXISTS (
      SELECT 1
      FROM payment_audit_logs pal
      WHERE pal.order_id = po.id::text
        AND pal.action IN ('AFFILIATE_REBATE_APPLIED', 'AFFILIATE_REBATE_SKIPPED')
  );

-- 尽力回填该迁移前已经产生的返利流水订单关联。
-- 只有在同一订单只能匹配到一条返利流水时才回填，避免把多笔同额流水错误绑定到订单。
WITH rebate_audits AS (
    SELECT po.id AS order_id,
           po.user_id AS invitee_user_id,
           invitee_aff.inviter_id,
           rebate_detail.rebate_amount,
           pal.created_at AS audit_created_at
    FROM payment_audit_logs pal
    CROSS JOIN LATERAL (
        SELECT substring(
            pal.detail
            FROM '"rebateAmount"[[:space:]]*:[[:space:]]*(-?[0-9]+(\.[0-9]+)?)'
        )::numeric AS rebate_amount
    ) rebate_detail
    JOIN payment_orders po ON po.id::text = pal.order_id
    JOIN user_affiliates invitee_aff ON invitee_aff.user_id = po.user_id
    WHERE pal.action = 'AFFILIATE_REBATE_APPLIED'
      AND rebate_detail.rebate_amount IS NOT NULL
),
ranked_matches AS (
    SELECT ual.id AS ledger_id,
           ra.order_id,
           COUNT(*) OVER (PARTITION BY ra.order_id) AS order_match_count,
           COUNT(*) OVER (PARTITION BY ual.id) AS ledger_match_count,
           ROW_NUMBER() OVER (
               PARTITION BY ual.id
               ORDER BY ABS(EXTRACT(EPOCH FROM (ual.created_at - ra.audit_created_at))), ra.order_id
           ) AS ledger_rank
    FROM rebate_audits ra
    JOIN user_affiliate_ledger ual
      ON ual.action = 'accrue'
     AND ual.source_order_id IS NULL
     AND ual.user_id = ra.inviter_id
     AND ual.source_user_id = ra.invitee_user_id
     AND ABS(ual.amount - ra.rebate_amount) < 0.00000001
     AND ual.created_at BETWEEN ra.audit_created_at - INTERVAL '10 minutes'
                            AND ra.audit_created_at + INTERVAL '10 minutes'
)
UPDATE user_affiliate_ledger ual
SET source_order_id = ranked_matches.order_id,
    updated_at = NOW()
FROM ranked_matches
WHERE ual.id = ranked_matches.ledger_id
  AND ranked_matches.order_match_count = 1
  AND ranked_matches.ledger_match_count = 1
  AND ranked_matches.ledger_rank = 1
  AND NOT EXISTS (
      SELECT 1
      FROM user_affiliate_ledger existing
      WHERE existing.source_order_id = ranked_matches.order_id
        AND existing.action = 'accrue'
  );

-- 清理 fork 旧版邀请返利字段，统一使用上游 user_affiliates / user_affiliate_ledger。
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_referred_by_user_id_fkey;

DROP INDEX IF EXISTS users_referred_by_user_id_reward_granted_idx;
DROP INDEX IF EXISTS users_referred_by_user_id_idx;
DROP INDEX IF EXISTS users_referral_code_unique_active;
DROP INDEX IF EXISTS user_referral_code;
DROP INDEX IF EXISTS user_referred_by_user_id;

ALTER TABLE users DROP COLUMN IF EXISTS referral_reward_granted_at;
ALTER TABLE users DROP COLUMN IF EXISTS referral_reward_amount;
ALTER TABLE users DROP COLUMN IF EXISTS referred_by_user_id;
ALTER TABLE users DROP COLUMN IF EXISTS referral_code;

DELETE FROM settings WHERE key = 'referral_reward_amount';
