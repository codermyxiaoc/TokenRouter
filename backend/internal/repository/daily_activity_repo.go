package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/shopspring/decimal"
)

type dailyActivityRepository struct{ db *sql.DB }

func NewDailyActivityRepository(db *sql.DB) service.DailyActivityRepository {
	return &dailyActivityRepository{db: db}
}

type dailyActivityQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readDailyActivityConfig(ctx context.Context, q dailyActivityQuerier, lock string) (*service.DailyActivityConfig, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`+lock, service.DailyActivitySettingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) && lock == "" {
		c := service.DefaultDailyActivityConfig()
		return &c, nil
	}
	if err != nil {
		return nil, err
	}
	var c service.DailyActivityConfig
	if err = json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("decode daily activity config: %w", err)
	}
	if err = service.ValidateDailyActivityConfig(c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *dailyActivityRepository) GetConfig(ctx context.Context) (*service.DailyActivityConfig, error) {
	return readDailyActivityConfig(ctx, r.db, "")
}

// SaveConfig 锁定唯一设置行，首次启用时间由数据库生成，关闭再开启不重置累计起点。
func (r *dailyActivityRepository) SaveConfig(ctx context.Context, c service.DailyActivityConfig) (*service.DailyActivityConfig, error) {
	if err := service.ValidateDailyActivityConfig(c); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	old, err := readDailyActivityConfig(ctx, tx, " FOR UPDATE")
	if err != nil {
		return nil, err
	}
	c.StartedAt = old.StartedAt
	if c.Enabled && c.StartedAt == nil {
		var now time.Time
		// clock_timestamp 在等待锁之后取时间，避免把排队期间的历史收入计入。
		if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return nil, err
		}
		c.StartedAt = &now
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE settings SET value=$1,updated_at=clock_timestamp() WHERE key=$2`, string(raw), service.DailyActivitySettingKey); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &c, nil
}

// dailyActivityRechargeSQL 使用余额充值本金、外部订阅实付数值与已开启来源的入账事件。
// 订阅按支付订单实付数值累计，不换算币种；站内余额购买订阅始终排除。
// 关闭来源只停止产生新事件，已累计金额与已领取次数不能被重新解释。
// 曾履约成功的订单在退款各阶段都保留原累计金额，礼包仍按原到账日期归属。
const dailyActivityRechargeSQL = `SELECT COALESCE(SUM(amount),0)::text FROM (
 SELECT CASE WHEN order_type='subscription' THEN GREATEST(pay_amount,0) ELSE GREATEST(amount-COALESCE(bonus_amount,0),0) END AS amount
 FROM payment_orders WHERE user_id=$1 AND order_type IN ('balance','subscription') AND payment_type<>'balance'
 AND status IN ('COMPLETED','REFUND_REQUESTED','REFUNDING','REFUND_PENDING','PARTIALLY_REFUNDED','REFUNDED','REFUND_FAILED') AND completed_at >= $2 AND completed_at < $3
 UNION ALL
 SELECT amount FROM daily_activity_credit_events
 WHERE user_id=$1 AND created_at >= $2 AND created_at < $3
) AS credits`

func dailyActivityDecimal(ctx context.Context, q dailyActivityQuerier, query string, args ...any) (decimal.Decimal, error) {
	var raw string
	if err := q.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromString(raw)
}

func dailyActivityState(kind string, rule service.DailyActivityRule, eligible decimal.Decimal, claimed int64) (service.DailyActivityState, error) {
	threshold := decimal.NewFromFloat(rule.Threshold)
	// 先整除再转整数，避免0.3/0.1一类浮点舍入丢失或多发机会。
	units, _ := eligible.QuoRem(threshold, 0)
	if units.GreaterThan(decimal.NewFromInt(9007199254740991)) {
		return service.DailyActivityState{}, fmt.Errorf("daily activity entitlement count overflow")
	}
	earned := units.IntPart()
	if kind == "daily_gift" && earned > 1 {
		earned = 1
	}
	available := earned - claimed
	if available < 0 {
		available = 0
	}
	amount, _ := eligible.Float64()
	progress, _ := eligible.Div(threshold).Float64()
	return service.DailyActivityState{Enabled: rule.Enabled, Type: kind, Threshold: rule.Threshold, RewardMax: rule.RewardMax, EligibleAmount: amount, ClaimedCount: claimed, AvailableCount: available, Progress: progress}, nil
}

