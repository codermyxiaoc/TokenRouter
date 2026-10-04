//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// newImageBillingIntegrationFixture 所有资金仅写入独立集成测试容器，测试不访问上游。
func newImageBillingIntegrationFixture(t *testing.T, balance, subscriptionLimit float64) (*usageBillingRepository, *service.ImageBillingReserveCommand, int64) {
	t.Helper()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "image-budget-" + uuid.NewString() + "@example.com", PasswordHash: "test", Balance: balance})
	group := mustCreateGroup(t, client, &service.Group{Name: "image-budget-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID,
		Key: "sk-image-budget-test-" + uuid.NewString(), Name: "image-budget", Quota: 100, RateLimit5h: 100})
	var subID int64
	if subscriptionLimit > 0 {
		plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "image-budget-" + uuid.NewString(), Price: 1,
			ValidityDays: 30, ValidityUnit: "day", GroupIDs: []int64{group.ID},
			GroupRateMultipliers: map[int64]float64{group.ID: 6.6}, DailyLimitUSD: float64Ptr(subscriptionLimit)})
		window := timezone.StartOfDay(time.Now())
		sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID,
			DailyLimitUSD: float64Ptr(subscriptionLimit), DailyWindowStart: &window})
		subID = sub.ID
	}
	cmd := &service.ImageBillingReserveCommand{Hold: service.BatchImageBalanceHoldCommand{
		RequestID: "client:image-" + uuid.NewString(), UserID: user.ID, ActorUserID: user.ID, APIKeyID: key.ID, GroupID: &group.ID,
		APIKeyBillingMode: service.APIKeyBillingModeAuto, BaseAmountUSD: 0.12,
		SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 1,
	}, Quote: json.RawMessage(`{"model":"image-budget","prices":{"1K":0.12},"count":1}`)}
	return &usageBillingRepository{db: integrationDB}, cmd, subID
}

func imageBillingCaptureCommand(hold *service.ImageBillingReservation) *service.UsageBillingCommand {
	return &service.UsageBillingCommand{RequestID: hold.ID, UserID: hold.Hold.UserID, ActorUserID: hold.Hold.ActorUserID,
		APIKeyID: hold.Hold.APIKeyID, GroupID: hold.Hold.GroupID, TeamID: hold.Hold.TeamID, Model: "image-budget", ImageCount: 1,
		APIKeyBillingMode: hold.Hold.APIKeyBillingMode, APIKeyQuotaCost: 1, APIKeyRateLimitCost: 1}
}

func TestImageBillingReservationMixedSourceSnapshotAndIdempotency(t *testing.T) {
	ctx := context.Background()
	repo, cmd, subID := newImageBillingIntegrationFixture(t, 1, 0.50831488)
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	require.True(t, hold.Applied)
	require.InDelta(t, 0.04298259, hold.Hold.BalanceHoldAmount, 1e-10)
	billingFocusAssertDecimal(t, "0.95701741", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0.04298259", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0.50831488", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, subID)
	duplicate, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	require.False(t, duplicate.Applied)
	changed := *cmd
	changed.Quote = json.RawMessage(`{"model":"other"}`)
	_, err = repo.ReserveImageBilling(ctx, &changed)
	require.ErrorIs(t, err, service.ErrImageBillingReservationConflict)

	// 预占后编辑套餐倍率，完成结算仍必须遵守已经保存的价格合同。
	_, err = integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET rate_multiplier=99 WHERE group_id=$1`, *cmd.Hold.GroupID)
	require.NoError(t, err)
	capture := imageBillingCaptureCommand(hold)
	result, err := repo.CaptureImageBilling(ctx, hold.ID, capture, 0.12)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 0.50831488, result.SubscriptionAmountUSD, 1e-10)
	require.InDelta(t, 0.04298259, result.BalanceAmountUSD, 1e-10)
	billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0.95701741", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0.55129747", `SELECT quota_used::text FROM api_keys WHERE id=$1`, cmd.Hold.APIKeyID)
	result, err = repo.CaptureImageBilling(ctx, hold.ID, capture, 0.12)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.ErrorIs(t, repo.ReleaseImageBilling(ctx, hold.ID, cmd.Hold.UserID, cmd.Hold.APIKeyID), service.ErrImageBillingReservationConflict)
	_, err = repo.CaptureImageBilling(ctx, hold.ID, capture, 0.06)
	require.Error(t, err)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, hold.ID, cmd.Hold.APIKeyID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestImageBillingReservationInsufficientFundsRollsBackSubscription(t *testing.T) {
	ctx := context.Background()
	repo, cmd, subID := newImageBillingIntegrationFixture(t, 0.01, 0.50831488)
	_, err := repo.ReserveImageBilling(ctx, cmd)
	require.ErrorIs(t, err, service.ErrBatchImageInsufficientBalance)
	billingFocusAssertDecimal(t, "0", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, subID)
	billingFocusAssertDecimal(t, "0.01", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_billing_reservations WHERE user_id=$1`, cmd.Hold.UserID).Scan(&count))
	require.Zero(t, count)
	cmd.Hold.APIKeyBillingMode = service.APIKeyBillingModeSubscription
	cmd.Hold.PreferredSubscriptionID = &subID
	_, err = repo.ReserveImageBilling(ctx, cmd)
	require.ErrorIs(t, err, service.ErrPreferredSubscriptionInsufficient)
	billingFocusAssertDecimal(t, "0", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, subID)
}

