package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 租约校验失败不得写历史；保存历史失败时，状态更新与解锁也必须回滚。
func TestGroupAvailabilityProbeSaveFencesLeaseAndCommitsAtomically(t *testing.T) {
	for _, test := range []struct {
		name       string
		token      string
		affected   int64
		historyErr error
	}{
		{name: "missing_lease"},
		{name: "stale_lease", token: "old-token"},
		{name: "current_lease", token: "current-token", affected: 1},
		{name: "history_failure", token: "current-token", affected: 1, historyErr: errors.New("history unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			now := time.Now()
			next := now.Add(time.Hour)
			result := &service.GroupAvailabilityProbeResult{GroupID: 42, LeaseToken: test.token, ModelID: "test-model",
				Status: "failed", ErrorMessage: "context deadline exceeded", StartedAt: now, FinishedAt: now}
			if test.token != "" {
				mock.ExpectBegin()
				mock.ExpectExec(`(?s)UPDATE group_availability_probe_states SET.*WHERE group_id=\$1 AND locked_by=\$8`).
					WithArgs(int64(42), next, "failed", false, int64(0), result.ErrorMessage, now, test.token).
					WillReturnResult(sqlmock.NewResult(0, test.affected))
				if test.affected == 1 {
					insert := mock.ExpectExec(`INSERT INTO group_availability_probe_results`).
						WithArgs(int64(42), nil, result.ModelID, "failed", false, int64(0), result.ErrorMessage, now, now)
					if test.historyErr != nil {
						insert.WillReturnError(test.historyErr)
					} else {
						insert.WillReturnResult(sqlmock.NewResult(1, 1))
					}
				}
				if test.affected == 1 && test.historyErr == nil {
					mock.ExpectCommit()
				} else {
					mock.ExpectRollback()
				}
			}
			err = NewGroupAvailabilityProbeRepository(db).SaveResultAndScheduleNext(context.Background(), result, next)
			switch {
			case test.token == "" || test.affected == 0:
				require.ErrorIs(t, err, service.ErrGroupAvailabilityProbeLeaseLost)
			case test.historyErr != nil:
				require.ErrorIs(t, err, test.historyErr)
			default:
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
