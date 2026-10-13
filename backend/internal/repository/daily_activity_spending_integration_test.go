//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 设置仅写入测试容器，首次启用时间在过去，暂停领取不暂停消费累计。
func configureDailyActivitySpendingTest(t *testing.T, enabled bool) {
	t.Helper()
	raw := fmt.Sprintf(`{"enabled":%t,"started_at":"2000-01-01T00:00:00Z"}`, enabled)
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO settings (key,value,updated_at) VALUES ($1,$2,NOW()) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`, dailyActivitySpendingSettingKey, raw)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM settings WHERE key=$1`, dailyActivitySpendingSettingKey)
		require.NoError(t, err)
	})
}

func assertDailyActivitySpending(t *testing.T, userID int64, count int, amount float64) {
	t.Helper()
	var actualCount int
	var actualAmount float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*),COALESCE(SUM(amount),0) FROM daily_activity_spending_events WHERE user_id=$1`, userID).Scan(&actualCount, &actualAmount))
	require.Equal(t, count, actualCount)
	require.InDelta(t, amount, actualAmount, 1e-10)
	var totalAmount float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COALESCE((SELECT amount FROM daily_activity_spending_totals WHERE user_id=$1),0)`, userID).Scan(&totalAmount))
	require.InDelta(t, amount, totalAmount, 1e-10)
}

// 同一资金请求高并发重放只产生一次消费，资格属于付款用户而不是调用成员。
func TestDailyActivitySpendingOrdinaryConcurrentReplayAndPause(t *testing.T) {
	client := testEntClient(t)
	configureDailyActivitySpendingTest(t, true)
	owner := mustCreateUser(t, client, &service.User{Email: "activity-owner-" + uuid.NewString() + "@example.com", Balance: 10})
	actor := mustCreateUser(t, client, &service.User{Email: "activity-actor-" + uuid.NewString() + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: owner.ID, Key: "sk-activity-" + uuid.NewString(), Name: "activity"})
	repo := NewUsageBillingRepository(client, integrationDB)
	command := service.UsageBillingCommand{RequestID: "activity:" + uuid.NewString(), APIKeyID: key.ID, UserID: owner.ID, ActorUserID: actor.ID, BillableAmountUSD: 0.72, APIKeyBillingMode: service.APIKeyBillingModeBalance}
	const concurrency = 24
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := command
			_, err := repo.Apply(context.Background(), &copy)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assertDailyActivitySpending(t, owner.ID, 1, .72)
	assertDailyActivitySpending(t, actor.ID, 0, 0)

	configureDailyActivitySpendingTest(t, false)
	command.RequestID = "activity:" + uuid.NewString()
	command.BillableAmountUSD = .12
	_, err := repo.Apply(context.Background(), &command)
	require.NoError(t, err)
	assertDailyActivitySpending(t, owner.ID, 2, .84)
	// 免费调用没有实际扣费，不得仅因产生一条请求记录而增加资格。
	command.RequestID = "activity:" + uuid.NewString()
	command.BillableAmountUSD = 0
	command.RequestFingerprint = ""
	_, err = repo.Apply(context.Background(), &command)
	require.NoError(t, err)
	assertDailyActivitySpending(t, owner.ID, 2, .84)
}

