package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 完整失败请求正文必须随错误日志保留期清理，不能因为新增快照表而无限累积。
func TestOpsCleanupIncludesRequestPayloadDetails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		// SQL 连接清理也纳入测试断言，避免资源释放错误被静默忽略。
		mock.ExpectClose()
		require.NoError(t, db.Close())
	})
	svc := &OpsCleanupService{db: db, cfg: &config.Config{}, effective: config.OpsCleanupConfig{
		ErrorLogRetentionDays: 7, SystemLogRetentionDays: -1,
		MinuteMetricsRetentionDays: -1, HourlyMetricsRetentionDays: -1,
		BatchSize: 100, BatchPauseMS: 1,
	}}
	for _, table := range []string{"ops_error_logs", "ops_ingress_reject_aggregates", "ops_alert_events", "ops_system_log_cleanup_audits", "ops_request_details"} {
		deleted := int64(0)
		if table == "ops_request_details" {
			deleted = 3
		}
		mock.ExpectExec(`(?s)WITH batch AS MATERIALIZED.*DELETE FROM `+table+` AS target`).
			WithArgs(sqlmock.AnyArg(), 100).WillReturnResult(sqlmock.NewResult(0, deleted))
	}
	counts, err := svc.runCleanupOnce(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 3, counts.requestDetails)
	require.Contains(t, counts.String(), "request_details=3")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsCleanupPlan(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name         string
		days         int
		wantOK       bool
		wantTruncate bool
		wantCutoff   time.Time
	}{
		{name: "negative skips", days: -1, wantOK: false},
		{name: "zero truncates", days: 0, wantOK: true, wantTruncate: true},
		{name: "positive yields past cutoff", days: 7, wantOK: true, wantCutoff: now.AddDate(0, 0, -7)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cutoff, truncate, ok := opsCleanupPlan(now, tc.days)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if truncate != tc.wantTruncate {
				t.Fatalf("truncate = %v, want %v", truncate, tc.wantTruncate)
			}
			if !tc.wantTruncate && !cutoff.Equal(tc.wantCutoff) {
				t.Fatalf("cutoff = %v, want %v", cutoff, tc.wantCutoff)
			}
		})
	}
}

func TestIsMissingRelationError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil is not missing", err: nil, want: false},
		{name: "match relation does not exist", err: fakeErr(`pq: relation "ops_error_logs" does not exist`), want: true},
		{name: "match case-insensitive", err: fakeErr(`ERROR: Relation "x" Does Not Exist`), want: true},
		{name: "non-matching error", err: fakeErr("connection refused"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingRelationError(tc.err); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeleteOldRowsByCTIDBatchesAndThrottles(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer func() { _ = db.Close() }()

	cutoff := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	query := `(?s)WITH batch AS MATERIALIZED.*ORDER BY created_at ASC, id ASC.*DELETE FROM ops_system_logs AS target`
	mock.ExpectExec(query).WithArgs(cutoff, 2).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(query).WithArgs(cutoff, 2).WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := deleteOldRowsByCTID(context.Background(), db, "ops_system_logs", "created_at", cutoff, 2, time.Millisecond, false)
	if err != nil {
		t.Fatalf("delete rows: %v", err)
	}
	if result.deleted != 3 || result.batches != 2 {
		t.Fatalf("result = %#v, want deleted=3 batches=2", result)
	}
	if result.throttled <= 0 {
		t.Fatalf("throttled = %v, want positive duration", result.throttled)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestTruncateOpsTableUsesStatisticsInsteadOfFullCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`(?s)SELECT COALESCE.*FROM pg_class.*to_regclass`).
		WithArgs("ops_error_logs").
		WillReturnRows(sqlmock.NewRows([]string{"estimated_rows"}).AddRow(int64(4200)))
	mock.ExpectExec(`TRUNCATE TABLE ops_error_logs`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	estimatedRows, err := truncateOpsTable(context.Background(), db, "ops_error_logs")
	if err != nil {
		t.Fatalf("truncate table: %v", err)
	}
	if estimatedRows != 4200 {
		t.Fatalf("estimated rows = %d, want 4200", estimatedRows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestSleepOpsCleanupWithContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepOpsCleanupWithContext(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
