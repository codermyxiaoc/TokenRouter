//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 集成用量全部写入隔离数据库，金额与归属仅使用测试用户。
func insertSettlementPlatformQuota(t *testing.T, userID int64, platform string, limit *float64) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd,weekly_limit_usd,monthly_limit_usd) VALUES($1,$2,$3,$3,$3)`, userID, platform, limit)
	require.NoError(t, err)
}

func assertSettlementPlatformQuota(t *testing.T, userID int64, platform string, want float64) {
	t.Helper()
	var day, week, month float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform=$2`, userID, platform).Scan(&day, &week, &month))
	for _, actual := range []float64{day, week, month} {
		require.InDelta(t, want, actual, 1e-9)
	}
}

func settlementQuotaCommand(f *legacyVideoFixture, requestID string) service.UsageBillingCommand {
	return service.UsageBillingCommand{RequestID: requestID, UserID: f.user, ActorUserID: f.user,
		APIKeyID: f.key, AccountID: f.account, GroupID: &f.group, QuotaPlatform: service.PlatformGrok,
		APIKeyBillingMode: service.APIKeyBillingModeBalance, BaseAmountUSD: 0.25, BillableAmountUSD: 0.25,
		BalanceRateMultiplier: 1, APIKeyQuotaCost: 0.25, APIKeyRateLimitCost: 0.25,
		AccountType: service.AccountTypeAPIKey, AccountQuotaCost: 0.25}
}

