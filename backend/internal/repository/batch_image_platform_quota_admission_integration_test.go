//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 两种入口使用真实任务行，测试共享预占事务而不调用外部生成服务。
func quotaAdmissionHold(t *testing.T, f *legacyVideoFixture, creative bool) service.BatchImageBalanceHoldCommand {
	t.Helper()
	id := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now().UTC()
	requestID := service.BatchImageHoldRequestID(id)
	if creative {
		var err error
		id, err = service.NewCreativeRunID()
		require.NoError(t, err)
		_, err = (&creativeRunRepository{client: integrationEntClient}).CreateCreativeRun(context.Background(), service.CreateCreativeRunParams{
			RunID: id, UserID: f.user, GroupID: f.group, APIKeyID: f.key, Model: "quota-image", RequestedModel: "quota-image",
			Operation: service.CreativeOperationGenerate, RequestedOutputCount: 1, ImageSize: "1K", ResponseMIMEType: "image/png",
			PromptHash: "test", RequestFingerprint: id, QuotaPlatform: service.PlatformGemini,
		})
		require.NoError(t, err)
		requestID = service.CreativeHoldRequestID(id)
	} else {
		insertBatchImageAllowanceTestJob(t, id, f.user, f.user, f.key, nil, now)
	}
	return service.BatchImageBalanceHoldCommand{RequestID: requestID, APIKeyID: f.key, UserID: f.user, ActorUserID: f.user,
		GroupID: &f.group, BatchID: id, HoldAmount: 1, ReservedAt: now, CreativeEntity: creative, QuotaPlatform: service.PlatformGemini,
		APIKeyBillingMode: service.APIKeyBillingModeBalance, PricingSnapshotVersion: 3, BaseAmountUSD: 1,
		SubscriptionRateMultiplier: 2, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 0.5, SettlementRateScale: 1}
}

