//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 只声明密钥窗口写入，任何订阅、余额、成员或账号写入都会使 SQL mock 失败。
func TestUsageBillingEffectsSimpleModeOnlyWritesKeyWindows(t *testing.T) {
	for _, mode := range []string{service.APIKeyBillingModeAuto, service.APIKeyBillingModeBalance, service.APIKeyBillingModeSubscription} {
		t.Run(mode, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			mock.ExpectExec(`(?s)UPDATE api_keys SET\s+usage_5h =`).WithArgs(3.25, int64(13)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			teamID := int64(11)
			cmd := &service.UsageBillingCommand{UserID: 7, ActorUserID: 8, TeamID: &teamID, AccountID: 9, AccountType: service.AccountTypeAPIKey, APIKeyID: 13, APIKeyBillingMode: mode, APIKeyRateLimitCost: 3.25}
			result := &service.UsageBillingApplyResult{Applied: true}
			require.NoError(t, (&usageBillingRepository{}).applyUsageBillingEffects(context.Background(), tx, cmd, result))
			require.Zero(t, result.BalanceAmountUSD)
			require.Zero(t, result.SubscriptionAmountUSD)
			require.Empty(t, result.BillingAllocations)
			require.NoError(t, tx.Commit())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