// 图片只在捕获时累计最终混合扣费，预占和失败释放不计消费。
func TestDailyActivitySpendingImageCaptureAndRelease(t *testing.T) {
	repo, command, _ := newImageBillingIntegrationFixture(t, 1, .50831488)
	configureDailyActivitySpendingTest(t, true)
	hold, err := repo.ReserveImageBilling(context.Background(), command)
	require.NoError(t, err)
	assertDailyActivitySpending(t, command.Hold.UserID, 0, 0)
	capture := imageBillingCaptureCommand(hold)
	result, err := repo.CaptureImageBilling(context.Background(), hold.ID, capture, .12)
	require.NoError(t, err)
	require.InDelta(t, .55129747, result.SubscriptionAmountUSD+result.BalanceAmountUSD, 1e-10)
	assertDailyActivitySpending(t, command.Hold.UserID, 1, .55129747)
	replayed, err := repo.CaptureImageBilling(context.Background(), hold.ID, capture, .12)
	require.NoError(t, err)
	require.False(t, replayed.Applied)
	assertDailyActivitySpending(t, command.Hold.UserID, 1, .55129747)

	command.Hold.RequestID = "activity-failed-image:" + uuid.NewString()
	failed, err := repo.ReserveImageBilling(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, repo.ReleaseImageBilling(context.Background(), failed.ID, command.Hold.UserID, command.Hold.APIKeyID))
	assertDailyActivitySpending(t, command.Hold.UserID, 1, .55129747)
}

// Video 的按秒预扣最终按 Token 退补，只记录真实消费，不把预扣或退差计成额外事件。
func TestDailyActivitySpendingVideoTokenPrepay(t *testing.T) {
	f := newVideoBillingFixture(t)
	configureDailyActivitySpendingTest(t, true)
	task := f.reserve(tokenPrepayTestTask(t, f, 2, .3))
	assertDailyActivitySpending(t, f.userID, 0, 0)
	command := f.command(task, "completed", 3)
	command.VideoActualFixedAmountUSD = .3
	result, err := f.repo.CaptureBatchImageBalance(context.Background(), &command)
	require.NoError(t, err)
	require.InDelta(t, 2.7, result.HoldAmountUSD, 1e-10)
	require.InDelta(t, 6.3, result.ActualAmountUSD, 1e-10)
	assertDailyActivitySpending(t, f.userID, 1, 6.3)
	replayed, err := f.repo.CaptureBatchImageBalance(context.Background(), &command)
	require.NoError(t, err)
	require.False(t, replayed.Applied)
	assertDailyActivitySpending(t, f.userID, 1, 6.3)

	failed := f.reserve(f.task(2, 1, 1))
	release := f.command(failed, "failed", 0)
	_, err = f.repo.ReleaseBatchImageBalance(context.Background(), &release)
	require.NoError(t, err)
	assertDailyActivitySpending(t, f.userID, 1, 6.3)
}

// 批量图片和创作台共用同一捕获入口；普通批量任务也须按实际费用而非预估金额累计。
func TestDailyActivitySpendingBatchCapture(t *testing.T) {
	client := testEntClient(t)
	configureDailyActivitySpendingTest(t, true)
	user := mustCreateUser(t, client, &service.User{Email: "activity-batch-" + uuid.NewString() + "@example.com", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-activity-" + uuid.NewString(), Name: "activity"})
	batchID := "activity-batch-" + uuid.NewString()
	reservedAt := time.Now().UTC()
	insertBatchImageAllowanceTestJob(t, batchID, user.ID, user.ID, key.ID, nil, reservedAt)
	repo := NewUsageBillingRepository(client, integrationDB)
	command := service.BatchImageBalanceHoldCommand{RequestID: service.BatchImageHoldRequestID(batchID), BatchID: batchID, UserID: user.ID, ActorUserID: user.ID, APIKeyID: key.ID, HoldAmount: .8, ReservedAt: reservedAt}
	reserved, err := repo.ReserveBatchImageBalance(context.Background(), &command)
	require.NoError(t, err)
	assertDailyActivitySpending(t, user.ID, 0, 0)
	command.RequestID = service.BatchImageCaptureRequestID(batchID)
	command.RequestFingerprint = ""
	command.BalanceHoldAmount, command.AllowanceReserved, command.ActualAmount = reserved.BalanceAmountUSD, true, .3
	_, err = repo.CaptureBatchImageBalance(context.Background(), &command)
	require.NoError(t, err)
	assertDailyActivitySpending(t, user.ID, 1, .3)
}