// 余额不足的平台拒绝必须回滚已分配的订阅、钱包冻结、任务快照和去重，不能留下半次预占。
func TestBatchCreativePlatformQuotaAdmissionFundingAndRollback(t *testing.T) {
	for _, creative := range []bool{false, true} {
		for _, funding := range []string{"balance", "mixed", "subscription", "free"} {
			t.Run(fmt.Sprintf("creative_%t/%s", creative, funding), func(t *testing.T) {
				f := newLegacyVideoFixture(t)
				ctx := context.Background()
				insertSettlementPlatformQuota(t, f.user, service.PlatformGemini, float64Ptr(0))
				cmd := quotaAdmissionHold(t, f, creative)
				var subID int64
				if funding == "mixed" || funding == "subscription" {
					cmd.APIKeyBillingMode = service.APIKeyBillingModeAuto
					limit := 0.5
					if funding == "subscription" {
						limit = 10
					}
					plan := mustCreatePlan(t, integrationEntClient, &service.SubscriptionPlan{Name: "admission-" + uuid.NewString(),
						Price: 1, GroupIDs: []int64{f.group}, GroupRateMultipliers: map[int64]float64{f.group: 2}})
					sub := mustCreateSubscription(t, integrationEntClient, &service.UserSubscription{UserID: f.user, PlanID: plan.ID,
						DailyLimitUSD: &limit, WeeklyLimitUSD: &limit, MonthlyLimitUSD: &limit})
					subID = sub.ID
				}
				if funding == "free" {
					cmd.BalanceRateMultiplier = 0
				}
				result, err := NewUsageBillingRepository(integrationEntClient, integrationDB).ReserveBatchImageBalance(ctx, &cmd)
				var balance, frozen, keyUsage, subUsage, holdBalance float64
				var dedup int
				var reserved bool
				table, column := "batch_image_jobs", "batch_id"
				if creative {
					table, column = "creative_runs", "run_id"
				}
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, f.user).Scan(&balance, &frozen))
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used FROM api_keys WHERE id=$1`, f.key).Scan(&keyUsage))
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, f.key).Scan(&dedup))
				require.NoError(t, integrationDB.QueryRowContext(ctx, fmt.Sprintf(`SELECT balance_hold_amount,allowance_reserved FROM %s WHERE %s=$1`, table, column), cmd.BatchID).Scan(&holdBalance, &reserved))
				if subID > 0 {
					require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, subID).Scan(&subUsage))
				}
				if funding == "balance" || funding == "mixed" {
					require.ErrorIs(t, err, service.ErrUserPlatformDailyQuotaExhausted)
					require.NotEmpty(t, infraerrors.FromError(err).Metadata["window_resets_at"])
					require.Nil(t, result)
					require.Zero(t, dedup)
					require.Zero(t, keyUsage)
					require.Zero(t, subUsage)
					require.Zero(t, holdBalance)
					require.False(t, reserved)
				} else {
					require.NoError(t, err)
					require.True(t, result.Applied)
					require.Zero(t, result.BalanceAmountUSD)
					require.Equal(t, 1, dedup)
					if funding == "subscription" {
						require.InDelta(t, 2, subUsage, 1e-9)
					}
				}
				require.InDelta(t, 100, balance, 1e-9)
				require.Zero(t, frozen)
				assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, 0)
			})
		}
	}
}

// 准入使用站点窗口及原限额语义：只比较当前用量；捕获时即使额度已耗尽仍应按实扣入账。
func TestBatchCreativePlatformQuotaAdmissionDoesNotRejectAcceptedCapture(t *testing.T) {
	for _, creative := range []bool{false, true} {
		t.Run(fmt.Sprintf("creative_%t", creative), func(t *testing.T) {
			f := newLegacyVideoFixture(t)
			ctx := context.Background()
			insertSettlementPlatformQuota(t, f.user, service.PlatformGemini, float64Ptr(0.1))
			repo := NewUsageBillingRepository(integrationEntClient, integrationDB)
			hold := quotaAdmissionHold(t, f, creative)
			reserved, err := repo.ReserveBatchImageBalance(ctx, &hold)
			require.NoError(t, err)
			require.InDelta(t, 0.5, reserved.BalanceAmountUSD, 1e-9)
			assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, 0)
			_, err = integrationDB.ExecContext(ctx, `UPDATE user_platform_quotas SET daily_limit_usd=0 WHERE user_id=$1 AND platform=$2`, f.user, service.PlatformGemini)
			require.NoError(t, err)
			duplicate, err := repo.ReserveBatchImageBalance(ctx, &hold)
			require.NoError(t, err)
			require.False(t, duplicate.Applied)
			capture := quotaAdmissionCapture(hold, reserved, 1)
			result, err := repo.CaptureBatchImageBalance(ctx, &capture)
			require.NoError(t, err)
			require.True(t, result.Applied)
			assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, 0.5)
			result, err = NewUsageBillingRepository(integrationEntClient, integrationDB).CaptureBatchImageBalance(ctx, &capture)
			require.NoError(t, err)
			require.False(t, result.Applied)
			assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, 0.5)
			billingFocusAssertDecimal(t, "99.5", `SELECT balance::text FROM users WHERE id=$1`, f.user)
			billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, f.user)
		})
	}
}

func quotaAdmissionCapture(hold service.BatchImageBalanceHoldCommand, reserved *service.BatchImageBalanceHoldResult, actualBase float64) service.BatchImageBalanceHoldCommand {
	capture := hold
	capture.RequestID = service.BatchImageCaptureRequestID(hold.BatchID)
	if hold.CreativeEntity {
		capture.RequestID = service.CreativeCaptureRequestID(hold.BatchID)
	}
	capture.RequestFingerprint, capture.ActualBaseAmountUSD = "", actualBase
	capture.BalanceHoldAmount, capture.AllowanceReserved = reserved.BalanceAmountUSD, true
	for _, allocation := range reserved.BillingAllocations {
		if allocation.Type == domain.BillingAllocationTypeSubscription {
			capture.SubscriptionHoldAllocations = append(capture.SubscriptionHoldAllocations, allocation)
		}
	}
	return capture
}

// 各窗口耗尽保留对应错误与重置时间；自然过期只影响判断，不在预占时重写历史统计。
func TestBatchPlatformQuotaAdmissionWindowErrorsAndExpiry(t *testing.T) {
	for _, window := range []string{"daily", "weekly", "monthly"} {
		t.Run(window, func(t *testing.T) {
			f := newLegacyVideoFixture(t)
			ctx := context.Background()
			now := time.Now()
			start, reset := timezone.StartOfDay(now), timezone.StartOfDay(now).AddDate(0, 0, 1)
			wantErr := service.ErrUserPlatformDailyQuotaExhausted
			if window == "weekly" {
				start, reset = timezone.StartOfWeek(now), timezone.StartOfWeek(now).AddDate(0, 0, 7)
				wantErr = service.ErrUserPlatformWeeklyQuotaExhausted
			} else if window == "monthly" {
				start = now.Add(-10 * 24 * time.Hour).Truncate(time.Second)
				reset = start.Add(30 * 24 * time.Hour)
				wantErr = service.ErrUserPlatformMonthlyQuotaExhausted
			}
			_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`INSERT INTO user_platform_quotas(user_id,platform,%s_limit_usd,%s_usage_usd,%s_window_start) VALUES($1,$2,1,1,$3)`, window, window, window), f.user, service.PlatformGemini, start)
			require.NoError(t, err)
			hold := quotaAdmissionHold(t, f, false)
			repo := NewUsageBillingRepository(integrationEntClient, integrationDB)
			_, err = repo.ReserveBatchImageBalance(ctx, &hold)
			require.ErrorIs(t, err, wantErr)
			actualReset, parseErr := time.Parse(time.RFC3339, infraerrors.FromError(err).Metadata["window_resets_at"])
			require.NoError(t, parseErr)
			require.True(t, actualReset.Equal(reset), "reset got=%v want=%v", actualReset, reset)
			_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`UPDATE user_platform_quotas SET %s_window_start=$2 WHERE user_id=$1`, window), f.user, now.Add(-31*24*time.Hour))
			require.NoError(t, err)
			_, err = repo.ReserveBatchImageBalance(ctx, &hold)
			require.NoError(t, err)
			var usage float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s_usage_usd FROM user_platform_quotas WHERE user_id=$1`, window), f.user).Scan(&usage))
			require.Equal(t, 1.0, usage)
		})
	}
}

