package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 单读替身用于确认旧仓储兼容路径不会改变列表契约。
type chanshuiHistorySingleStub struct {
	ChanshuiRepository
	configs []ChanshuiConfig
	runs    map[int64][]ChanshuiRun
	calls   int
	err     error
}

func (s *chanshuiHistorySingleStub) ListConfigs(context.Context) ([]ChanshuiConfig, error) {
	return append([]ChanshuiConfig(nil), s.configs...), nil
}

func (s *chanshuiHistorySingleStub) ListRuns(_ context.Context, id int64) ([]ChanshuiRun, error) {
	s.calls++
	return s.runs[id], s.err
}

type chanshuiHistoryBatchStub struct {
	*chanshuiHistorySingleStub
	batchCalls int
	ids        []int64
}

func (s *chanshuiHistoryBatchStub) ListRecentRuns(_ context.Context, ids []int64) (map[int64][]ChanshuiRun, error) {
	s.batchCalls++
	s.ids = append([]int64(nil), ids...)
	return s.runs, s.err
}

func TestChanshuiAdminHistoryUsesSingleBatchForTwoHundredConfigs(t *testing.T) {
	base := &chanshuiHistorySingleStub{runs: make(map[int64][]ChanshuiRun)}
	for id := int64(1); id <= 200; id++ {
		base.configs = append(base.configs, ChanshuiConfig{ID: id})
		base.runs[id] = []ChanshuiRun{{ID: fmt.Sprint(id), ConfigID: id, CreatedAt: time.Now(), Report: json.RawMessage(`{"probes":[]}`)}}
	}
	repo := &chanshuiHistoryBatchStub{chanshuiHistorySingleStub: base}
	svc := &ChanshuiService{repo: repo}
	configs, err := svc.AdminList(context.Background())
	require.NoError(t, err)
	require.Len(t, configs, 200)
	require.Equal(t, 1, repo.batchCalls)
	require.Zero(t, repo.calls)
	require.Len(t, repo.ids, 200)
	for _, config := range configs {
		require.Len(t, config.RecentRuns, 1)
		require.Equal(t, config.ID, config.LatestRun.ConfigID)
		require.Nil(t, config.LatestRun.Report)
	}
}

func TestChanshuiAdminHistoryKeepsLatestSixtyIsolatedAndClearsStaleLatest(t *testing.T) {
	start := time.Now().UTC()
	runs := []ChanshuiRun{{ID: "foreign", ConfigID: 2, CreatedAt: start.Add(time.Hour)}}
	for i := 0; i < 65; i++ {
		runs = append(runs, ChanshuiRun{ID: fmt.Sprintf("run-%02d", i), ConfigID: 1, CreatedAt: start.Add(time.Duration(i) * time.Second)})
	}
	repo := &chanshuiHistoryBatchStub{chanshuiHistorySingleStub: &chanshuiHistorySingleStub{
		configs: []ChanshuiConfig{{ID: 1}, {ID: 2, LatestRun: &ChanshuiRun{ID: "stale"}}},
		runs:    map[int64][]ChanshuiRun{1: runs},
	}}
	got, err := (&ChanshuiService{repo: repo}).AdminList(context.Background())
	require.NoError(t, err)
	require.Len(t, got[0].RecentRuns, 60)
	require.Equal(t, "run-64", got[0].LatestRun.ID)
	require.Equal(t, "run-05", got[0].RecentRuns[59].ID)
	require.Empty(t, got[1].RecentRuns)
	require.NotNil(t, got[1].RecentRuns)
	require.Nil(t, got[1].LatestRun)
}

func TestChanshuiAdminHistoryReadFailureIsNotRetriedAsSingleQueries(t *testing.T) {
	expected := errors.New("history unavailable")
	repo := &chanshuiHistoryBatchStub{chanshuiHistorySingleStub: &chanshuiHistorySingleStub{configs: []ChanshuiConfig{{ID: 1}}, err: expected}}
	got, err := (&ChanshuiService{repo: repo}).AdminList(context.Background())
	require.ErrorIs(t, err, expected)
	require.Nil(t, got)
	require.Equal(t, 1, repo.batchCalls)
	require.Zero(t, repo.calls)
}

func TestChanshuiAdminHistoryEmptyAndLegacyRepository(t *testing.T) {
	batch := &chanshuiHistoryBatchStub{chanshuiHistorySingleStub: &chanshuiHistorySingleStub{}}
	_, err := (&ChanshuiService{repo: batch}).AdminList(context.Background())
	require.NoError(t, err)
	require.Zero(t, batch.batchCalls)
	legacy := &chanshuiHistorySingleStub{configs: []ChanshuiConfig{{ID: 1}, {ID: 2}}, runs: map[int64][]ChanshuiRun{1: {{ID: "one", ConfigID: 1}}}}
	got, err := (&ChanshuiService{repo: legacy}).AdminList(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, legacy.calls)
	require.Equal(t, "one", got[0].LatestRun.ID)
	require.Empty(t, got[1].RecentRuns)
}
