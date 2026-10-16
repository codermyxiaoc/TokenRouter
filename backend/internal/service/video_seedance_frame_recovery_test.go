//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 旧任务夹具只保存帧数终态，却保留旧解析器错误提取的请求秒数。
func seedanceFrameRecoveryFixture(t *testing.T, reservedSeconds float64) (*VideoTaskService, *VideoTaskRecord, *videoLifecycleUpstream, *videoLifecycleBilling, *videoLifecycleLogs) {
	t.Helper()
	s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
	upstream.selection.Target = officialProtocolTarget(VideoEndpointSeedance, VideoProtocolAdapterSeedance)
	upstream.selection.Target.AccountID = upstream.selection.Account.ID
	upstream.selection.Metadata.DurationSeconds = reservedSeconds
	created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model"}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), created.LocalTaskID)
	require.NoError(t, err)
	task.Status = "completed"
	task.Metadata.DurationSeconds = 2
	task.ResponseStatus = 200
	task.RawResponse = []byte(`{"id":"upstream-task","status":"succeeded","frames":57,"framespersecond":24,"resolution":"720p","content":{"video_url":"https://cdn.example.com/result.mp4"}}`)
	require.NoError(t, s.repo.Save(context.Background(), task, false))
	return s, task, upstream, billing, logs
}

type seedanceFrameRecoverySaveRepo struct {
	VideoTaskRepository
	err   error
	saves int
}

func (r *seedanceFrameRecoverySaveRepo) Save(ctx context.Context, task *VideoTaskRecord, release bool) error {
	r.saves++
	if r.err != nil {
		return r.err
	}
	return r.VideoTaskRepository.Save(ctx, task, release)
}

// 模拟捕获刚提交进程即中断，后续记录必须从更正后的持久秒数恢复，而不能重放旧的两秒。
func TestVideoSeedanceFrameRecoveryPersistsBeforeCaptureAndSurvivesRestart(t *testing.T) {
	s, task, upstream, billing, logs := seedanceFrameRecoveryFixture(t, 8)
	logs.err = errors.New("模拟扣费完成后使用记录暂不可写")
	s.finish(context.Background(), task)
	fresh, err := s.repo.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "settled", fresh.BillingStatus)
	require.InDelta(t, 57.0/24, fresh.Metadata.DurationSeconds, 1e-12)
	require.InDelta(t, 4.75, fresh.BillingResult.ActualAmountUSD, 1e-12)
	require.Equal(t, 1, billing.captures)
	require.Zero(t, billing.releases)
	require.Empty(t, logs.logs)
	logs.err = nil
	restarted := *s
	restarted.advance(context.Background(), fresh)
	require.Equal(t, 1, billing.captures)
	require.Zero(t, upstream.polls)
	require.Equal(t, 1, upstream.submitted)
	require.Len(t, logs.logs, 1)
	log := logs.logs["video_capture:"+task.ID]
	require.InDelta(t, 4.75, log.ActualCost, 1e-12)
	require.Equal(t, 3, *log.VideoDurationSeconds)
}

// 状态存储失败必须发生在捕获之前；恢复持久存储后沿用唯一任务及唯一扣费键。
func TestVideoSeedanceFrameRecoverySaveFailurePreventsCapture(t *testing.T) {
	s, task, _, billing, _ := seedanceFrameRecoveryFixture(t, 8)
	repo := &seedanceFrameRecoverySaveRepo{VideoTaskRepository: s.repo, err: errors.New("模拟数据库不可写")}
	s.repo = repo
	s.finish(context.Background(), task)
	require.Equal(t, 1, repo.saves)
	require.Zero(t, billing.captures)
	require.Empty(t, billing.captureCommands)
	fresh, err := s.repo.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, float64(2), fresh.Metadata.DurationSeconds)
	require.Equal(t, "reserved", fresh.BillingStatus)
	repo.err = nil
	s.finish(context.Background(), fresh)
	require.Equal(t, 1, billing.captures)
	require.InDelta(t, 57.0/24, billing.captureCommands[0].ActualBaseAmountUSD, 1e-12)
}

