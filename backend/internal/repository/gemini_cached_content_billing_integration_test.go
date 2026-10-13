//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 缓存与图片复用资金表，但缓存额度预留还必须覆盖尚未捕获的并发请求。
func TestGeminiCacheReservationAtomicKeyQuota(t *testing.T) {
	ctx := context.Background()
	for _, column := range []string{"quota", "rate_limit_5h", "rate_limit_1d", "rate_limit_7d"} {
		t.Run(column, func(t *testing.T) {
			repo, cmd, _ := newImageBillingIntegrationFixture(t, 10, 0)
			cmd.Quote = json.RawMessage(`{"kind":"gemini_cache"}`)
			cmd.Hold.BaseAmountUSD = 0.12
			_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`UPDATE api_keys SET %s=0.12 WHERE id=$1`, column), cmd.Hold.APIKeyID)
			require.NoError(t, err)
			var wg sync.WaitGroup
			var successes atomic.Int32
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					request := *cmd
					request.Hold.RequestID = fmt.Sprintf("gcache:create:parallel-%d-%d", cmd.Hold.APIKeyID, i)
					_, err := repo.ReserveImageBilling(ctx, &request)
					if err == nil {
						successes.Add(1)
					}
				}(i)
			}
			wg.Wait()
			require.Equal(t, int32(1), successes.Load())
			billingFocusAssertDecimal(t, "9.88", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
			billingFocusAssertDecimal(t, "0.12", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
			billingFocusAssertDecimal(t, "0", `SELECT quota_used::text FROM api_keys WHERE id=$1`, cmd.Hold.APIKeyID)
		})
	}
}

