package repository

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

const dailyActivitySpendingSettingSQL = `SELECT value, clock_timestamp\(\) FROM settings WHERE key = \$1`

// 活动关闭后仍累计消费，但没有首次开启时间、非法配置或未来开始时间必须保持原扣款行为。
func TestRecordDailyActivitySpendingConfiguration(t *testing.T) {
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, config string
		missing      bool
		write        bool
	}{
		{name: "missing", missing: true},
		{name: "never started", config: `{"enabled":false}`},
		{name: "enabled without timestamp", config: `{"enabled":true}`},
		{name: "malformed JSON", config: `{"started_at":`},
		{name: "malformed timestamp", config: `{"started_at":"yesterday"}`},
		{name: "zero timestamp", config: `{"started_at":"0001-01-01T00:00:00Z"}`},
		{name: "future timestamp", config: `{"started_at":"2026-10-14T00:00:00Z"}`},
		{name: "exact start", config: `{"enabled":true,"started_at":"2026-10-13T16:00:00+08:00"}`, write: true},
		{name: "paused retains progress", config: `{"enabled":false,"started_at":"2026-10-12T00:00:00Z"}`, write: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			query := mock.ExpectQuery(dailyActivitySpendingSettingSQL).WithArgs(dailyActivitySpendingSettingKey)
			if tc.missing {
				query.WillReturnError(sql.ErrNoRows)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"value", "clock_timestamp"}).AddRow(tc.config, now))
			}
			if tc.write {
				mock.ExpectExec(`(?s)INSERT INTO daily_activity_spending_events.*ON CONFLICT \(source_key\) DO NOTHING`).
					WithArgs(int64(42), "usage:7:req-capture", "0.7059300011", now).
					WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectExec(`(?s)INSERT INTO daily_activity_spending_totals.*ON CONFLICT \(user_id\) DO UPDATE`).
					WithArgs(int64(42), "0.7059300011").WillReturnResult(sqlmock.NewResult(1, 1))
			}
			mock.ExpectCommit()
			require.NoError(t, recordDailyActivitySpending(context.Background(), tx, 42, 7, " req-capture ", 0.1059300011, 0.6))
			require.NoError(t, tx.Commit())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// 独立消费事件的唯一键是累计闸门，即使上层重放防护遗漏也不能重复增加总消费。
func TestRecordDailyActivitySpendingDuplicateSkipsTotal(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(dailyActivitySpendingSettingSQL).WithArgs(dailyActivitySpendingSettingKey).
		WillReturnRows(sqlmock.NewRows([]string{"value", "clock_timestamp"}).AddRow(`{"started_at":"2026-01-01T00:00:00Z"}`, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)))
	mock.ExpectExec(`(?s)INSERT INTO daily_activity_spending_events`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	require.NoError(t, recordDailyActivitySpending(context.Background(), tx, 42, 7, "request", .1, .2))
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

// 汇总更新失败时事件与真实扣款必须一起回滚，禁止出现可见事件但没有累计额度的半完成状态。
func TestRecordDailyActivitySpendingTotalFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(dailyActivitySpendingSettingSQL).WithArgs(dailyActivitySpendingSettingKey).
		WillReturnRows(sqlmock.NewRows([]string{"value", "clock_timestamp"}).AddRow(`{"started_at":"2026-01-01T00:00:00Z"}`, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)))
	mock.ExpectExec(`(?s)INSERT INTO daily_activity_spending_events`).WillReturnResult(sqlmock.NewResult(1, 1))
	want := errors.New("spending total unavailable")
	mock.ExpectExec(`(?s)INSERT INTO daily_activity_spending_totals`).WillReturnError(want)
	mock.ExpectRollback()
	require.ErrorIs(t, recordDailyActivitySpending(context.Background(), tx, 42, 7, "request", .1, .2), want)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

// 没有资金实际变化的调用不增加数据库访问，也不能产生奖励资格。
func TestRecordDailyActivitySpendingSkipsInvalidOrZeroCharges(t *testing.T) {
	for _, tc := range []struct {
		userID, apiKeyID     int64
		requestID            string
		subscription, wallet float64
	}{
		{42, 7, "request", 0, 0},
		{0, 7, "request", 1, 0},
		{42, 0, "request", 1, 0},
		{42, 7, " ", 1, 0},
		{42, 7, "request", -1, 2},
		{42, 7, "request", 2, -1},
		{42, 7, "request", math.NaN(), 0},
		{42, 7, "request", 0, math.Inf(1)},
	} {
		require.NoError(t, recordDailyActivitySpending(context.Background(), nil, tc.userID, tc.apiKeyID, tc.requestID, tc.subscription, tc.wallet))
	}
}

// 账本读写故障必须反馈给外层资金事务，避免已扣费但活动进度无法补偿。
func TestRecordDailyActivitySpendingDatabaseFailure(t *testing.T) {
	for _, writeFailure := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		mock.ExpectBegin()
		tx, err := db.BeginTx(context.Background(), nil)
		require.NoError(t, err)
		query := mock.ExpectQuery(dailyActivitySpendingSettingSQL).WithArgs(dailyActivitySpendingSettingKey)
		want := errors.New("database unavailable")
		if writeFailure {
			query.WillReturnRows(sqlmock.NewRows([]string{"value", "clock_timestamp"}).AddRow(`{"started_at":"2026-01-01T00:00:00Z"}`, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)))
			mock.ExpectExec(`(?s)INSERT INTO daily_activity_spending_events`).WillReturnError(want)
		} else {
			query.WillReturnError(want)
		}
		mock.ExpectRollback()
		require.ErrorIs(t, recordDailyActivitySpending(context.Background(), tx, 42, 7, "request", 0.1, 0.2), want)
		require.NoError(t, tx.Rollback())
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}

// 活动记录落库失败必须回滚整笔普通扣费，包括已经认领的资金去重键。
func TestDailyActivitySpendingFailureRollsBackUsageBilling(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	command := &service.UsageBillingCommand{RequestID: "activity-failure", RequestFingerprint: "fingerprint", UserID: 42, APIKeyID: 7, BillableAmountUSD: .42, APIKeyBillingMode: service.APIKeyBillingModeBalance}
	mock.ExpectBegin()
	mock.ExpectQuery(usageBillingClaimSQL).WithArgs(command.RequestID, command.APIKeyID, command.RequestFingerprint).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(usageBillingArchiveSQL).WithArgs(command.RequestID, command.APIKeyID).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(usageBillingUserLockSQL).WithArgs(command.UserID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(command.UserID))
	mock.ExpectQuery(usageBillingBalanceDeductSQL).WithArgs(.42, command.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "deducted_amount"}).AddRow(9.58, .42))
	mock.ExpectQuery(dailyActivitySpendingSettingSQL).WithArgs(dailyActivitySpendingSettingKey).
		WillReturnRows(sqlmock.NewRows([]string{"value", "clock_timestamp"}).AddRow(`{"started_at":"2026-01-01T00:00:00Z"}`, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)))
	want := errors.New("activity ledger write failed")
	mock.ExpectExec(`(?s)INSERT INTO daily_activity_spending_events`).WillReturnError(want)
	mock.ExpectRollback()
	_, err = (&usageBillingRepository{db: db}).Apply(context.Background(), command)
	require.ErrorIs(t, err, want)
	require.NoError(t, mock.ExpectationsWereMet())
}
