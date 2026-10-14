//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 独立测试库验证两种崩溃窗口和多实例恢复：捕获失败/捕获后日志失败都只扣一次且补回零额日志。
func TestImageBillingSettlementRecoveryConcurrentCaptureAndZeroLog(t *testing.T) {
	for _, captured := range []bool{false, true} {
		name := "capture_pending"
		if captured {
			name = "capture_committed_log_pending"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo, reserve, _ := newImageBillingIntegrationFixture(t, 1, 0)
			hold, err := repo.ReserveImageBilling(ctx, reserve)
			require.NoError(t, err)
			account := mustCreateAccount(t, testEntClient(t), &service.Account{Name: "image-recovery", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
			cmd := imageBillingCaptureCommand(hold)
			cmd.AccountID, cmd.BaseAmountUSD = account.ID, 0.12
			log := service.UsageLog{UserID: hold.Hold.ActorUserID, BillingUserID: hold.Hold.UserID, APIKeyID: hold.Hold.APIKeyID,
				AccountID: account.ID, RequestID: hold.ID, Model: "image-budget", GroupID: hold.Hold.GroupID, ImageCount: 1,
				TotalCost: 0.12, ActualCost: 0, CreatedAt: time.Now()}
			evidence := &service.ImageBillingSettlementEvidence{Version: 1, Command: *cmd, Usage: log}
			require.NoError(t, repo.SaveImageBillingSettlementEvidence(ctx, evidence))
			// 原价证据不可被后来修改的价格覆盖。
			changed := *evidence
			changed.Command.BaseAmountUSD = 0.11
			require.ErrorIs(t, repo.SaveImageBillingSettlementEvidence(ctx, &changed), service.ErrImageBillingReservationConflict)
			if captured {
				_, err = repo.CaptureImageBilling(ctx, hold.ID, cmd, 0.12)
				require.NoError(t, err)
			}
			_, err = (&usageLogRepository{}).createSingle(ctx, integrationDB, &log)
			require.NoError(t, err)
			require.NoError(t, repo.MarkImageBillingReconciliation(ctx, hold.ID, hold.Hold.UserID, hold.Hold.APIKeyID))
			_, err = integrationDB.ExecContext(ctx, `UPDATE image_billing_reservations SET settlement_retry_at=NOW()-INTERVAL '1 minute' WHERE request_id=$1 AND api_key_id=$2`, hold.ID, hold.Hold.APIKeyID)
			require.NoError(t, err)
			var wg sync.WaitGroup
			errs := make(chan error, 4)
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := repo.RecoverImageBillingSettlements(ctx, time.Now(), 25)
					errs <- err
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			billingFocusAssertDecimal(t, "0.88", `SELECT balance::text FROM users WHERE id=$1`, hold.Hold.UserID)
			billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, hold.Hold.UserID)
			billingFocusAssertDecimal(t, "0.12", `SELECT quota_used::text FROM api_keys WHERE id=$1`, hold.Hold.APIKeyID)
			billingFocusAssertDecimal(t, "0.12", `SELECT actual_cost::text FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, hold.ID, hold.Hold.APIKeyID)
			billingFocusAssertDecimal(t, "0.12", `SELECT balance_amount_usd::text FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, hold.ID, hold.Hold.APIKeyID)
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, hold.ID, hold.Hold.APIKeyID).Scan(&count))
			require.Equal(t, 1, count)
			recovered, err := repo.RecoverImageBillingSettlements(ctx, time.Now().Add(time.Hour), 25)
			require.NoError(t, err)
			for _, item := range recovered {
				require.NotEqual(t, hold.Hold.APIKeyID, item.APIKeyID)
			}
		})
	}
}