func TestImageBillingReservationFreeBalanceStillCountsPaidSubscriptionQuota(t *testing.T) {
	ctx := context.Background()
	repo, cmd, subID := newImageBillingIntegrationFixture(t, 0, 100)
	cmd.Hold.BalanceRateMultiplier = 0
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	require.InDelta(t, 0.792, hold.Hold.HoldAmount, 1e-10)
	require.Zero(t, hold.Hold.BalanceHoldAmount)
	// 余额定价免费时，已配置的 Key 限额仍必须统计实际由订阅消费的金额。
	result, err := repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.12)
	require.NoError(t, err)
	require.InDelta(t, 0.792, result.SubscriptionAmountUSD, 1e-10)
	require.Zero(t, result.BalanceAmountUSD)
	billingFocusAssertDecimal(t, "0.792", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, subID)
	billingFocusAssertDecimal(t, "0.792", `SELECT quota_used::text FROM api_keys WHERE id=$1`, cmd.Hold.APIKeyID)
	billingFocusAssertDecimal(t, "0.792", `SELECT usage_5h::text FROM api_keys WHERE id=$1`, cmd.Hold.APIKeyID)
	// 同一免费余额合同在订阅耗尽后允许零金额捕获，但不再虚增 Key 用量。
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_usage_usd=100 WHERE id=$1`, subID)
	require.NoError(t, err)
	cmd.Hold.RequestID = "client:image-" + uuid.NewString()
	hold, err = repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	result, err = repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.12)
	require.NoError(t, err)
	require.Zero(t, result.SubscriptionAmountUSD)
	require.Zero(t, result.BalanceAmountUSD)
	billingFocusAssertDecimal(t, "0.792", `SELECT quota_used::text FROM api_keys WHERE id=$1`, cmd.Hold.APIKeyID)
}

func TestImageBillingReservationReleaseAndExpiredRecovery(t *testing.T) {
	ctx := context.Background()
	repo, cmd, subID := newImageBillingIntegrationFixture(t, 1, 0.50831488)
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE image_billing_reservations SET expires_at=NOW()-INTERVAL '1 minute' WHERE request_id=$1 AND api_key_id=$2`, hold.ID, cmd.Hold.APIKeyID)
	require.NoError(t, err)
	count, err := repo.ReconcileExpiredImageBilling(ctx, time.Now(), 100)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	// 过期不确定任务保持冻结；收到明确失败后才允许释放。
	billingFocusAssertDecimal(t, "0.04298259", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	require.NoError(t, repo.ReleaseImageBilling(ctx, hold.ID, cmd.Hold.UserID, cmd.Hold.APIKeyID))
	require.NoError(t, repo.ReleaseImageBilling(ctx, hold.ID, cmd.Hold.UserID, cmd.Hold.APIKeyID))
	billingFocusAssertDecimal(t, "1", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, subID)
	_, err = repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.12)
	require.ErrorIs(t, err, service.ErrImageBillingReservationReleased)

	cmd.Hold.RequestID = "client:image-" + uuid.NewString()
	hold, err = repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	require.NoError(t, repo.MarkImageBillingReconciliation(ctx, hold.ID, cmd.Hold.UserID, cmd.Hold.APIKeyID))
	result, err := repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.12)
	require.NoError(t, err)
	require.True(t, result.Applied)
}

