//go:build unit

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func canvasBatchRows(items ...*service.CreativeCanvasExecution) *sqlmock.Rows {
	rows := sqlmock.NewRows(strings.Split(strings.ReplaceAll(canvasExecutionColumns, "COALESCE(lease_token::text,'')", "lease_token"), ","))
	for _, e := range items {
		if e == nil {
			continue
		}
		raw, _ := json.Marshal(e.Params)
		rows.AddRow(e.ID, e.UserID, e.CanvasID, e.NodeID, e.Kind, raw, e.IdempotencyKey, e.Fingerprint, e.TaskID, e.Status, e.ErrorCode, e.ErrorMessage, "", time.Now(), time.Now(), "")
	}
	return rows
}
func canvasRepoBatchItems() []*service.CreativeCanvasExecution {
	out := []*service.CreativeCanvasExecution{}
	for i, id := range []string{"child-one", "child-two"} {
		out = append(out, &service.CreativeCanvasExecution{ID: id, UserID: 7, CanvasID: "canvas", NodeID: "image", Kind: "image", IdempotencyKey: id, Fingerprint: "same-params", Status: "queued", Params: service.CreativeCanvasExecutionParams{BatchID: "batch", BatchIndex: i, BatchSize: 2, BatchOutputCount: 2}})
	}
	return out
}
func canvasExpectNewBatch(mock sqlmock.Sqlmock, items []*service.CreativeCanvasExecution, stored, inFlight int64) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT .* FROM creative_canvas_executions WHERE user_id=\\$1 AND params->>'batch_id'=\\$2").WithArgs(int64(7), "batch").WillReturnRows(canvasBatchRows())
	for _, e := range items {
		mock.ExpectQuery("SELECT .* FROM creative_canvas_executions WHERE user_id=\\$1 AND idempotency_key=\\$2").WithArgs(e.UserID, e.IdempotencyKey).WillReturnRows(canvasBatchRows(nil))
	}
	mock.ExpectQuery("SELECT document FROM creative_canvases .* FOR SHARE").WithArgs("canvas", int64(7)).WillReturnRows(sqlmock.NewRows([]string{"document"}).AddRow(`{"nodes":[{"id":"image","type":"image"}]}`))
	mock.ExpectQuery("SELECT COALESCE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"stored", "in_flight"}).AddRow(stored, inFlight))
}

// 批次插入和总容量校验必须处于同一事务，任一子任务失败不能留下已排队的半批。
func TestCreativeCanvasBatchRepositoryAtomicity(t *testing.T) {
	for _, mode := range []string{"success", "second_insert_failed", "quota", "execution_limit", "mixed_execution_limit", "quota_with_in_flight", "exact_limit"} {
		t.Run(mode, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			r := &creativeCanvasRepository{db: db}
			items := canvasRepoBatchItems()
			stored, inFlight := int64(0), int64(0)
			switch mode {
			case "quota", "quota_with_in_flight":
				stored = service.CreativeCanvasUserQuota - (128 << 20)
			case "execution_limit":
				inFlight = service.CreativeCanvasUserQuota - (128 << 20)
			case "mixed_execution_limit":
				stored, inFlight = 256<<20, service.CreativeCanvasUserQuota-(256<<20)
			case "exact_limit":
				stored, inFlight = 256<<20, service.CreativeCanvasUserQuota-(512<<20)
			}
			if mode == "quota_with_in_flight" {
				inFlight = 128 << 20
			}
			canvasExpectNewBatch(mock, items, stored, inFlight)
			accepted := mode == "success" || mode == "exact_limit"
			if accepted || mode == "second_insert_failed" {
				mock.ExpectQuery("INSERT INTO creative_canvas_executions.* RETURNING").WithArgs(items[0].ID, int64(7), "canvas", "image", "image", sqlmock.AnyArg(), items[0].IdempotencyKey, "same-params").WillReturnRows(canvasBatchRows(items[0]))
				second := mock.ExpectQuery("INSERT INTO creative_canvas_executions.* RETURNING").WithArgs(items[1].ID, int64(7), "canvas", "image", "image", sqlmock.AnyArg(), items[1].IdempotencyKey, "same-params")
				if accepted {
					second.WillReturnRows(canvasBatchRows(items[1]))
					mock.ExpectCommit()
				} else {
					second.WillReturnError(errors.New("database unavailable"))
					mock.ExpectRollback()
				}
			} else {
				mock.ExpectRollback()
			}
			out, err := r.CreateExecutionBatch(context.Background(), items)
			if accepted {
				require.NoError(t, err)
				require.Len(t, out, 2)
			} else {
				require.Error(t, err)
				require.Nil(t, out)
			}
			if mode == "quota" || mode == "quota_with_in_flight" {
				require.ErrorIs(t, err, service.ErrCanvasQuota)
				require.Equal(t, http.StatusRequestEntityTooLarge, infraerrors.Code(err))
			}
			if mode == "execution_limit" || mode == "mixed_execution_limit" {
				require.ErrorIs(t, err, service.ErrCanvasExecutionLimit)
				require.Equal(t, http.StatusTooManyRequests, infraerrors.Code(err))
				require.Equal(t, "CANVAS_EXECUTION_LIMIT", infraerrors.Reason(err))
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCreativeCanvasBatchRepositoryReplayConflictAndIncomplete(t *testing.T) {
	for _, mode := range []string{"replay", "changed", "incomplete", "changed_shape"} {
		t.Run(mode, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			items := canvasRepoBatchItems()
			old := *items[0]
			if mode == "changed" {
				old.Fingerprint = "different-count-or-prompt"
			}
			mock.ExpectBegin()
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
			accepted := []*service.CreativeCanvasExecution{&old, items[1]}
			if mode == "incomplete" {
				accepted = accepted[:1]
			}
			mock.ExpectQuery("SELECT .* FROM creative_canvas_executions WHERE user_id=\\$1 AND params->>'batch_id'=\\$2").WithArgs(int64(7), "batch").WillReturnRows(canvasBatchRows(accepted...))
			if mode == "changed_shape" {
				copy := *items[0]
				copy.Params.BatchSize = 1
				items = []*service.CreativeCanvasExecution{&copy}
			}
			mock.ExpectRollback()
			out, err := (&creativeCanvasRepository{db: db}).CreateExecutionBatch(context.Background(), items)
			if mode == "replay" || mode == "changed_shape" {
				require.NoError(t, err)
				require.Len(t, out, 2)
			} else {
				require.ErrorIs(t, err, service.ErrCanvasConflict)
				require.Nil(t, out)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCreativeCanvasBatchRepositoryOwnerRecheckedInsideTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	items := canvasRepoBatchItems()
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT .* FROM creative_canvas_executions WHERE user_id=\\$1 AND params->>'batch_id'=\\$2").WithArgs(int64(7), "batch").WillReturnRows(canvasBatchRows())
	for _, e := range items {
		mock.ExpectQuery("SELECT .* FROM creative_canvas_executions WHERE user_id=\\$1 AND idempotency_key=\\$2").WithArgs(e.UserID, e.IdempotencyKey).WillReturnRows(canvasBatchRows(nil))
	}
	mock.ExpectQuery("SELECT document FROM creative_canvases .* FOR SHARE").WithArgs("canvas", int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, err = (&creativeCanvasRepository{db: db}).CreateExecutionBatch(context.Background(), items)
	require.ErrorIs(t, err, service.ErrCanvasNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
