//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 退款状态只改变订单处理阶段，不能撤销已经到账的活动资格。
var dailyActivityRefundStates = []string{
	"REFUND_REQUESTED", "REFUNDING", "REFUND_PENDING", "PARTIALLY_REFUNDED", "REFUNDED", "REFUND_FAILED",
}

func addDailyActivityRefundOrder(t *testing.T, uid int64, kind, status, paymentType string, completed any) int64 {
	t.Helper()
	var id int64
	err := integrationDB.QueryRowContext(context.Background(), `INSERT INTO payment_orders
		(user_id,amount,pay_amount,bonus_amount,status,order_type,payment_type,completed_at,expires_at)
		VALUES($1,3,7,.5,$2,$3,$4,$5,NOW()+INTERVAL '1 day') RETURNING id`, uid, status, kind, paymentType, completed).Scan(&id)
	require.NoError(t, err)
	return id
}

func TestDailyActivityRefundLifecyclePreservesClaims(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		amount float64
		count  int64
	}{{"balance", 2.5, 25}, {"subscription", 7, 70}} {
		t.Run(tc.kind, func(t *testing.T) {
			repo, uid, _ := dailyActivityFixture(t)
			ctx := context.Background()
			id := addDailyActivityRefundOrder(t, uid, tc.kind, "COMPLETED", "alipay", dailyActivityDBNow(t))
			input := service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()}
			first, err := repo.Claim(ctx, uid, input)
			require.NoError(t, err)
			require.Len(t, first.Items, 1)
			giftInput := service.DailyActivityClaimInput{ActivityType: "daily_gift", IdempotencyKey: uuid.NewString()}
			gift, err := repo.Claim(ctx, uid, giftInput)
			require.NoError(t, err)

			for _, state := range dailyActivityRefundStates {
				t.Run(state, func(t *testing.T) {
					// 同时覆盖无确认时间和超出原订单金额的退款字段，累计不再依赖这两个字段。
					for _, refundAt := range []any{nil, dailyActivityDBNow(t)} {
						_, err := integrationDB.ExecContext(ctx, `UPDATE payment_orders SET status=$1,refund_amount=100,refund_at=$2 WHERE id=$3`, state, refundAt, id)
						require.NoError(t, err)
						status, err := repo.Status(ctx, uid)
						require.NoError(t, err)
						require.Equal(t, tc.amount, status.Activities[0].EligibleAmount)
						require.Zero(t, status.Activities[0].AvailableCount)
						require.Equal(t, tc.amount, status.Activities[1].EligibleAmount)
						require.Equal(t, int64(1), status.Activities[1].ClaimedCount)
						require.Equal(t, tc.count-1, status.Activities[1].AvailableCount)
						replay, err := repo.Claim(ctx, uid, input)
						require.NoError(t, err)
						require.True(t, replay.Replayed)
						require.Equal(t, first.Items, replay.Items)
					}
				})
			}

			// 全额退款后仍可领取未用机会，但旧请求和每日礼包均不能重复入账。
			_, err = integrationDB.ExecContext(ctx, `UPDATE payment_orders SET status='REFUNDED',refund_amount=amount,refund_at=clock_timestamp() WHERE id=$1`, id)
			require.NoError(t, err)
			all, err := repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: "recharge", All: true, IdempotencyKey: uuid.NewString()})
			require.NoError(t, err)
			require.Len(t, all.Items, int(tc.count-1))
			require.Zero(t, all.RemainingCount)
			replay, err := repo.Claim(ctx, uid, giftInput)
			require.NoError(t, err)
			require.True(t, replay.Replayed)
			require.Equal(t, gift.Items, replay.Items)
			for _, kind := range []string{"daily_gift", "recharge"} {
				_, err = repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: kind, IdempotencyKey: uuid.NewString()})
				require.ErrorIs(t, err, service.ErrDailyActivityUnavailable)
			}
			history, err := repo.History(ctx, uid, "", 1, 100)
			require.NoError(t, err)
			require.Equal(t, tc.count+1, history.Total)
			var balance float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
			require.InDelta(t, 5+float64(tc.count+1)*.1, balance, 1e-9)
		})
	}
}