// 平台计数与真实八位钱包实扣一致，不能把未实扣的小数尾差累计进限额。
func TestBatchCreativePlatformQuotaCaptureWalletPrecision(t *testing.T) {
	for _, creative := range []bool{false, true} {
		for _, amount := range []float64{0.123456784, 0.123456786, 0.000000004} {
			t.Run(fmt.Sprintf("creative_%t/%.9f", creative, amount), func(t *testing.T) {
				f := newLegacyVideoFixture(t)
				ctx := context.Background()
				insertSettlementPlatformQuota(t, f.user, service.PlatformGemini, float64Ptr(100))
				hold := quotaAdmissionHold(t, f, creative)
				hold.BalanceRateMultiplier = 1
				repo := NewUsageBillingRepository(integrationEntClient, integrationDB)
				reserved, err := repo.ReserveBatchImageBalance(ctx, &hold)
				require.NoError(t, err)
				capture := quotaAdmissionCapture(hold, reserved, amount)
				result, err := repo.CaptureBatchImageBalance(ctx, &capture)
				require.NoError(t, err)
				require.True(t, result.Applied)
				expected := normalizeBatchImageBalanceAmount(amount)
				var spent, platform float64
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT 100-balance FROM users WHERE id=$1`, f.user).Scan(&spent))
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_platform_quotas WHERE user_id=$1`, f.user).Scan(&platform))
				require.InDelta(t, expected, spent, 1e-12)
				require.InDelta(t, spent, platform, 1e-12)
				assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, expected)
				_, err = repo.CaptureBatchImageBalance(ctx, &capture)
				require.NoError(t, err)
				assertSettlementPlatformQuota(t, f.user, service.PlatformGemini, expected)
			})
		}
	}
}