func TestImageBillingReservationCaptureOverrunAndPartialResult(t *testing.T) {
	ctx := context.Background()
	repo, cmd, _ := newImageBillingIntegrationFixture(t, 1, 0)
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	_, err = repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.24)
	require.ErrorIs(t, err, service.ErrBatchImageSettlementCostExceedsHold)
	billingFocusAssertDecimal(t, "0.12", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	result, err := repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.06)
	require.NoError(t, err)
	require.InDelta(t, 0.06, result.BalanceAmountUSD, 1e-10)
	billingFocusAssertDecimal(t, "0.94", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0.06", `SELECT quota_used::text FROM api_keys WHERE id=$1`, cmd.Hold.APIKeyID)
}

func TestImageBillingReservationCaptureEffectsFailureRollsBackAndCanRecover(t *testing.T) {
	ctx := context.Background()
	repo, cmd, _ := newImageBillingIntegrationFixture(t, 1, 0)
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	// 模拟异步运行中 Key 被删除，使资金捕获后的配额更新失败；整个事务必须回滚。
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET deleted_at=NOW() WHERE id=$1`, cmd.Hold.APIKeyID)
	require.NoError(t, err)
	_, err = repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.06)
	require.ErrorIs(t, err, service.ErrAPIKeyNotFound)
	billingFocusAssertDecimal(t, "0.88", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0.12", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	var state string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM image_billing_reservations WHERE request_id=$1 AND api_key_id=$2`, hold.ID, cmd.Hold.APIKeyID).Scan(&state))
	require.Equal(t, service.ImageBillingReserved, state)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, hold.ID, cmd.Hold.APIKeyID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, repo.MarkImageBillingReconciliation(ctx, hold.ID, cmd.Hold.UserID, cmd.Hold.APIKeyID))
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET deleted_at=NULL WHERE id=$1`, cmd.Hold.APIKeyID)
	require.NoError(t, err)
	result, err := repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.06)
	require.NoError(t, err)
	require.True(t, result.Applied)
	billingFocusAssertDecimal(t, "0.94", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	billingFocusAssertDecimal(t, "0.06", `SELECT quota_used::text FROM api_keys WHERE id=$1`, cmd.Hold.APIKeyID)
}

func TestImageBillingReservation220ConcurrentFundsCannotBeReused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repo, template, subID := newImageBillingIntegrationFixture(t, 1.2, 1.584)
	previous := integrationDB.Stats().MaxOpenConnections
	integrationDB.SetMaxOpenConns(32)
	t.Cleanup(func() { integrationDB.SetMaxOpenConns(previous) })
	const total = 220
	results := make([]*service.ImageBillingReservation, total)
	errorsByRequest := make([]error, total)
	start := make(chan struct{})
	var done sync.WaitGroup
	for i := range total {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			cmd := *template
			cmd.Hold.RequestID = fmt.Sprintf("%s:%d", template.Hold.RequestID, i)
			<-start
			results[i], errorsByRequest[i] = repo.ReserveImageBilling(ctx, &cmd)
		}(i)
	}
	close(start)
	done.Wait()
	success := 0
	for i, err := range errorsByRequest {
		if err == nil {
			success++
			require.True(t, results[i].Applied)
		} else {
			require.True(t, errors.Is(err, service.ErrBatchImageInsufficientBalance), "unexpected reserve error: %v", err)
		}
	}
	require.Equal(t, 12, success, "2 张订阅 + 10 张余额，其他 208 次不得透支")
	billingFocusAssertDecimal(t, "0", `SELECT balance::text FROM users WHERE id=$1`, template.Hold.UserID)
	billingFocusAssertDecimal(t, "1.2", `SELECT frozen_balance::text FROM users WHERE id=$1`, template.Hold.UserID)
	billingFocusAssertDecimal(t, "1.584", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, subID)
	// 重复捕获同时到达只能生成一笔结算，随后释放其他预占不会动到已捕获金额。
	var selected *service.ImageBillingReservation
	for _, result := range results {
		if result != nil {
			selected = result
			break
		}
	}
	applied := make([]bool, 32)
	captureErrors := make([]error, 32)
	for i := range 32 {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			result, err := repo.CaptureImageBilling(ctx, selected.ID, imageBillingCaptureCommand(selected), 0.12)
			captureErrors[i] = err
			if result != nil {
				applied[i] = result.Applied
			}
		}(i)
	}
	done.Wait()
	applyCount := 0
	for i, err := range captureErrors {
		require.NoError(t, err)
		if applied[i] {
			applyCount++
		}
	}
	require.Equal(t, 1, applyCount)
	for _, hold := range results {
		if hold != nil && hold.ID != selected.ID {
			require.NoError(t, repo.ReleaseImageBilling(ctx, hold.ID, template.Hold.UserID, template.Hold.APIKeyID))
		}
	}
	billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, template.Hold.UserID)
}