// 多实例同时提交不同请求及同一请求，平台、余额和密钥统计应保持同一恰好一次提交边界。
func TestUsageBillingPlatformQuotaConcurrentExactlyOnce(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	insertSettlementPlatformQuota(t, f.user, service.PlatformGrok, float64Ptr(100))
	const unique, copies = 12, 3
	commands := make([]service.UsageBillingCommand, unique)
	type outcome struct {
		result *service.UsageBillingApplyResult
		err    error
	}
	outcomes := make(chan outcome, unique*copies)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range commands {
		commands[i] = settlementQuotaCommand(f, "quota-concurrent-"+uuid.NewString())
		for j := 0; j < copies; j++ {
			cmd := commands[i]
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				result, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
				outcomes <- outcome{result, err}
			}()
		}
	}
	close(start)
	wg.Wait()
	close(outcomes)
	applied := 0
	for out := range outcomes {
		require.NoError(t, out.err)
		require.NotNil(t, out.result)
		if out.result.Applied {
			applied++
		}
	}
	require.Equal(t, unique, applied)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 3)
	var balance, keyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user).Scan(&balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used FROM api_keys WHERE id=$1`, f.key).Scan(&keyUsage))
	require.InDelta(t, 97, balance, 1e-9)
	require.InDelta(t, 3, keyUsage, 1e-9)
	// 热去重迁入归档后重启仓储，不能补计已提交的平台消费。
	_, err := integrationDB.ExecContext(ctx, `WITH moved AS (DELETE FROM usage_billing_dedup WHERE api_key_id=$1 RETURNING request_id,api_key_id,request_fingerprint,created_at)
 INSERT INTO usage_billing_dedup_archive(request_id,api_key_id,request_fingerprint,created_at) SELECT request_id,api_key_id,request_fingerprint,created_at FROM moved`, f.key)
	require.NoError(t, err)
	restarted := NewUsageBillingRepository(integrationEntClient, integrationDB)
	for i := range commands {
		result, err := restarted.Apply(ctx, &commands[i])
		require.NoError(t, err)
		require.False(t, result.Applied)
	}
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 3)
	quotaRepo := NewUserPlatformQuotaRepository(integrationEntClient)
	for _, window := range []string{"daily", "weekly", "monthly"} {
		require.NoError(t, quotaRepo.ResetExpiredWindow(ctx, f.user, service.PlatformGrok, window, time.Now().UTC()))
	}
	for i := range commands {
		_, err := restarted.Apply(ctx, &commands[i])
		require.NoError(t, err)
	}
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0)
	newCommand := settlementQuotaCommand(f, "quota-after-reset-"+uuid.NewString())
	_, err = restarted.Apply(ctx, &newCommand)
	require.NoError(t, err)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.25)
}

// 资金分摊只把余额实扣计入平台；统计错误必须回滚同一事务内的全部资金、额度和去重。
func TestUsageBillingPlatformQuotaMixedFundsAndRollback(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	insertSettlementPlatformQuota(t, f.user, service.PlatformGrok, float64Ptr(100))
	plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{Name: "quota-mixed-" + uuid.NewString(),
		Price: 1, GroupIDs: []int64{f.group}, GroupRateMultipliers: map[int64]float64{f.group: 2}})
	sub := mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{UserID: f.user, PlanID: plan.ID,
		DailyLimitUSD: float64Ptr(0.5), WeeklyLimitUSD: float64Ptr(0.5), MonthlyLimitUSD: float64Ptr(0.5)})
	cmd := settlementQuotaCommand(f, "quota-mixed-"+uuid.NewString())
	cmd.APIKeyBillingMode, cmd.BaseAmountUSD, cmd.BalanceRateMultiplier = service.APIKeyBillingModeAuto, 1, 0.5
	cmd.SubscriptionRateMultiplier, cmd.SubscriptionRateMultiplierScale = 2, 1
	repo := NewUsageBillingRepository(integrationEntClient, integrationDB)
	result, err := repo.Apply(ctx, &cmd)
	require.NoError(t, err)
	require.InDelta(t, 0.5, result.SubscriptionAmountUSD, 1e-9)
	require.InDelta(t, 0.375, result.BalanceAmountUSD, 1e-9)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.375)
	var balance, keyUsage, accountUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user).Scan(&balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used FROM api_keys WHERE id=$1`, f.key).Scan(&keyUsage))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id=$1`, f.account).Scan(&accountUsage))
	bad := settlementQuotaCommand(f, "quota-rollback-"+uuid.NewString())
	bad.QuotaPlatform = "invalid-platform"
	_, err = repo.Apply(ctx, &bad)
	require.Error(t, err)
	var gotBalance, gotKey, gotAccount, subUsage float64
	var dedup int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user).Scan(&gotBalance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used FROM api_keys WHERE id=$1`, f.key).Scan(&gotKey))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id=$1`, f.account).Scan(&gotAccount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&subUsage))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, bad.RequestID, bad.APIKeyID).Scan(&dedup))
	require.Equal(t, balance, gotBalance)
	require.Equal(t, keyUsage, gotKey)
	require.Equal(t, accountUsage, gotAccount)
	require.InDelta(t, 0.5, subUsage, 1e-9)
	require.Zero(t, dedup)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.375)
}

// 自然日周按站点时区，月按完整 720 小时滚动；自然窗口变化不修改管理员重置代次。
func TestUsageBillingPlatformQuotaWindowRollover(t *testing.T) {
	f := newLegacyVideoFixture(t)
	previous := timezone.Location().String()
	require.NoError(t, timezone.Init("Asia/Shanghai"))
	t.Cleanup(func() { _ = timezone.Init(previous) })
	ctx := context.Background()
	insertSettlementPlatformQuota(t, f.user, service.PlatformGrok, float64Ptr(100))
	now := time.Date(2026, 10, 19, 0, 30, 0, 0, timezone.Location())
	_, err := integrationDB.ExecContext(ctx, `UPDATE user_platform_quotas SET daily_usage_usd=5,weekly_usage_usd=7,monthly_usage_usd=9,
 daily_window_start=$2,weekly_window_start=$3,monthly_window_start=$4,daily_reset_generation=3,weekly_reset_generation=4,monthly_reset_generation=5 WHERE user_id=$1`,
		f.user, timezone.StartOfDay(now.Add(-24*time.Hour)), timezone.StartOfWeek(now.Add(-7*24*time.Hour)), now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	applyAt := func(at time.Time, amount float64) {
		t.Helper()
		tx, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer tx.Rollback()
		require.NoError(t, incrementUsageBillingPlatformQuota(ctx, tx, f.user, service.PlatformGrok, amount, at))
		require.NoError(t, tx.Commit())
	}
	applyAt(now, 0.25)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.25)
	var dayStart, weekStart, monthStart time.Time
	var dg, wg, mg int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_window_start,weekly_window_start,monthly_window_start,daily_reset_generation,weekly_reset_generation,monthly_reset_generation FROM user_platform_quotas WHERE user_id=$1`, f.user).Scan(&dayStart, &weekStart, &monthStart, &dg, &wg, &mg))
	require.True(t, timezone.StartOfDay(now).Equal(dayStart))
	require.True(t, timezone.StartOfWeek(now).Equal(weekStart))
	require.True(t, now.Equal(monthStart))
	require.Equal(t, []int64{3, 4, 5}, []int64{dg, wg, mg})
	applyAt(now.Add(24*time.Hour), 0.5)
	var day, week, month float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_platform_quotas WHERE user_id=$1`, f.user).Scan(&day, &week, &month))
	require.InDelta(t, 0.5, day, 1e-9)
	require.InDelta(t, 0.75, week, 1e-9)
	require.InDelta(t, 0.75, month, 1e-9)
	applyAt(now.Add(30*24*time.Hour), 0.125)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.125)
}

// 没有额度配置、取消限额、免费或纯订阅均不增加平台用量；显式零限额仍记录已完成消费。
func TestUsageBillingPlatformQuotaLimitAndZeroSemantics(t *testing.T) {
	for _, scenario := range []string{"missing", "unlimited", "soft_deleted", "free", "zero_limit", "video_separate_ledger", "subscription_only"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLegacyVideoFixture(t)
			ctx := context.Background()
			platform := service.PlatformGrok
			if scenario == "video_separate_ledger" {
				platform = service.PlatformVideo
			}
			limit := float64Ptr(100)
			if scenario == "unlimited" {
				limit = nil
			} else if scenario == "zero_limit" {
				limit = float64Ptr(0)
			}
			if scenario != "missing" {
				insertSettlementPlatformQuota(t, f.user, platform, limit)
			}
			if scenario == "soft_deleted" {
				_, err := integrationDB.ExecContext(ctx, `UPDATE user_platform_quotas SET deleted_at=NOW() WHERE user_id=$1`, f.user)
				require.NoError(t, err)
			}
			cmd := settlementQuotaCommand(f, "quota-skip-"+uuid.NewString())
			cmd.QuotaPlatform = platform
			if scenario == "free" {
				cmd.BalanceRateMultiplier = 0
			}
			if scenario == "subscription_only" {
				plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{Name: "quota-sub-" + uuid.NewString(), Price: 1})
				mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{UserID: f.user, PlanID: plan.ID, DailyLimitUSD: float64Ptr(100)})
				cmd.APIKeyBillingMode, cmd.SubscriptionRateMultiplier = service.APIKeyBillingModeAuto, 1
			}
			_, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
			require.NoError(t, err)
			if scenario == "missing" {
				var count int
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_platform_quotas WHERE user_id=$1`, f.user).Scan(&count))
				require.Zero(t, count)
			} else {
				want := 0.0
				if scenario == "zero_limit" {
					want = 0.25
				}
				assertSettlementPlatformQuota(t, f.user, platform, want)
			}
		})
	}
}

