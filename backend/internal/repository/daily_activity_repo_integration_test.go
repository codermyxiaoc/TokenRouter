//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 测试只操作容器数据库，恢复共享配置且按测试用户清理活动与充值记录。
func dailyActivityFixture(t *testing.T) (service.DailyActivityRepository, int64, *service.DailyActivityConfig) {
	t.Helper()
	ctx := context.Background()
	var old string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.DailyActivitySettingKey).Scan(&old))
	initial, _ := json.Marshal(service.DefaultDailyActivityConfig())
	_, err := integrationDB.ExecContext(ctx, `UPDATE settings SET value=$1 WHERE key=$2`, string(initial), service.DailyActivitySettingKey)
	require.NoError(t, err)
	u := mustCreateUser(t, integrationEntClient, &service.User{Balance: 5})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM payment_orders WHERE user_id=$1`, u.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, u.ID)
		_, _ = integrationDB.ExecContext(ctx, `UPDATE settings SET value=$1 WHERE key=$2`, old, service.DailyActivitySettingKey)
	})
	repo := NewDailyActivityRepository(integrationDB)
	cfg := service.DefaultDailyActivityConfig()
	cfg.Enabled = true
	cfg.Recharge = service.DailyActivityRule{Threshold: .1, RewardMax: .1}
	cfg.Consumption = cfg.Recharge
	cfg.DailyGift = cfg.Recharge
	saved, err := repo.SaveConfig(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, saved.StartedAt)
	return repo, u.ID, saved
}

func addDailyActivityOrder(t *testing.T, uid int64, amount, bonus, refund float64, status, kind, paymentType string, at time.Time) {
	t.Helper()
	var refundAt any
	if refund > 0 && status == "PARTIALLY_REFUNDED" {
		refundAt = at
	}
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO payment_orders(user_id,amount,pay_amount,bonus_amount,refund_amount,refund_at,status,order_type,payment_type,completed_at,expires_at) VALUES($1,$2,999,$3,$4,$5,$6,$7,$8,$9,NOW()+INTERVAL '1 day')`, uid, amount, bonus, refund, refundAt, status, kind, paymentType, at)
	require.NoError(t, err)
}

// 完成时间与被测SQL使用同一时钟，避免Windows宿主与容器时钟微小偏差造成假阴性。
func dailyActivityDBNow(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT clock_timestamp()`).Scan(&now))
	return now
}

func TestDailyActivityPostgresConcurrentClaimsAndReplay(t *testing.T) {
	repo, uid, _ := dailyActivityFixture(t)
	ctx := context.Background()
	addDailyActivityOrder(t, uid, .3, 0, 0, "COMPLETED", "balance", "alipay", dailyActivityDBNow(t))
	var wg sync.WaitGroup
	results := make(chan *service.DailyActivityClaimResult, 20)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()})
			if err != nil {
				errs <- err
			} else {
				results <- out
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	require.Len(t, results, 3)
	require.Len(t, errs, 17)
	for err := range errs {
		require.ErrorIs(t, err, service.ErrDailyActivityUnavailable)
	}
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.InDelta(t, 5.3, balance, 1e-9)
	key := uuid.NewString()
	input := service.DailyActivityClaimInput{ActivityType: "daily_gift", IdempotencyKey: key}
	first, err := repo.Claim(ctx, uid, input)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		replay, err := repo.Claim(ctx, uid, input)
		require.NoError(t, err)
		require.True(t, replay.Replayed)
		require.Equal(t, first.Items, replay.Items)
		require.Equal(t, first.Balance, replay.Balance)
	}
	// 奖励余额不会重新增加充值资格，配置门槛修改也不删除已经领取的次数。
	state, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, .3, state.Activities[1].EligibleAmount)
	require.Equal(t, int64(3), state.Activities[1].ClaimedCount)
	config, err := repo.GetConfig(ctx)
	require.NoError(t, err)
	config.Recharge.Threshold = .2
	_, err = repo.SaveConfig(ctx, *config)
	require.NoError(t, err)
	state, err = repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(3), state.Activities[1].ClaimedCount)
	require.Zero(t, state.Activities[1].AvailableCount)
	// 暂停领取不阻止查询已经成功的幂等结果。
	config.Enabled = false
	_, err = repo.SaveConfig(ctx, *config)
	require.NoError(t, err)
	replay, err := repo.Claim(ctx, uid, input)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	input.All = true
	_, err = repo.Claim(ctx, uid, input)
	require.ErrorIs(t, err, service.ErrDailyActivityIdempotency)
	history, err := repo.History(ctx, uid, "", 2, 2)
	require.NoError(t, err)
	require.Equal(t, int64(4), history.Total)
	require.Len(t, history.Items, 2)
	other := mustCreateUser(t, integrationEntClient, &service.User{})
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, other.ID) })
	history, err = repo.History(ctx, other.ID, "", 1, 20)
	require.NoError(t, err)
	require.Zero(t, history.Total)
}

func TestDailyActivityPostgresBaselineBonusRefundAndPersistentProgress(t *testing.T) {
	repo, uid, cfg := dailyActivityFixture(t)
	ctx := context.Background()
	now := dailyActivityDBNow(t)
	addDailyActivityOrder(t, uid, 100, 0, 0, "COMPLETED", "balance", "alipay", cfg.StartedAt.Add(-time.Second))
	addDailyActivityOrder(t, uid, 10, 2, 0, "COMPLETED", "balance", "alipay", now)
	addDailyActivityOrder(t, uid, 10, 2, 5, "PARTIALLY_REFUNDED", "balance", "alipay", now)
	addDailyActivityOrder(t, uid, 1, 0, 1, "REFUND_FAILED", "balance", "alipay", now)
	for _, status := range []string{"PENDING", "FAILED", "REFUND_PENDING", "REFUND_REQUESTED", "REFUNDED"} {
		addDailyActivityOrder(t, uid, 100, 0, 0, status, "balance", "alipay", now)
	}
	addDailyActivityOrder(t, uid, 100, 0, 0, "COMPLETED", "subscription", "alipay", now)
	addDailyActivityOrder(t, uid, 100, 0, 0, "COMPLETED", "balance", "balance", now)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO daily_activity_spending_events(user_id,source_key,amount,created_at) VALUES($1,$2,.3,$3),($1,$4,100,$5)`, uid, uuid.NewString(), now, uuid.NewString(), cfg.StartedAt.Add(-time.Second))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO daily_activity_spending_totals(user_id,amount) VALUES($1,.3)`, uid)
	require.NoError(t, err)
	s, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 13.0, s.Activities[1].EligibleAmount)
	require.Equal(t, int64(3), s.Activities[2].AvailableCount)
	// 人为推进查询时间到次日，仅礼包归零；累计活动金额和未领次数仍然保留。
	next, err := dailyActivityStatus(ctx, integrationDB, uid, cfg, now.Add(48*time.Hour))
	require.NoError(t, err)
	require.Zero(t, next.Activities[0].AvailableCount)
	require.Equal(t, int64(130), next.Activities[1].AvailableCount)
	require.Equal(t, int64(3), next.Activities[2].AvailableCount)
	forged := cfg.StartedAt.Add(-365 * 24 * time.Hour)
	cfg.StartedAt = &forged
	cfg.Enabled = false
	saved, err := repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	require.NotEqual(t, forged, *saved.StartedAt)
	_, err = repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()})
	require.ErrorIs(t, err, service.ErrDailyActivityDisabled)
	saved.Enabled = true
	again, err := repo.SaveConfig(ctx, *saved)
	require.NoError(t, err)
	require.Equal(t, saved.StartedAt, again.StartedAt)
	result, err := repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: "recharge", All: true, IdempotencyKey: uuid.NewString()})
	require.NoError(t, err)
	require.Len(t, result.Items, 100)
	require.Equal(t, int64(30), result.RemainingCount)
	require.Equal(t, 10.0, result.Amount)
	var threshold, maximum, eligible float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT threshold,reward_max,eligible_amount FROM daily_activity_claims WHERE id=$1`, result.Items[0].ID).Scan(&threshold, &maximum, &eligible))
	require.Equal(t, .1, threshold)
	require.Equal(t, .1, maximum)
	require.Equal(t, 13.0, eligible)
}

