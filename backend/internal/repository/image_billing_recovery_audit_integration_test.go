//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 真实事务审计夹具只写独立测试库，保留原预占、账号及结算命令供并发/故障恢复使用。
func newImageRecoveryAuditFixture(t *testing.T, accountType string) (*usageBillingRepository, *service.ImageBillingReservation, *service.UsageBillingCommand, *service.ImageBillingSettlementEvidence) {
	t.Helper()
	repo, reserve, _ := newImageBillingIntegrationFixture(t, 1, 0)
	hold, err := repo.ReserveImageBilling(context.Background(), reserve)
	require.NoError(t, err)
	account := mustCreateAccount(t, testEntClient(t), &service.Account{Name: "image-recovery-audit", Platform: service.PlatformOpenAI, Type: accountType})
	cmd := imageBillingCaptureCommand(hold)
	cmd.AccountID, cmd.AccountType, cmd.AccountQuotaCost, cmd.BaseAmountUSD = account.ID, accountType, 0.12, 0.12
	log := service.UsageLog{UserID: hold.Hold.ActorUserID, BillingUserID: hold.Hold.UserID, APIKeyID: hold.Hold.APIKeyID,
		AccountID: account.ID, RequestID: hold.ID, Model: cmd.Model, GroupID: hold.Hold.GroupID,
		ImageCount: 1, TotalCost: 0.12, CreatedAt: time.Now()}
	return repo, hold, cmd, &service.ImageBillingSettlementEvidence{Version: 1, Command: *cmd, Usage: log}
}

// 已产图后删除原账号只能停用该账号，不能使冻结资金和真实用量永远停在待核对。
func TestImageBillingRecoverySoftDeletedAccountCapturesAndLogsOnce(t *testing.T) {
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeBedrock} {
		for _, captured := range []bool{false, true} {
			name := accountType + "/capture_pending"
			if captured {
				name = accountType + "/log_pending"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				repo, hold, cmd, evidence := newImageRecoveryAuditFixture(t, accountType)
				require.NoError(t, repo.SaveImageBillingSettlementEvidence(ctx, evidence))
				if captured {
					_, err := repo.CaptureImageBilling(ctx, hold.ID, cmd, 0.12)
					require.NoError(t, err)
				}
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, cmd.AccountID)
				require.NoError(t, err)
				recovered, err := repo.RecoverImageBillingSettlements(ctx, time.Now().Add(2*time.Minute), 25)
				require.NoError(t, err)
				require.Len(t, recovered, 1)
				require.Equal(t, cmd.AccountID, recovered[0].AccountID)
				billingFocusAssertDecimal(t, "0.88", `SELECT balance::text FROM users WHERE id=$1`, cmd.UserID)
				billingFocusAssertDecimal(t, "0", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.UserID)
				billingFocusAssertDecimal(t, "0.12", `SELECT quota_used::text FROM api_keys WHERE id=$1`, cmd.APIKeyID)
				billingFocusAssertDecimal(t, "0.12", `SELECT actual_cost::text FROM usage_logs WHERE request_id=$1 AND api_key_id=$2 AND user_id=$3 AND billing_user_id=$4 AND account_id=$5`, hold.ID, cmd.APIKeyID, cmd.ActorUserID, cmd.UserID, cmd.AccountID)
				// 重放返回原捕获结果，既不重复减余额，也不补记已删除账号的额度。
				result, err := repo.CaptureImageBilling(ctx, hold.ID, cmd, 0.12)
				require.NoError(t, err)
				require.False(t, result.Applied)
				require.InDelta(t, 0.12, result.BalanceAmountUSD, 1e-10)
				wantAccountUsage := "0"
				if captured {
					wantAccountUsage = "0.12"
				}
				billingFocusAssertDecimal(t, wantAccountUsage, `SELECT COALESCE(extra->>'quota_used','0') FROM accounts WHERE id=$1`, cmd.AccountID)
				var count int
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, hold.ID, cmd.APIKeyID).Scan(&count))
				require.Equal(t, 1, count)
				recovered, err = repo.RecoverImageBillingSettlements(ctx, time.Now().Add(time.Hour), 25)
				require.NoError(t, err)
				require.Empty(t, recovered)
			})
		}
	}
}

// 软删除例外仅属于已验证预占的捕获，普通同步命令仍回滚且不产生账单。
func TestImageBillingDeletedAccountExceptionDoesNotRelaxOrdinaryApply(t *testing.T) {
	ctx := context.Background()
	repo, hold, cmd, _ := newImageRecoveryAuditFixture(t, service.AccountTypeAPIKey)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, cmd.AccountID)
	require.NoError(t, err)
	cmd.RequestID = "ordinary:" + hold.ID
	cmd.BillableAmountUSD = 0.12
	cmd.SubscriptionRateMultiplier, cmd.SubscriptionRateMultiplierScale, cmd.BalanceRateMultiplier = 1, 1, 1
	_, err = repo.Apply(ctx, cmd)
	require.ErrorIs(t, err, service.ErrAccountNotFound)
	billingFocusAssertDecimal(t, "0.88", `SELECT balance::text FROM users WHERE id=$1`, cmd.UserID)
	billingFocusAssertDecimal(t, "0.12", `SELECT frozen_balance::text FROM users WHERE id=$1`, cmd.UserID)
	billingFocusAssertDecimal(t, "0", `SELECT quota_used::text FROM api_keys WHERE id=$1`, cmd.APIKeyID)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, cmd.APIKeyID).Scan(&count))
	require.Zero(t, count)
}

// 同请求的异主使用记录必须保持原值，不能为了恢复展示而改写别人的账单。
func TestImageBillingRecoveryRefusesConflictingUsageOwner(t *testing.T) {
	ctx := context.Background()
	repo, hold, cmd, evidence := newImageRecoveryAuditFixture(t, service.AccountTypeAPIKey)
	require.NoError(t, repo.SaveImageBillingSettlementEvidence(ctx, evidence))
	other := mustCreateUser(t, testEntClient(t), &service.User{Email: "other-" + hold.ID + "@example.test", Balance: 0})
	wrong := evidence.Usage
	wrong.UserID = other.ID
	_, err := (&usageLogRepository{}).createSingle(ctx, integrationDB, &wrong)
	require.NoError(t, err)
	_, err = repo.RecoverImageBillingSettlements(ctx, time.Now().Add(2*time.Minute), 25)
	require.ErrorIs(t, err, service.ErrImageBillingReservationConflict)
	billingFocusAssertDecimal(t, "0", `SELECT actual_cost::text FROM usage_logs WHERE request_id=$1 AND api_key_id=$2 AND user_id=$3`, hold.ID, cmd.APIKeyID, other.ID)
	billingFocusAssertDecimal(t, "0.88", `SELECT balance::text FROM users WHERE id=$1`, cmd.UserID)
	var incomplete bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT settlement_completed_at IS NULL FROM image_billing_reservations WHERE request_id=$1 AND api_key_id=$2`, hold.ID, cmd.APIKeyID).Scan(&incomplete))
	require.True(t, incomplete)
}