// 恢复不因账号软删除漏记，也不能因原分组后来变更平台而把旧视频消费转移。
func TestUsageBillingPlatformQuotaLegacySnapshotAndDeletedAccount(t *testing.T) {
	for _, scenario := range []string{"grok_video", "seedance_video", "seedance_frozen_gemini"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLegacyVideoFixture(t)
			ctx := context.Background()
			source := scenario
			platform := service.PlatformGrok
			if source == "seedance_video" {
				platform = service.PlatformOpenAI
			}
			if scenario == "seedance_frozen_gemini" {
				source, platform = "seedance_video", service.PlatformGemini
			}
			insertSettlementPlatformQuota(t, f.user, platform, float64Ptr(100))
			task := f.task()
			task.Source = source
			if scenario == "seedance_frozen_gemini" {
				task.Pending.SeedanceBilling = &service.SeedanceBillingSnapshot{QuotaPlatform: service.PlatformGemini}
			}
			require.NoError(t, f.repo.Save(ctx, task))
			_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, f.account)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET platform='gemini',deleted_at=NOW() WHERE id=$1`, f.group)
			require.NoError(t, err)
			cmd := settlementQuotaCommand(f, task.RequestID)
			cmd.LegacyVideoTaskID, cmd.QuotaPlatform = task.ID, ""
			first, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
			require.NoError(t, err)
			require.True(t, first.Applied)
			assertSettlementPlatformQuota(t, f.user, platform, 0.25)
			second, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
			require.NoError(t, err)
			require.False(t, second.Applied)
			assertSettlementPlatformQuota(t, f.user, platform, 0.25)
		})
	}
}

// 无新平台字段的旧普通命令仍可从原软删除分组归属恢复，不能查当前 Key 绑定。
func TestUsageBillingPlatformQuotaOriginalGroupFallback(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	insertSettlementPlatformQuota(t, f.user, service.PlatformGrok, float64Ptr(100))
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET deleted_at=NOW() WHERE id=$1`, f.group)
	require.NoError(t, err)
	cmd := settlementQuotaCommand(f, fmt.Sprintf("quota-old-%d", f.user))
	cmd.QuotaPlatform = ""
	_, err = NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
	require.NoError(t, err)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.25)
}