func TestGeminiCacheReservationRecoveryIsolationAndCapture(t *testing.T) {
	ctx := context.Background()
	repo, cmd, _ := newImageBillingIntegrationFixture(t, 1, 0)
	cmd.Quote = json.RawMessage(`{"kind":"gemini_cache"}`)
	cmd.Hold.RequestID = "gcache:create:recovery-" + cmd.Hold.RequestID
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	// 模拟进程重启：新的仓储实例只能看到持久合同，不能因为普通图片扫描而退款或重复发请求。
	restarted := &usageBillingRepository{db: integrationDB}
	_, err = integrationDB.ExecContext(ctx, `UPDATE image_billing_reservations SET expires_at=NOW()-INTERVAL '1 minute' WHERE request_id=$1 AND api_key_id=$2`, hold.ID, hold.Hold.APIKeyID)
	require.NoError(t, err)
	_, err = restarted.ReconcileExpiredImageBilling(ctx, time.Now(), 100)
	require.NoError(t, err)
	duplicate, err := restarted.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	require.False(t, duplicate.Applied)
	require.Equal(t, service.ImageBillingReconciliation, duplicate.State)
	billingFocusAssertDecimal(t, "0.12", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	capture := imageBillingCaptureCommand(hold)
	capture.Model = "gemini-2.5-pro"
	capture.ImageCount = 0
	result, err := restarted.CaptureImageBilling(ctx, hold.ID, capture, 0.06)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 0.06, result.BalanceAmountUSD, 1e-8)
	billingFocusAssertDecimal(t, "0.94", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	result, err = restarted.CaptureImageBilling(ctx, hold.ID, capture, 0.06)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.ErrorIs(t, restarted.ReleaseImageBilling(ctx, hold.ID, cmd.Hold.UserID, cmd.Hold.APIKeyID), service.ErrImageBillingReservationConflict)
}

func TestGeminiCacheReservationQuotaRollbackAndRelease(t *testing.T) {
	ctx := context.Background()
	repo, cmd, subID := newImageBillingIntegrationFixture(t, 1, 0.5)
	cmd.Quote = json.RawMessage(`{"kind":"gemini_cache"}`)
	_, err := integrationDB.ExecContext(ctx, `UPDATE api_keys SET quota=0.1 WHERE id=$1`, cmd.Hold.APIKeyID)
	require.NoError(t, err)
	_, err = repo.ReserveImageBilling(ctx, cmd)
	require.ErrorIs(t, err, service.ErrAPIKeyQuotaExhausted)
	billingFocusAssertDecimal(t, "0", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, subID)
	billingFocusAssertDecimal(t, "1", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET quota=1 WHERE id=$1`, cmd.Hold.APIKeyID)
	require.NoError(t, err)
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	require.NoError(t, repo.ReleaseImageBilling(ctx, hold.ID, cmd.Hold.UserID, cmd.Hold.APIKeyID))
	require.NoError(t, repo.ReleaseImageBilling(ctx, hold.ID, cmd.Hold.UserID, cmd.Hold.APIKeyID))
	billingFocusAssertDecimal(t, "0", `SELECT daily_usage_usd::text FROM user_subscriptions WHERE id=$1`, subID)
	billingFocusAssertDecimal(t, "1", `SELECT balance::text FROM users WHERE id=$1`, cmd.Hold.UserID)
}

// 两个不同密钥共用成员预算时，待结算预留也必须按实际成员合并。
func TestGeminiCacheReservationTeamPendingAllowanceAcrossKeys(t *testing.T) {
	ctx := context.Background()
	repo, cmd, _ := newImageBillingIntegrationFixture(t, 10, 0)
	client := testEntClient(t)
	member := mustCreateUser(t, client, &service.User{Email: "cache-member-" + uuid.NewString() + "@example.com"})
	teams := NewTeamRepository(integrationDB)
	team, err := teams.Create(ctx, "缓存预算", cmd.Hold.UserID, 10)
	require.NoError(t, err)
	invite := uuid.NewString()
	_, err = teams.CreateInvitation(ctx, team.Team.ID, cmd.Hold.UserID, member.Email, invite, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teams.ResolveInvitation(ctx, invite, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE team_memberships SET daily_limit_usd=0.12 WHERE team_id=$1 AND user_id=$2`, team.Team.ID, member.ID)
	require.NoError(t, err)
	cmd.Hold.TeamID = &team.Team.ID
	cmd.Hold.ActorUserID = member.ID
	cmd.Quote = json.RawMessage(`{"kind":"gemini_cache"}`)
	secondKey := mustCreateApiKey(t, client, &service.APIKey{UserID: cmd.Hold.UserID, GroupID: cmd.Hold.GroupID, Name: "second", Key: "sk-cache-" + uuid.NewString()})
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	second := *cmd
	second.Hold.APIKeyID = secondKey.ID
	second.Hold.RequestID += "-second"
	_, err = repo.ReserveImageBilling(ctx, &second)
	require.ErrorIs(t, err, service.ErrTeamMemberDailyExceeded)
	require.NoError(t, repo.ReleaseImageBilling(ctx, hold.ID, cmd.Hold.UserID, cmd.Hold.APIKeyID))
	hold, err = repo.ReserveImageBilling(ctx, &second)
	require.NoError(t, err)
	capture := imageBillingCaptureCommand(hold)
	capture.Model = "gemini-2.5-pro"
	capture.ImageCount = 0
	result, err := repo.CaptureImageBilling(ctx, hold.ID, capture, 0.12)
	require.NoError(t, err)
	require.True(t, result.Applied)
	billingFocusAssertDecimal(t, "0.12", `SELECT daily_usage_usd::text FROM team_memberships WHERE team_id=$1 AND user_id=$2`, team.Team.ID, member.ID)
}

// 多订阅各自保存倍率和额度，余量转余额时不能沿用最后一张订阅的倍率。
func TestGeminiCacheReservationMultipleSubscriptionsThenBalance(t *testing.T) {
	ctx := context.Background()
	repo, cmd, _ := newImageBillingIntegrationFixture(t, 1, 0.5)
	client := testEntClient(t)
	window := timezone.StartOfDay(time.Now())
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "cache-multi-" + uuid.NewString(), Price: 1, ValidityDays: 30, ValidityUnit: "day", GroupIDs: []int64{*cmd.Hold.GroupID}, GroupRateMultipliers: map[int64]float64{*cmd.Hold.GroupID: 2}, DailyLimitUSD: float64Ptr(0.2)})
	mustCreateSubscription(t, client, &service.UserSubscription{UserID: cmd.Hold.UserID, PlanID: plan.ID, DailyLimitUSD: float64Ptr(0.2), DailyWindowStart: &window})
	cmd.Quote = json.RawMessage(`{"kind":"gemini_cache"}`)
	cmd.Hold.BaseAmountUSD = 0.3
	hold, err := repo.ReserveImageBilling(ctx, cmd)
	require.NoError(t, err)
	require.Len(t, hold.Hold.SubscriptionHoldAllocations, 2)
	require.InDelta(t, 0.3-0.5/6.6-0.2/2, hold.Hold.BalanceHoldAmount, 1e-7)
	capture := imageBillingCaptureCommand(hold)
	capture.Model = "gemini-2.5-pro"
	capture.ImageCount = 0
	result, err := repo.CaptureImageBilling(ctx, hold.ID, capture, 0.3)
	require.NoError(t, err)
	require.InDelta(t, 0.7, result.SubscriptionAmountUSD, 1e-7)
	require.InDelta(t, hold.Hold.BalanceHoldAmount, result.BalanceAmountUSD, 1e-7)
}
