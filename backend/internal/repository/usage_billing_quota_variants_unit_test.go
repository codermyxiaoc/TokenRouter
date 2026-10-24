//go:build unit

package repository

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

var accountQuotaStateColumns = []string{"quota_used", "quota_limit", "quota_daily_used", "quota_daily_limit", "quota_weekly_used", "quota_weekly_limit"}

func TestAccountQuotaIncrementSQLs_GuardOmittedDimensions(t *testing.T) {
	for _, daily := range []bool{false, true} {
		for _, weekly := range []bool{false, true} {
			query := accountQuotaIncrementSQLs[accountQuotaIncrementSQLIndex(daily, weekly)]
			set, where, ok := strings.Cut(query, "WHERE ")
			require.True(t, ok)
			require.Equal(t, !daily || !weekly, strings.Contains(where, accountQuotaExtraIsObjectCond), "daily=%v weekly=%v", daily, weekly)
			require.Equal(t, daily, strings.Contains(set, "'quota_daily_used'"), "daily=%v weekly=%v", daily, weekly)
			require.Equal(t, weekly, strings.Contains(set, "'quota_weekly_used'"), "daily=%v weekly=%v", daily, weekly)
			require.Equal(t, !daily, strings.Contains(where, accountQuotaDailyLimitUnsetCond), "daily=%v weekly=%v", daily, weekly)
			require.Equal(t, !weekly, strings.Contains(where, accountQuotaWeeklyLimitUnsetCond), "daily=%v weekly=%v", daily, weekly)
		}
	}
}

func TestIncrementUsageBillingAccountQuota_UsesStatementMatchingHints(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(accountQuotaIncrementSQLs[accountQuotaIncrementSQLIndex(true, false)]).
		WithArgs(2.5, int64(9)).
		WillReturnRows(sqlmock.NewRows(accountQuotaStateColumns).AddRow(12.5, 100.0, 2.5, 50.0, 0.0, 0.0))
	mock.ExpectCommit()

	state, err := incrementUsageBillingAccountQuota(ctx, tx, 9, 2.5, true, false)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.InDelta(t, 12.5, state.TotalUsed, 0.000001)
	require.InDelta(t, 2.5, state.DailyUsed, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIncrementUsageBillingAccountQuota_FallsBackToFullStatementWhenGuardMisses(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(accountQuotaIncrementSQLs[accountQuotaIncrementSQLIndex(false, false)]).
		WithArgs(2.5, int64(9)).
		WillReturnRows(sqlmock.NewRows(accountQuotaStateColumns))
	mock.ExpectQuery(accountQuotaIncrementSQLs[accountQuotaIncrementSQLIndex(true, true)]).
		WithArgs(2.5, int64(9)).
		WillReturnRows(sqlmock.NewRows(accountQuotaStateColumns).AddRow(2.5, 0.0, 0.0, 0.0, 2.5, 20.0))
	mock.ExpectCommit()

	state, err := incrementUsageBillingAccountQuota(ctx, tx, 9, 2.5, false, false)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.InDelta(t, 2.5, state.WeeklyUsed, 0.000001)
	require.InDelta(t, 20.0, state.WeeklyLimit, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIncrementUsageBillingAccountQuota_ReturnsNotFoundWhenFullStatementMisses(t *testing.T) {
	for _, hints := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		ctx := context.Background()
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)

		mock.ExpectBegin()
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		mock.ExpectQuery(accountQuotaIncrementSQLs[accountQuotaIncrementSQLIndex(hints[0], hints[1])]).
			WithArgs(2.5, int64(9)).
			WillReturnRows(sqlmock.NewRows(accountQuotaStateColumns))
		if !(hints[0] && hints[1]) {
			mock.ExpectQuery(accountQuotaIncrementSQLs[accountQuotaIncrementSQLIndex(true, true)]).
				WithArgs(2.5, int64(9)).
				WillReturnRows(sqlmock.NewRows(accountQuotaStateColumns))
		}
		mock.ExpectRollback()

		state, err := incrementUsageBillingAccountQuota(ctx, tx, 9, 2.5, hints[0], hints[1])
		require.ErrorIs(t, err, service.ErrAccountNotFound, "hints=%v", hints)
		require.Nil(t, state)
		require.NoError(t, tx.Rollback())
		require.NoError(t, mock.ExpectationsWereMet(), "hints=%v", hints)
		_ = db.Close()
	}
}
