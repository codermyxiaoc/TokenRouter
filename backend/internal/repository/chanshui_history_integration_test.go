//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestChanshuiPostgresBatchHistoryIsBoundedIsolatedAndHasNoReport(t *testing.T) {
	repo, first, _ := chanshuiFixture(t)
	ctx := context.Background()
	second := *first
	second.ID, second.Protocol = 0, "chat"
	require.NoError(t, repo.SaveConfig(ctx, &second))
	other := *first
	other.ID, other.Model = 0, "other-model"
	require.NoError(t, repo.SaveConfig(ctx, &other))
	start := time.Now().UTC().Add(-time.Hour)
	prefix := uuid.NewString()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	for _, config := range []*service.ChanshuiConfig{first, &second, &other} {
		for i := 0; i < 65; i++ {
			id := fmt.Sprintf("%s-%d-%02d", prefix, config.ID, i)
			created := start.Add(time.Duration(i) * time.Second)
			// 旧摘要中的状态与归属故意错误，实际查询必须采用持久列；报告内容不得返回。
			raw, err := json.Marshal(service.ChanshuiRun{ID: id, ConfigID: -1, AccountID: config.AccountID, Model: config.Model, Status: "queued", Verdict: "clean", ScoreComplete: true, CreatedAt: created, UpdatedAt: created, Report: json.RawMessage(`{"sensitive":"embedded-report"}`)})
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, `INSERT INTO chanshui_test_runs(id,config_id,status,record,report,config_updated_at,created_at,updated_at) VALUES($1,$2,'completed',$3,$4,$5,$6,$6)`, id, config.ID, string(raw), `{"sensitive":"full-report"}`, config.UpdatedAt, created)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tx.Commit())
	batch := repo.(*chanshuiRepository)
	history, err := batch.ListRecentRuns(ctx, []int64{first.ID, second.ID, first.ID})
	require.NoError(t, err)
	require.Len(t, history, 2)
	for _, config := range []*service.ChanshuiConfig{first, &second} {
		runs := history[config.ID]
		require.Len(t, runs, 60)
		require.Equal(t, fmt.Sprintf("%s-%d-64", prefix, config.ID), runs[0].ID)
		require.Equal(t, fmt.Sprintf("%s-%d-05", prefix, config.ID), runs[59].ID)
		for _, run := range runs {
			require.Equal(t, config.ID, run.ConfigID)
			require.Equal(t, "completed", run.Status)
			require.Nil(t, run.Report)
			require.Empty(t, run.RemoteID)
			require.Empty(t, run.LeaseToken)
		}
	}
	empty, err := batch.ListRecentRuns(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
