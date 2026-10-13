package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/shopspring/decimal"
)

const dailyActivitySpendingSettingKey = service.DailyActivitySettingKey

// recordDailyActivitySpending 在实际扣费事务中保存活动消费事实，不能依赖可能丢弃或清理的使用日志。
// 首次开启后持续累计；临时关闭只影响领取，因此这里不根据 enabled 丢弃已结算消费。
// 调用方已按既有结算顺序锁定付款用户，事件与累计表不得另开事务或异步补写。
func recordDailyActivitySpending(ctx context.Context, tx *sql.Tx, userID, apiKeyID int64, requestID string, subscriptionAmount, balanceAmount float64) error {
	requestID = strings.TrimSpace(requestID)
	if userID <= 0 || apiKeyID <= 0 || requestID == "" ||
		math.IsNaN(subscriptionAmount) || math.IsInf(subscriptionAmount, 0) || subscriptionAmount < 0 ||
		math.IsNaN(balanceAmount) || math.IsInf(balanceAmount, 0) || balanceAmount < 0 {
		return nil
	}
	// 分别以十进制相加，保留订阅和余额各自的实际精度，不提前舍入到奖励使用的分。
	amount := decimal.NewFromFloat(subscriptionAmount).Add(decimal.NewFromFloat(balanceAmount)).Round(12)
	if !amount.IsPositive() {
		return nil
	}

	var raw string
	var settledAt time.Time
	err := tx.QueryRowContext(ctx, `SELECT value, clock_timestamp() FROM settings WHERE key = $1`, dailyActivitySpendingSettingKey).Scan(&raw, &settledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read daily activity spending start: %w", err)
	}
	var config struct {
		StartedAt string `json:"started_at"`
	}
	// 缺失或损坏的活动配置不能改变原有扣款；时间由数据库获取，不依赖进程所在时区。
	if json.Unmarshal([]byte(raw), &config) != nil || strings.TrimSpace(config.StartedAt) == "" {
		return nil
	}
	startedAt, err := time.Parse(time.RFC3339Nano, config.StartedAt)
	if err != nil || startedAt.IsZero() || settledAt.Before(startedAt) {
		return nil
	}

	// 与普通、图片和视频的资金幂等键保持相同作用域，重放和跨路径恢复不会重复累计。
	sourceKey := "usage:" + strconv.FormatInt(apiKeyID, 10) + ":" + requestID
	inserted, err := tx.ExecContext(ctx, `
		INSERT INTO daily_activity_spending_events (user_id, source_key, amount, created_at)
		VALUES ($1, $2, $3::numeric, $4)
		ON CONFLICT (source_key) DO NOTHING`, userID, sourceKey, amount.String(), settledAt)
	if err != nil {
		return fmt.Errorf("record daily activity spending: %w", err)
	}
	rows, err := inserted.RowsAffected()
	if err != nil {
		return fmt.Errorf("check daily activity spending insertion: %w", err)
	}
	if rows == 0 {
		return nil
	}
	if rows != 1 {
		return fmt.Errorf("unexpected daily activity spending insertion count: %d", rows)
	}
	// 只累计新事件并与资金原子提交，状态查询不再扫描首次开启以来的全部消费历史。
	_, err = tx.ExecContext(ctx, `
		INSERT INTO daily_activity_spending_totals (user_id, amount)
		VALUES ($1, $2::numeric)
		ON CONFLICT (user_id) DO UPDATE
		SET amount = daily_activity_spending_totals.amount + EXCLUDED.amount`, userID, amount.String())
	if err != nil {
		return fmt.Errorf("accumulate daily activity spending: %w", err)
	}
	return nil
}