func TestDailyActivityRefundNextDayDoesNotCreateNewGift(t *testing.T) {
	for _, kind := range []string{"balance", "subscription"} {
		t.Run(kind, func(t *testing.T) {
			_, uid, cfg := dailyActivityFixture(t)
			ctx := context.Background()
			completed := *cfg.StartedAt
			_, _, dayEnd := service.DailyActivityDay(completed)
			refundAt := dayEnd.Add(time.Hour)
			id := addDailyActivityRefundOrder(t, uid, kind, "REFUNDED", "alipay", completed)
			_, err := integrationDB.ExecContext(ctx, `UPDATE payment_orders SET refund_amount=amount,refund_at=$1 WHERE id=$2`, refundAt, id)
			require.NoError(t, err)
			want := 2.5
			if kind == "subscription" {
				want = 7
			}
			// 到账日仍保留一次礼包资格，次日退款不把这笔订单复制成新到账。
			before, err := dailyActivityStatus(ctx, integrationDB, uid, cfg, dayEnd.Add(-time.Nanosecond))
			require.NoError(t, err)
			require.Equal(t, want, before.Activities[0].EligibleAmount)
			require.Equal(t, int64(1), before.Activities[0].AvailableCount)
			after, err := dailyActivityStatus(ctx, integrationDB, uid, cfg, refundAt.Add(time.Second))
			require.NoError(t, err)
			require.Zero(t, after.Activities[0].EligibleAmount)
			require.Zero(t, after.Activities[0].AvailableCount)
			require.Equal(t, want, after.Activities[1].EligibleAmount)
			require.Equal(t, before.Activities[1].AvailableCount, after.Activities[1].AvailableCount)
		})
	}
}

func TestDailyActivityRefundNeverMakesExcludedOrdersEligible(t *testing.T) {
	repo, uid, cfg := dailyActivityFixture(t)
	ctx := context.Background()
	now := dailyActivityDBNow(t)
	for _, kind := range []string{"balance", "subscription"} {
		for _, state := range dailyActivityRefundStates {
			// 首次开启前到账、缺少到账时间、站内余额付款都不能借退款状态获得资格。
			addDailyActivityRefundOrder(t, uid, kind, state, "alipay", cfg.StartedAt.Add(-time.Second))
			addDailyActivityRefundOrder(t, uid, kind, state, "alipay", nil)
			addDailyActivityRefundOrder(t, uid, kind, state, "balance", now)
		}
	}
	_, err := integrationDB.ExecContext(ctx, `UPDATE payment_orders SET refund_amount=amount,refund_at=clock_timestamp() WHERE user_id=$1`, uid)
	require.NoError(t, err)
	status, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	for _, activity := range status.Activities {
		require.Zero(t, activity.EligibleAmount)
		require.Zero(t, activity.AvailableCount)
	}
	for _, kind := range []string{"daily_gift", "recharge"} {
		_, err = repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: kind, IdempotencyKey: uuid.NewString()})
		require.ErrorIs(t, err, service.ErrDailyActivityUnavailable)
	}
}

func TestDailyActivityRefundConcurrentTransitionsDoNotOvergrant(t *testing.T) {
	repo, uid, cfg := dailyActivityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg.Recharge.Threshold = 1
	_, err := repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	addDailyActivityRefundOrder(t, uid, "balance", "COMPLETED", "alipay", dailyActivityDBNow(t))
	addDailyActivityRefundOrder(t, uid, "subscription", "COMPLETED", "alipay", dailyActivityDBNow(t))
	start := make(chan struct{})
	errors := make(chan error, 33)
	results := make(chan *service.DailyActivityClaimResult, 32)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 24; i++ {
			_, err := integrationDB.ExecContext(ctx, `UPDATE payment_orders SET status=$1,refund_amount=amount,refund_at=clock_timestamp() WHERE user_id=$2`, dailyActivityRefundStates[i%len(dailyActivityRefundStates)], uid)
			if err != nil {
				errors <- err
				return
			}
		}
	}()
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()})
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	close(results)
	// 两笔原始到账共9.5，只能领9次；退款更新不应临时吞掉或重新释放机会。
	require.Len(t, results, 9)
	require.Len(t, errors, 23)
	for err := range errors {
		require.ErrorIs(t, err, service.ErrDailyActivityUnavailable)
	}
	status, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 9.5, status.Activities[1].EligibleAmount)
	require.Equal(t, int64(9), status.Activities[1].ClaimedCount)
	require.Zero(t, status.Activities[1].AvailableCount)
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.InDelta(t, 5.9, balance, 1e-9)
}
