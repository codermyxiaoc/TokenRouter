package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 固定秒价只确定预扣金额，真实 Token 费用仍使用冻结倍率，覆盖退差和补扣两种方向。
func TestVideoTaskTokenPrepayFixedDepositAndActualSettlement(t *testing.T) {
	for _, rate := range []float64{0, .38, 3} {
		for _, tokenCount := range []int64{80_770, 5_000_000} {
			s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
			key.Group.VideoRateIndependent = true
			key.Group.VideoRateMultiplier = rate
			enableVideoLifecycleImagePricing(key, upstream, 7, 5, .15)
			enableVideoLifecycleTokenPrepay(key, .3)
			response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","duration":8}`)})
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			require.True(t, task.Hold.VideoTokenPrepay)
			require.False(t, task.Hold.VideoDeferredBilling)
			require.InDelta(t, 2.70, task.Hold.HoldAmount, 1e-9)
			require.InDelta(t, 2.40, task.Hold.BaseAmountUSD, 1e-9)
			require.Equal(t, 8.0, task.Hold.VideoPrepayDurationSeconds)
			require.Equal(t, rate, task.Hold.BalanceRateMultiplier)
			// 后续修改配置或上游实际时长不能改变原预扣事实。
			*key.Group.ModelPricing[0].VideoTokenPrepay.PricePerSecond = 99
			key.Group.VideoRateMultiplier = 99
			upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`), Metadata: VideoRequestMetadata{Tokens: &tokenCount, DurationSeconds: 10}}
			restarted := *s
			restarted.advance(context.Background(), task)
			require.Equal(t, "settled", task.BillingStatus)
			require.InDelta(t, float64(tokenCount)/1e6*rate+.30, task.BillingResult.ActualAmountUSD, 1e-9)
			restarted.advance(context.Background(), task)
			require.Equal(t, 1, billing.captures)
			require.Len(t, logs.logs, 1)
			media := NewMediaTaskService(&videoDeferredMediaRepo{record: task})
			projected := &MediaTask{Source: "video"}
			media.enrichVideoBilling(context.Background(), projected)
			require.True(t, projected.VideoBilling.TokenPrepay)
			require.Equal(t, .3, *projected.VideoBilling.PrepayPricePerSecond)
			require.Equal(t, 8.0, projected.VideoBilling.PrepayDurationSeconds)
			require.Equal(t, 10.0, projected.VideoBilling.DurationSeconds)
		}
	}
}

// 自动时长只可使用明确的账号预留上限；不把未知时长默认为免费。
func TestVideoTaskTokenPrepayAutomaticDurationRequiresKnownBudget(t *testing.T) {
	for _, duration := range []float64{0, 12} {
		s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoToken)
		enableVideoLifecycleTokenPrepay(key, .3)
		upstream.selection.Metadata.DurationSeconds = -1
		upstream.selection.Account.Credentials["video_max_duration_seconds"] = duration
		response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"duration":-1}`)})
		if duration == 0 {
			require.ErrorIs(t, err, ErrVideoTaskBudget)
			require.Zero(t, billing.reserves)
			require.Zero(t, upstream.submitted)
			continue
		}
		require.NoError(t, err)
		task, err := s.repo.Get(context.Background(), response.LocalTaskID)
		require.NoError(t, err)
		require.InDelta(t, 3.6, task.Hold.HoldAmount, 1e-9)
		require.Equal(t, -1.0, task.Metadata.DurationSeconds, "预留上限不能改写生成参数")
		require.Equal(t, 12.0, task.Hold.VideoPrepayDurationSeconds)
	}
}
