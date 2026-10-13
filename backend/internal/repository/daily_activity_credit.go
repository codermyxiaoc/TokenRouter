package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// recordDailyActivityCredit 在权益发放事务内记录开启来源对应的正数到账额。
// 读取配置快照不加设置锁，避免与领取的“配置→用户”锁顺序形成反向死锁。
// @project-doc docs/domains/daily_activities.md#daily_activity_eligibility
func recordDailyActivityCredit(ctx context.Context, client *dbent.Client, usage *service.RedeemCodeUsage) error {
	rows, err := client.QueryContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.DailyActivitySettingKey)
	if err != nil {
		return fmt.Errorf("read daily activity credit configuration: %w", err)
	}
	var raw string
	if rows.Next() {
		err = rows.Scan(&raw)
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	var config struct {
		StartedAt            *time.Time `json:"started_at"`
		IncludeRedeemBalance bool       `json:"include_redeem_balance"`
		IncludeAdminBalance  bool       `json:"include_admin_balance"`
	}
	// 缺失或损坏的活动配置不阻断既有兑换；未首次启用不能产生历史资格。
	if json.Unmarshal([]byte(raw), &config) != nil || config.StartedAt == nil || config.StartedAt.IsZero() ||
		(!config.IncludeRedeemBalance && !config.IncludeAdminBalance) {
		return nil
	}
	// 在线充值也使用内部兑换码履约，关联任意支付状态都必须排除，由订单统一累计资格。
	// 唯一使用记录去重，保存值快照；活动关闭只停领取，已开启来源的实际到账仍按原规则累计。
	_, err = client.ExecContext(ctx, `INSERT INTO daily_activity_credit_events(user_id,source_usage_id,source_type,amount,created_at)
        SELECT $1,$2,CASE WHEN rc.type='balance' THEN 'redeem_balance' ELSE 'admin_balance' END,rc.value,clock_timestamp()
        FROM redeem_codes rc WHERE rc.id=$3 AND rc.value>0
        AND (($4::boolean AND rc.type='balance') OR ($5::boolean AND rc.type='admin_balance'))
        AND clock_timestamp() >= $6
        AND NOT EXISTS (SELECT 1 FROM payment_orders po WHERE po.recharge_code<>'' AND po.recharge_code=rc.code)
        ON CONFLICT (source_usage_id) DO NOTHING`, usage.UserID, usage.ID, usage.RedeemCodeID,
		config.IncludeRedeemBalance, config.IncludeAdminBalance, *config.StartedAt)
	if err != nil {
		return fmt.Errorf("record daily activity credit: %w", err)
	}
	return nil
}