func dailyActivityStatus(ctx context.Context, q dailyActivityQuerier, uid int64, c *service.DailyActivityConfig, now time.Time) (*service.DailyActivityStatus, error) {
	day, dayStart, dayEnd := service.DailyActivityDay(now)
	out := &service.DailyActivityStatus{Enabled: c.Enabled && c.StartedAt != nil, StartedAt: c.StartedAt, IncludeRedeemBalance: c.IncludeRedeemBalance, IncludeAdminBalance: c.IncludeAdminBalance, Day: day, Activities: make([]service.DailyActivityState, 0, 3)}
	var daily, recharge, spent decimal.Decimal
	if c.StartedAt != nil {
		start := *c.StartedAt
		if dayStart.Before(start) {
			dayStart = start
		}
		var err error
		daily, err = dailyActivityDecimal(ctx, q, dailyActivityRechargeSQL, uid, dayStart, dayEnd)
		if err != nil {
			return nil, err
		}
		recharge, err = dailyActivityDecimal(ctx, q, dailyActivityRechargeSQL, uid, start, now)
		if err != nil {
			return nil, err
		}
		// 累计表只接收首次开启之后的最终结算事件，按用户主键读取避免历史用量放大查询成本。
		spent, err = dailyActivityDecimal(ctx, q, `SELECT COALESCE((SELECT amount FROM daily_activity_spending_totals WHERE user_id=$1),0)::text`, uid)
		if err != nil {
			return nil, err
		}
	}
	for i, kind := range []string{"daily_gift", "recharge", "consumption"} {
		var claimed int64
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_activity_claims WHERE user_id=$1 AND activity_type=$2 AND ($2<>'daily_gift' OR activity_day=$3::date)`, uid, kind, day).Scan(&claimed); err != nil {
			return nil, err
		}
		s, err := dailyActivityState(kind, []service.DailyActivityRule{c.DailyGift, c.Recharge, c.Consumption}[i], []decimal.Decimal{daily, recharge, spent}[i], claimed)
		if err != nil {
			return nil, err
		}
		out.Activities = append(out.Activities, s)
	}
	return out, nil
}

func (r *dailyActivityRepository) Status(ctx context.Context, uid int64) (*service.DailyActivityStatus, error) {
	// 一个快照同时读取累计金额与领取次数，避免页面展示混合事务的临时次数。
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := readDailyActivityConfig(ctx, tx, "")
	if err != nil {
		return nil, err
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	out, err := dailyActivityStatus(ctx, tx, uid, c, now)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// Claim 配置共享锁与用户资金行锁保障多实例并发、幂等重试及余额入账在一个事务中提交。
func (r *dailyActivityRepository) Claim(ctx context.Context, uid int64, in service.DailyActivityClaimInput) (*service.DailyActivityClaimResult, error) {
	if err := service.ValidateDailyActivityClaim(in); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := readDailyActivityConfig(ctx, tx, " FOR SHARE")
	if err != nil {
		return nil, err
	}
	var balance decimal.Decimal
	if err = tx.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1 AND deleted_at IS NULL AND status='active' FOR NO KEY UPDATE`, uid).Scan(&balance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, infraerrors.Forbidden("DAILY_ACTIVITY_USER_UNAVAILABLE", "用户不可领取活动奖励")
		}
		return nil, err
	}
	var oldKind, raw string
	var oldAll bool
	err = tx.QueryRowContext(ctx, `SELECT activity_type,claim_all,result::text FROM daily_activity_claim_batches WHERE user_id=$1 AND idempotency_key=$2`, uid, in.IdempotencyKey).Scan(&oldKind, &oldAll, &raw)
	if err == nil {
		if oldKind != in.ActivityType || oldAll != in.All {
			return nil, service.ErrDailyActivityIdempotency
		}
		var out service.DailyActivityClaimResult
		if err = json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, err
		}
		out.Replayed = true
		return &out, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if !c.Enabled || c.StartedAt == nil {
		return nil, service.ErrDailyActivityDisabled
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	status, err := dailyActivityStatus(ctx, tx, uid, c, now)
	if err != nil {
		return nil, err
	}
	var state service.DailyActivityState
	for _, s := range status.Activities {
		if s.Type == in.ActivityType {
			state = s
			break
		}
	}
	// 已成功的幂等请求已在上方重放；关闭单项活动只拒绝新领取，不抹去累计资格。
	if !state.Enabled {
		return nil, service.ErrDailyActivityRuleDisabled
	}
	if state.AvailableCount < 1 {
		return nil, service.ErrDailyActivityUnavailable
	}
	count := int64(1)
	if in.All {
		count = state.AvailableCount
		if count > service.DailyActivityBatchLimit {
			count = service.DailyActivityBatchLimit
		}
	}
	var batchID int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO daily_activity_claim_batches(user_id,idempotency_key,activity_type,claim_all,result,created_at) VALUES($1,$2,$3,$4,'{}',$5) RETURNING id`, uid, in.IdempotencyKey, in.ActivityType, in.All, now).Scan(&batchID); err != nil {
		return nil, err
	}
	out := &service.DailyActivityClaimResult{Items: make([]service.DailyActivityRecord, 0, count), RemainingCount: state.AvailableCount - count}
	var totalCents int64
	for i := int64(0); i < count; i++ {
		cents, err := service.DailyActivityRewardCents(state.RewardMax)
		if err != nil {
			return nil, err
		}
		totalCents += cents
		record := service.DailyActivityRecord{ActivityType: in.ActivityType, Amount: float64(cents) / 100, CreatedAt: now}
		if err = tx.QueryRowContext(ctx, `INSERT INTO daily_activity_claims(batch_id,user_id,activity_type,activity_day,amount,threshold,reward_max,eligible_amount,created_at) VALUES($1,$2,$3,$4::date,$5,$6,$7,$8,$9) RETURNING id`, batchID, uid, in.ActivityType, status.Day, decimal.NewFromInt(cents).Div(decimal.NewFromInt(100)).String(), state.Threshold, state.RewardMax, state.EligibleAmount, now).Scan(&record.ID); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, record)
	}
	amount := decimal.NewFromInt(totalCents).Div(decimal.NewFromInt(100))
	// 奖励只增加可用余额，不增加累计充值本金，不递归触发充值奖励。
	if err = tx.QueryRowContext(ctx, `UPDATE users SET balance=balance+$1,updated_at=$2 WHERE id=$3 RETURNING balance`, amount.String(), now, uid).Scan(&balance); err != nil {
		return nil, err
	}
	out.Amount, _ = amount.Float64()
	out.Balance, _ = balance.Float64()
	data, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE daily_activity_claim_batches SET result=$1::jsonb WHERE id=$2`, string(data), batchID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *dailyActivityRepository) History(ctx context.Context, uid int64, kind string, page, size int) (*service.DailyActivityHistory, error) {
	if (kind != "" && !service.ValidDailyActivityType(kind)) || page < 1 || page > 1000000 || size < 1 || size > 100 {
		return nil, infraerrors.BadRequest("DAILY_ACTIVITY_INVALID_HISTORY", "领取记录查询参数无效")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := &service.DailyActivityHistory{Items: make([]service.DailyActivityRecord, 0), Page: page, PageSize: size}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_activity_claims WHERE user_id=$1 AND ($2='' OR activity_type=$2)`, uid, kind).Scan(&out.Total); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,activity_type,amount,created_at FROM daily_activity_claims WHERE user_id=$1 AND ($2='' OR activity_type=$2) ORDER BY id DESC LIMIT $3 OFFSET $4`, uid, kind, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item service.DailyActivityRecord
		if err = rows.Scan(&item.ID, &item.ActivityType, &item.Amount, &item.CreatedAt); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