// 历史预扣不足不授权扩大捕获金额，必须保留核对状态且不能按旧的较短秒数结算。
func TestVideoSeedanceFrameRecoveryKeepsInsufficientHistoricalHoldUnsettled(t *testing.T) {
	s, task, _, billing, logs := seedanceFrameRecoveryFixture(t, 2)
	task.RawResponse = []byte(`{"id":"upstream-task","status":"succeeded","frames":289,"framespersecond":24,"resolution":"720p"}`)
	s.finish(context.Background(), task)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Zero(t, billing.captures)
	require.Zero(t, billing.releases)
	require.Empty(t, logs.logs)
	require.Len(t, billing.captureCommands, 1)
	require.InDelta(t, 289.0/24, billing.captureCommands[0].ActualBaseAmountUSD, 1e-12)
	require.Equal(t, float64(2), task.Hold.BaseAmountUSD)
}

// 畸形或不属于本任务的历史帧数完成包，不得被已保存的请求时长掩盖。
func TestVideoSeedanceFrameRecoveryRejectsAmbiguousEvidence(t *testing.T) {
	for name, body := range map[string]string{
		"missing_fps":        `{"id":"upstream-task","status":"succeeded","frames":57,"resolution":"720p"}`,
		"string_fps":         `{"id":"upstream-task","status":"succeeded","frames":57,"framespersecond":"24","resolution":"720p"}`,
		"zero_fps":           `{"id":"upstream-task","status":"succeeded","frames":57,"framespersecond":0,"resolution":"720p"}`,
		"wrong_fps":          `{"id":"upstream-task","status":"succeeded","frames":57,"framespersecond":48,"resolution":"720p"}`,
		"null_frames":        `{"id":"upstream-task","status":"succeeded","frames":null,"framespersecond":24,"resolution":"720p"}`,
		"string_frames":      `{"id":"upstream-task","status":"succeeded","frames":"57","framespersecond":24,"resolution":"720p"}`,
		"out_of_range":       `{"id":"upstream-task","status":"succeeded","frames":293,"framespersecond":24,"resolution":"720p"}`,
		"off_stride":         `{"id":"upstream-task","status":"succeeded","frames":58,"framespersecond":24,"resolution":"720p"}`,
		"duplicate_frames":   `{"id":"upstream-task","status":"succeeded","frames":57,"frames":289,"framespersecond":24,"resolution":"720p"}`,
		"uppercase_frames":   `{"id":"upstream-task","status":"succeeded","Frames":57,"framespersecond":24,"resolution":"720p"}`,
		"uppercase_fps":      `{"id":"upstream-task","status":"succeeded","frames":57,"FramesPerSecond":24,"resolution":"720p"}`,
		"duration_conflict":  `{"id":"upstream-task","status":"succeeded","duration":2,"frames":57,"framespersecond":24,"resolution":"720p"}`,
		"wrong_owner":        `{"id":"another-task","status":"succeeded","frames":57,"framespersecond":24,"resolution":"720p"}`,
		"nonterminal_status": `{"id":"upstream-task","status":"running","frames":57,"framespersecond":24,"resolution":"720p"}`,
		"trailing_object":    `{"id":"upstream-task","status":"succeeded","frames":57,"framespersecond":24,"resolution":"720p"}{"frames":289}`,
		"trailing_garbage":   `{"id":"upstream-task","status":"succeeded","frames":57,"framespersecond":24,"resolution":"720p"}broken`,
	} {
		t.Run(name, func(t *testing.T) {
			s, task, _, billing, logs := seedanceFrameRecoveryFixture(t, 8)
			task.RawResponse = []byte(body)
			s.finish(context.Background(), task)
			require.True(t, task.PricingMismatch)
			require.NotEmpty(t, task.ResponseMetadataError)
			require.Equal(t, "reconciliation", task.BillingStatus)
			require.Zero(t, billing.captures)
			require.Empty(t, billing.captureCommands)
			require.Zero(t, billing.releases)
			require.Empty(t, logs.logs)
		})
	}
	for name, mutate := range map[string]func(*VideoTaskRecord){
		"http_error":       func(task *VideoTaskRecord) { task.ResponseStatus = 502 },
		"missing_task_id":  func(task *VideoTaskRecord) { task.UpstreamTaskID = "" },
		"invalid_snapshot": func(task *VideoTaskRecord) { task.Target.CreatePath = "/unexpected" },
		"missing_quote":    func(task *VideoTaskRecord) { task.Quote = nil },
	} {
		t.Run(name, func(t *testing.T) {
			s, task, _, billing, _ := seedanceFrameRecoveryFixture(t, 8)
			mutate(task)
			s.finish(context.Background(), task)
			require.True(t, task.PricingMismatch)
			require.Equal(t, "reconciliation", task.BillingStatus)
			require.Empty(t, billing.captureCommands)
			require.Zero(t, billing.releases)
		})
	}
}