func TestDailyActivityPostgresClaimFailureRollsBack(t *testing.T) {
	repo, uid, _ := dailyActivityFixture(t)
	ctx := context.Background()
	addDailyActivityOrder(t, uid, 1, 0, 0, "COMPLETED", "balance", "alipay", dailyActivityDBNow(t))
	// 利用测试用户最大余额造成入账溢出，验证前面写入的批次和奖励明细同时回滚。
	_, err := integrationDB.ExecContext(ctx, `UPDATE users SET balance=999999999999.99999999 WHERE id=$1`, uid)
	require.NoError(t, err)
	_, err = repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()})
	require.Error(t, err)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_activity_claims WHERE user_id=$1`, uid).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_activity_claim_batches WHERE user_id=$1`, uid).Scan(&count))
	require.Zero(t, count)
}

func TestDailyActivityPostgresConcurrentSameKeyOnlyOneCredit(t *testing.T) {
	repo, uid, _ := dailyActivityFixture(t)
	ctx := context.Background()
	addDailyActivityOrder(t, uid, 1, 0, 0, "COMPLETED", "balance", "alipay", dailyActivityDBNow(t))
	input := service.DailyActivityClaimInput{ActivityType: "recharge", All: true, IdempotencyKey: uuid.NewString()}
	var wg sync.WaitGroup
	results := make(chan *service.DailyActivityClaimResult, 20)
	errs := make(chan error, 20)
	// 模拟同一领取在网关重试和多个标签页内并发重放，不能消耗下一批机会。
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := repo.Claim(ctx, uid, input)
			if err != nil {
				errs <- err
			} else {
				results <- out
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, results, 20)
	var first *service.DailyActivityClaimResult
	nonReplay := 0
	for out := range results {
		if !out.Replayed {
			nonReplay++
		}
		if first == nil {
			first = out
		} else {
			require.Equal(t, first.Items, out.Items)
			require.Equal(t, first.Amount, out.Amount)
			require.Equal(t, first.Balance, out.Balance)
			require.Equal(t, first.RemainingCount, out.RemainingCount)
		}
	}
	require.Equal(t, 1, nonReplay)
	require.Len(t, first.Items, 10)
	var balance float64
	var batches, claims int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 6.0, balance)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_activity_claim_batches WHERE user_id=$1`, uid).Scan(&batches))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_activity_claims WHERE user_id=$1`, uid).Scan(&claims))
	require.Equal(t, 1, batches)
	require.Equal(t, 10, claims)
}