// 图片冻结时保存原平台，修改 Key、分组或结算命令均不能转移已受理任务的用量。
func TestUsageBillingPlatformQuotaImageCaptureSnapshotAndReplay(t *testing.T) {
	for _, subscriptionLimit := range []float64{0, 0.50831488} {
		t.Run(fmt.Sprintf("subscription_%g", subscriptionLimit), func(t *testing.T) {
			ctx := context.Background()
			repo, reserve, _ := newImageBillingIntegrationFixture(t, 10, subscriptionLimit)
			reserve.Hold.QuotaPlatform = service.PlatformOpenAI
			insertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformOpenAI, float64Ptr(100))
			insertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformGemini, float64Ptr(100))
			hold, err := repo.ReserveImageBilling(ctx, reserve)
			require.NoError(t, err)
			assertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformOpenAI, 0)
			_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET platform='gemini',deleted_at=NOW() WHERE id=$1`, *reserve.Hold.GroupID)
			require.NoError(t, err)
			capture := imageBillingCaptureCommand(hold)
			capture.QuotaPlatform = service.PlatformGemini
			type outcome struct {
				result *service.UsageBillingApplyResult
				err    error
			}
			results := make(chan outcome, 10)
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					cmd := *capture
					result, err := (&usageBillingRepository{db: integrationDB}).CaptureImageBilling(ctx, hold.ID, &cmd, 0.12)
					results <- outcome{result, err}
				}()
			}
			wg.Wait()
			close(results)
			applied := 0
			for out := range results {
				require.NoError(t, out.err)
				if out.result.Applied {
					applied++
				}
			}
			require.Equal(t, 1, applied)
			want := 0.12
			if subscriptionLimit > 0 {
				want = 0.04298259
			}
			assertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformOpenAI, want)
			assertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformGemini, 0)
			// 失败释放的另一笔冻结不产生平台消费。
			reserve.Hold.RequestID = "quota-image-release-" + uuid.NewString()
			released, err := repo.ReserveImageBilling(ctx, reserve)
			require.NoError(t, err)
			require.NoError(t, repo.ReleaseImageBilling(ctx, released.ID, reserve.Hold.UserID, reserve.Hold.APIKeyID))
			assertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformOpenAI, want)
		})
	}
}

// 故意让平台 UPDATE 失败，必须保留冻结合同，不能已扣款却漏记统计或留下成功去重。
func TestUsageBillingPlatformQuotaImageFailureRollsBackCapture(t *testing.T) {
	ctx := context.Background()
	repo, reserve, _ := newImageBillingIntegrationFixture(t, 1, 0)
	reserve.Hold.QuotaPlatform = service.PlatformOpenAI
	insertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformOpenAI, float64Ptr(100))
	hold, err := repo.ReserveImageBilling(ctx, reserve)
	require.NoError(t, err)
	constraint := "quota_atomic_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE user_platform_quotas ADD CONSTRAINT %s CHECK(user_id<>%d OR daily_usage_usd=0)`, constraint, reserve.Hold.UserID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS "+constraint)
	})
	_, err = repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.12)
	require.Error(t, err)
	billingFocusAssertDecimal(t, "0.88", `SELECT balance::text FROM users WHERE id=$1`, reserve.Hold.UserID)
	billingFocusAssertDecimal(t, "0.12", `SELECT frozen_balance::text FROM users WHERE id=$1`, reserve.Hold.UserID)
	var state string
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM image_billing_reservations WHERE request_id=$1 AND api_key_id=$2`, hold.ID, reserve.Hold.APIKeyID).Scan(&state))
	require.Equal(t, "reserved", state)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, hold.ID, reserve.Hold.APIKeyID).Scan(&count))
	require.Zero(t, count)
	assertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformOpenAI, 0)
	_, err = integrationDB.ExecContext(ctx, "ALTER TABLE user_platform_quotas DROP CONSTRAINT "+constraint)
	require.NoError(t, err)
	result, err := repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.12)
	require.NoError(t, err)
	require.True(t, result.Applied)
	assertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformOpenAI, 0.12)
}

// 批量图片和创作台采用同一资金事务，预占不计、完成实扣才计，释放和重放都不重复。
func TestUsageBillingPlatformQuotaBatchAndCreativeCapture(t *testing.T) {
	for _, creative := range []bool{false, true} {
		t.Run(fmt.Sprintf("creative_%t", creative), func(t *testing.T) {
			f := newLegacyVideoFixture(t)
			ctx := context.Background()
			insertSettlementPlatformQuota(t, f.user, service.PlatformGemini, float64Ptr(100))
			repo := NewUsageBillingRepository(integrationEntClient, integrationDB)
			newHold := func() service.BatchImageBalanceHoldCommand {
				t.Helper()
				id := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				now := time.Now().UTC()
				requestID := service.BatchImageHoldRequestID(id)
				if creative {
					var err error
					id, err = service.NewCreativeRunID()
					require.NoError(t, err)
					_, err = (&creativeRunRepository{client: integrationEntClient}).CreateCreativeRun(ctx, service.CreateCreativeRunParams{
						RunID: id, UserID: f.user, GroupID: f.group, APIKeyID: f.key, Model: "quota-image", RequestedModel: "quota-image",
						Operation: service.CreativeOperationGenerate, RequestedOutputCount: 1, ImageSize: "1K", ResponseMIMEType: "image/png", PromptHash: "test", RequestFingerprint: id,
					})
					require.NoError(t, err)
					requestID = service.CreativeHoldRequestID(id)
				} else {
					insertBatchImageAllowanceTestJob(t, id, f.user, f.user, f.key, nil, now)
				}
				return service.BatchImageBalanceHoldCommand{RequestID: requestID, APIKeyID: f.key, UserID: f.user, ActorUserID: f.user,
					GroupID: &f.group, BatchID: id, HoldAmount: 0.8, ReservedAt: now, CreativeEntity: creative, QuotaPlatform: service.PlatformGemini}
			}
			hold := newHold()
			reserved, err := repo.ReserveBatchImageBalance(ctx, &hold)
			require.NoError(t, err)
			assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, 0)
			capture := hold
			capture.RequestID = service.BatchImageCaptureRequestID(hold.BatchID)
			if creative {
				capture.RequestID = service.CreativeCaptureRequestID(hold.BatchID)
			}
			capture.RequestFingerprint, capture.ActualAmount = "", 0.3
			capture.BalanceHoldAmount, capture.AllowanceReserved = reserved.BalanceAmountUSD, true
			result, err := repo.CaptureBatchImageBalance(ctx, &capture)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.InDelta(t, 0.3, result.BalanceAmountUSD, 1e-9)
			assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, 0.3)
			capture.AllowanceReserved, capture.RequestFingerprint = false, ""
			result, err = NewUsageBillingRepository(integrationEntClient, integrationDB).CaptureBatchImageBalance(ctx, &capture)
			require.NoError(t, err)
			require.False(t, result.Applied)
			assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, 0.3)
			release := newHold()
			reserved, err = repo.ReserveBatchImageBalance(ctx, &release)
			require.NoError(t, err)
			release.RequestID = service.BatchImageReleaseRequestID(release.BatchID)
			if creative {
				release.RequestID = service.CreativeReleaseRequestID(release.BatchID)
			}
			release.RequestFingerprint, release.BalanceHoldAmount, release.AllowanceReserved = "", reserved.BalanceAmountUSD, true
			_, err = repo.ReleaseBatchImageBalance(ctx, &release)
			require.NoError(t, err)
			assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, 0.3)
		})
	}
}

// 独立视频原账本在预留时已记额度，通用捕获钩子必须跳过以免再次累加。
func TestUsageBillingPlatformQuotaVideoCaptureNotDoubleCounted(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	task := f.task(0.8, 1, 1)
	task = f.reserve(task)
	f.quotaUsage(0.8, 0.8, 0.8)
	capture := f.command(task, "completed", 0.3)
	capture.QuotaPlatform = service.PlatformVideo
	result, err := f.repo.CaptureBatchImageBalance(context.Background(), &capture)
	require.NoError(t, err)
	require.True(t, result.Applied)
	f.quotaUsage(0.3, 0.3, 0.3)
	result, err = NewUsageBillingRepository(integrationEntClient, integrationDB).CaptureBatchImageBalance(context.Background(), &capture)
	require.NoError(t, err)
	require.False(t, result.Applied)
	f.quotaUsage(0.3, 0.3, 0.3)
}

// 已创建图片预占升级后增加统计元数据，不得破坏原金额指纹或覆盖原快照。
func TestUsageBillingPlatformQuotaImageUpgradeFingerprintCompatible(t *testing.T) {
	ctx := context.Background()
	repo, reserve, _ := newImageBillingIntegrationFixture(t, 1, 0)
	insertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformOpenAI, float64Ptr(100))
	hold, err := repo.ReserveImageBilling(ctx, reserve)
	require.NoError(t, err)
	reserve.Hold.QuotaPlatform = service.PlatformOpenAI
	duplicate, err := repo.ReserveImageBilling(ctx, reserve)
	require.NoError(t, err)
	require.False(t, duplicate.Applied)
	require.Empty(t, duplicate.Hold.QuotaPlatform)
	_, err = repo.CaptureImageBilling(ctx, hold.ID, imageBillingCaptureCommand(hold), 0.12)
	require.NoError(t, err)
	assertSettlementPlatformQuota(t, reserve.Hold.UserID, service.PlatformOpenAI, 0.12)
}

// 账本等待管理员行锁后必须重新取时钟，不能用进入事务时的旧时间覆盖刚重置的窗口。
func TestUsageBillingPlatformQuotaLockWaitUsesPostLockClock(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	insertSettlementPlatformQuota(t, f.user, service.PlatformGrok, float64Ptr(100))
	blocker, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer blocker.Rollback()
	var id int64
	require.NoError(t, blocker.QueryRowContext(ctx, `SELECT id FROM user_platform_quotas WHERE user_id=$1 FOR UPDATE`, f.user).Scan(&id))
	pidChannel := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		tx, err := integrationDB.BeginTx(ctx, nil)
		if err != nil {
			done <- err
			return
		}
		defer tx.Rollback()
		var pid int
		if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			done <- err
			return
		}
		pidChannel <- pid
		if err := incrementUsageBillingPlatformQuota(ctx, tx, f.user, service.PlatformGrok, 0.25, time.Time{}); err != nil {
			done <- err
			return
		}
		done <- tx.Commit()
	}()
	var pid int
	select {
	case pid = <-pidChannel:
	case err := <-done:
		require.NoError(t, err)
		return
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Eventually(t, func() bool {
		var waiting bool
		return integrationDB.QueryRowContext(ctx, `SELECT COALESCE(wait_event_type='Lock',FALSE) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting) == nil && waiting
	}, 5*time.Second, 10*time.Millisecond)
	var releaseTime time.Time
	require.NoError(t, blocker.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&releaseTime))
	require.NoError(t, blocker.Commit())
	require.NoError(t, <-done)
	var updatedAt time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT updated_at FROM user_platform_quotas WHERE user_id=$1`, f.user).Scan(&updatedAt))
	require.False(t, updatedAt.Before(releaseTime), "统计时钟必须在取得额度行锁之后读取")
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.25)
}

// 平台统计 SQL 失败时，混合资金、账号/Key、旧任务回执及去重记录全部回滚；恢复后只提交一次。
func TestUsageBillingPlatformQuotaLegacyReceiptRollback(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	insertSettlementPlatformQuota(t, f.user, service.PlatformGrok, float64Ptr(100))
	task := f.task()
	require.NoError(t, f.repo.Save(ctx, task))
	plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{Name: "quota-rollback-" + uuid.NewString(), Price: 1,
		GroupIDs: []int64{f.group}, GroupRateMultipliers: map[int64]float64{f.group: 2}})
	sub := mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{UserID: f.user, PlanID: plan.ID, DailyLimitUSD: float64Ptr(0.5)})
	cmd := settlementQuotaCommand(f, task.RequestID)
	cmd.LegacyVideoTaskID = task.ID
	cmd.APIKeyBillingMode, cmd.BaseAmountUSD, cmd.BalanceRateMultiplier = service.APIKeyBillingModeAuto, 1, 0.5
	cmd.SubscriptionRateMultiplier, cmd.SubscriptionRateMultiplierScale = 2, 1
	constraint := "quota_receipt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE user_platform_quotas ADD CONSTRAINT %s CHECK(user_id<>%d OR daily_usage_usd=0)`, constraint, f.user))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS "+constraint)
	})
	_, err = NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
	require.Error(t, err)
	billingFocusAssertDecimal(t, "100", `SELECT balance::text FROM users WHERE id=$1`, f.user)
	billingFocusAssertDecimal(t, "0", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, sub.ID)
	billingFocusAssertDecimal(t, "0", `SELECT quota_used::text FROM api_keys WHERE id=$1`, f.key)
	var receiptMissing bool
	var dedup int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT billing_result IS NULL FROM legacy_video_tasks WHERE id=$1`, task.ID).Scan(&receiptMissing))
	require.True(t, receiptMissing)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, cmd.APIKeyID).Scan(&dedup))
	require.Zero(t, dedup)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0)
	_, err = integrationDB.ExecContext(ctx, "ALTER TABLE user_platform_quotas DROP CONSTRAINT "+constraint)
	require.NoError(t, err)
	paid, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
	require.NoError(t, err)
	require.True(t, paid.Applied)
	require.InDelta(t, 0.375, paid.BalanceAmountUSD, 1e-9)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.375)
	// 不调用 finalize 或缓存通知，模拟提交完成后进程退出；新仓储重放从原回执恢复。
	replayed, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
	require.NoError(t, err)
	require.False(t, replayed.Applied)
	require.Equal(t, paid.BalanceAmountUSD, replayed.BalanceAmountUSD)
	require.Equal(t, paid.SubscriptionAmountUSD, replayed.SubscriptionAmountUSD)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.375)
}

// 团队 API Key 的行为用户和付款人不同，平台额度必须与真实付款余额归属一致。
func TestUsageBillingPlatformQuotaTeamPayerAttribution(t *testing.T) {
	f := newLegacyVideoFixture(t)
	ctx := context.Background()
	member := mustCreateUser(t, integrationEntClient, &service.User{Email: "quota-member-" + uuid.NewString() + "@example.test", Balance: 100})
	teams := NewTeamRepository(integrationDB)
	team, err := teams.Create(ctx, "平台额度测试团队", f.user, 5)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = teams.CreateInvitation(ctx, team.Team.ID, f.user, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teams.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	key := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: member.ID, TeamID: &team.Team.ID, GroupID: &f.group,
		Key: "sk-quota-member-" + uuid.NewString(), Name: "quota-member"})
	for _, id := range []int64{f.user, member.ID} {
		insertSettlementPlatformQuota(t, id, service.PlatformGrok, float64Ptr(100))
	}
	cmd := settlementQuotaCommand(f, "quota-team-"+uuid.NewString())
	cmd.APIKeyID, cmd.ActorUserID, cmd.TeamID = key.ID, member.ID, &team.Team.ID
	result, err := NewUsageBillingRepository(integrationEntClient, integrationDB).Apply(ctx, &cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	assertSettlementPlatformQuota(t, f.user, service.PlatformGrok, 0.25)
	assertSettlementPlatformQuota(t, member.ID, service.PlatformGrok, 0)
	billingFocusAssertDecimal(t, "99.75", `SELECT balance::text FROM users WHERE id=$1`, f.user)
	billingFocusAssertDecimal(t, "100", `SELECT balance::text FROM users WHERE id=$1`, member.ID)
	billingFocusAssertDecimal(t, "0.25", `SELECT daily_usage_usd::text FROM team_memberships WHERE team_id=$1 AND user_id=$2`, team.Team.ID, member.ID)
}