// 弱响应不能清除历史价格档位冲突；后续完整同任务用量可以恢复，仍只有一次捕获。
func TestVideoSeedanceFrameRecoveryRequiresDimensionsToClearMismatch(t *testing.T) {
	for _, previousError := range []string{"", "先前存在分辨率冲突"} {
		t.Run(previousError, func(t *testing.T) {
			s, task, _, billing, _ := seedanceFrameRecoveryFixture(t, 8)
			task.PricingMismatch, task.ResponseMetadataError = true, previousError
			task.RawResponse = []byte(`{"id":"upstream-task","status":"succeeded","frames":57,"framespersecond":24}`)
			s.finish(context.Background(), task)
			require.True(t, task.PricingMismatch)
			require.NotEmpty(t, task.ResponseMetadataError)
			require.Zero(t, billing.captures)
			task.RawResponse = []byte(`{"id":"upstream-task","status":"succeeded","frames":57,"framespersecond":24,"resolution":"720p"}`)
			s.finish(context.Background(), task)
			require.False(t, task.PricingMismatch)
			require.Equal(t, "settled", task.BillingStatus)
			require.Equal(t, 1, billing.captures)
		})
	}
}

// 旧原样、可美及已经结算/释放的任务不能被本次官方帧数修复重新解释或重复收费。
func TestVideoSeedanceFrameRecoveryDoesNotMigrateOtherContracts(t *testing.T) {
	t.Run("version_one", func(t *testing.T) {
		s, task, _, billing, _ := seedanceFrameRecoveryFixture(t, 8)
		task.Target.Version = 1
		task.Target.ProtocolAdapter = ""
		s.finish(context.Background(), task)
		require.Equal(t, float64(2), task.Metadata.DurationSeconds)
		require.Equal(t, float64(2), billing.captureCommands[0].ActualBaseAmountUSD)
	})
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterKemei} {
		t.Run(adapter, func(t *testing.T) {
			s, task, _, billing, _ := seedanceFrameRecoveryFixture(t, 8)
			task.Target.ProtocolAdapter = adapter
			s.finish(context.Background(), task)
			require.Equal(t, float64(2), task.Metadata.DurationSeconds)
			require.Equal(t, float64(2), billing.captureCommands[0].ActualBaseAmountUSD)
		})
	}
	for _, state := range []string{"settled", "released"} {
		t.Run(state, func(t *testing.T) {
			s, task, _, billing, _ := seedanceFrameRecoveryFixture(t, 8)
			task.BillingStatus = state
			task.BillingResult = &BatchImageBalanceHoldResult{ActualAmountUSD: 4, BalanceAmountUSD: 4}
			s.finish(context.Background(), task)
			require.Equal(t, float64(2), task.Metadata.DurationSeconds)
			require.Zero(t, billing.captures)
			require.Zero(t, billing.releases)
		})
	}
	for _, state := range []string{"failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			s, task, _, billing, _ := seedanceFrameRecoveryFixture(t, 8)
			task.Status = state
			task.RawResponse = []byte(`{"id":"upstream-task","status":"failed","frames":null}`)
			s.finish(context.Background(), task)
			s.finish(context.Background(), task)
			require.Equal(t, "released", task.BillingStatus)
			require.Zero(t, billing.captures)
			require.Equal(t, 1, billing.releases)
		})
	}
}
