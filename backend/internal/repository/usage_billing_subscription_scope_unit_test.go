//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// 覆盖受限自动结算的分摊及失效回退，并保留指定订阅失效整笔拒绝的原语义。
func TestUsageBillingSubscriptionScopeAllocation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mode             string
		active           bool
		wantSubscription float64
		wantBalance      float64
		wantErr          error
	}{
		{"auto_partial", service.APIKeyBillingModeAuto, true, 0.2, 1.2, nil},
		{"auto_expired", service.APIKeyBillingModeAuto, false, 0, 2, nil},
		{"subscription_partial_unchanged", service.APIKeyBillingModeSubscription, true, 0.2, 1.2, nil},
		{"subscription_expired_unchanged", service.APIKeyBillingModeSubscription, false, 0, 0, service.ErrPreferredSubscriptionInsufficient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.ExpectBegin()
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			scopeID := int64(11)
			rows := sqlmock.NewRows([]string{
				"id", "plan_id", "starts_at", "expires_at", "daily_window_start", "weekly_window_start", "monthly_window_start",
				"daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd", "daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd", "daily_reset_count", "weekly_reset_count", "monthly_reset_count", "reset_counted_at", "group_rates",
			})
			if tc.active {
				now := time.Now().UTC()
				window := now.Add(-time.Hour)
				rows.AddRow(scopeID, int64(22), now.Add(-24*time.Hour), now.Add(24*time.Hour), window, window, window, 1.0, 1.0, 1.0, 0.8, 0.8, 0.8, int64(3), int64(2), int64(1), now, `{}`)
			}
			mock.ExpectQuery(`(?s)SELECT\s+id,\s+plan_id,.*FROM user_subscriptions.*AND \(\$4::bigint IS NULL OR id = \$4\).*FOR UPDATE`).
				WithArgs(int64(42), service.SubscriptionStatusActive, service.SubscriptionStatusPending, scopeID).WillReturnRows(rows)
			if tc.active {
				// 尚未到下一个重置点，原有日/周/月次数应随扣费原样保存，只有计数水位前进。
				mock.ExpectExec(`(?s)UPDATE user_subscriptions\s+SET.*WHERE id = \$7`).
					WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 1.0, 1.0, 1.0, scopeID, int64(3), int64(2), int64(1), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if tc.wantBalance > 0 {
				mock.ExpectQuery(`(?s)WITH locked_user AS \(.*SELECT updated.balance, \$1::numeric AS deducted_amount`).
					WithArgs(tc.wantBalance, int64(42)).WillReturnRows(sqlmock.NewRows([]string{"balance", "deducted_amount"}).AddRow(10-tc.wantBalance, tc.wantBalance))
			}
			cmd := &service.UsageBillingCommand{
				UserID: 42, BillableAmountUSD: 0.5, BaseAmountUSD: 1, SubscriptionRateMultiplier: 0.5,
				SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 2, APIKeyBillingMode: tc.mode, SubscriptionScopeID: &scopeID,
			}
			if tc.mode == service.APIKeyBillingModeSubscription {
				cmd.PreferredSubscriptionID = &scopeID
			}
			cmd.Normalize()
			result := &service.UsageBillingApplyResult{}
			err = (&usageBillingRepository{}).applyUsageBillingEffects(ctx, tx, cmd, result)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				require.InDelta(t, tc.wantSubscription, result.SubscriptionAmountUSD, 0.000001)
				require.InDelta(t, tc.wantBalance, result.BalanceAmountUSD, 0.000001)
				if tc.wantSubscription > 0 {
					require.Equal(t, scopeID, *result.BillingAllocations[0].SubscriptionID)
				}
			}
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUsageBillingSubscriptionScopeInvalidFailsBeforeTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, scopeID := range []int64{0, -1} {
		_, err = (&usageBillingRepository{db: db}).Apply(context.Background(), &service.UsageBillingCommand{
			RequestID: "invalid-scope", APIKeyBillingMode: service.APIKeyBillingModeAuto, SubscriptionScopeID: &scopeID,
		})
		require.ErrorIs(t, err, service.ErrUsageBillingSubscriptionScopeInvalid)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}
