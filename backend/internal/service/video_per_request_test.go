package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 按次价沿用视频矩阵和倍率，但固定图片费用不得随视频倍率变化。
func TestVideoPerRequestQuoteMatrixFixedImagesAndFrozenPrice(t *testing.T) {
	price, imagePrice, multiplier, count := 2.0, .15, 3.0, 7
	group := &Group{ID: 12, Platform: PlatformVideo, VideoRateIndependent: true, VideoRateMultiplier: .25,
		ModelPricing: []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideoPerRequest,
			PriceMultiplier: &multiplier, VideoPrices: []VideoPriceTier{{Resolution: "768p", Price: &price}},
			VideoImageInputPricing: &VideoImageInputPricing{FreeImages: 5, Price: &imagePrice}}}}
	r := videoPricingTestResolver(group, nil)
	input := VideoPriceInput{Group: group, Model: "video-model", Resolution: "768P", ReferenceImageCount: &count, RateMultiplier: 99}
	quote, err := r.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "request", quote.Unit)
	require.Equal(t, BillingModeVideoPerRequest, quote.Mode)
	require.Equal(t, PricingSourceGroup, quote.Source)
	for _, seconds := range []float64{0, -1, 8, 100} {
		cost, err := quote.Calculate(seconds, nil)
		require.NoError(t, err)
		require.Equal(t, 6.0, cost.OutputCost)
		require.InDelta(t, .3, cost.ImageInputCost, 1e-12)
		require.InDelta(t, 6.3, cost.TotalCost, 1e-12)
		require.InDelta(t, 1.8, cost.ActualCost, 1e-12)
	}
	// 重启后仍使用创建时的单价、图片计数与倍率，不追随管理员后续改价。
	raw, err := json.Marshal(quote)
	require.NoError(t, err)
	price, imagePrice, multiplier, count = 90, 10, 8, 100
	var restored VideoPriceQuote
	require.NoError(t, json.Unmarshal(raw, &restored))
	tokens := int64(5_000_000)
	cost, err := restored.Calculate(60, &tokens)
	require.NoError(t, err)
	require.InDelta(t, 1.8, cost.ActualCost, 1e-12)
}

// 有分辨率矩阵时不能省略档位绕过高价；只有统一兜底按次价允许未知分辨率。
func TestVideoPerRequestFallbackAndMissingPriceFailClosed(t *testing.T) {
	price, fallback := 3.0, 1.0
	group := &Group{ID: 12, Platform: PlatformVideo, ModelPricing: []ChannelModelPricing{{Platform: PlatformVideo,
		Models: []string{"video-model"}, BillingMode: BillingModeVideoPerRequest, VideoFallbackPrice: &fallback}}}
	r := videoPricingTestResolver(group, nil)
	input := VideoPriceInput{Group: group, Model: "video-model", RateMultiplier: 1}
	quote, err := r.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.True(t, quote.FallbackUsed)
	require.Empty(t, quote.Resolution)
	require.Equal(t, "request", quote.Unit)
	group.ModelPricing[0].VideoPrices = []VideoPriceTier{{Resolution: "1080p", Price: &price}}
	_, err = r.QuoteVideo(context.Background(), input)
	require.ErrorIs(t, err, ErrVideoPricingUnavailable)
	input.Resolution = "1080p"
	quote, err = r.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, price, quote.UnitPrice)
	input.HasReferenceVideo = true
	quote, err = r.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, price, quote.UnitPrice, "参考视频输入不能把已配置分辨率改成较低的兜底价")
	require.False(t, quote.FallbackUsed)
	input.Resolution = "480p"
	quote, err = r.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, fallback, quote.UnitPrice)
	group.ModelPricing[0].VideoFallbackPrice = nil
	_, err = r.QuoteVideo(context.Background(), input)
	require.ErrorIs(t, err, ErrVideoPricingUnavailable)
	group.ModelPricing[0].VideoPrices = nil
	group.ModelPricing[0].PerRequestPrice = &price
	_, err = r.QuoteVideo(context.Background(), input)
	require.ErrorIs(t, err, ErrVideoPricingUnavailable, "按次视频不得借旧平铺秒价报价")
	group.ModelPricing[0].VideoFallbackPrice = new(float64)
	quote, err = r.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	cost, err := quote.Calculate(0, nil)
	require.NoError(t, err)
	require.Zero(t, cost.ActualCost)
}

// 明确选择按次不能降级秒价，按次价也不能被 Token 或按秒入口误用。
func TestVideoPerRequestUnitIsolation(t *testing.T) {
	price, second := 2.0, .3
	pricing := &ChannelModelPricing{Platform: PlatformVideo, BillingMode: BillingModeVideoPerRequest, VideoFallbackPrice: &price}
	group := &Group{ID: 12, Platform: PlatformVideo, VideoPrice720P: &second}
	r := videoPricingTestResolver(group, pricing)
	input := VideoPriceInput{Group: group, Model: "video-model", Resolution: "720p", RateMultiplier: 1, Mode: BillingModeVideoPerRequest}
	quote, err := r.QuoteVideo(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, BillingModeVideoPerRequest, quote.Mode)
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken} {
		pricing.BillingMode = mode
		_, err := r.QuoteVideo(context.Background(), input)
		require.ErrorIs(t, err, ErrVideoPricingUnavailable)
	}
}

// 完成即计一次，缺时长、缺 Token、自动时长、重复轮询均不改变扣费。
func TestVideoPerRequestLifecycleNoUsageRequiredSettlesOnce(t *testing.T) {
	for _, duration := range []float64{0, -1} {
		s, key, u, b, logs := newVideoLifecycleFixture(BillingModeVideoPerRequest)
		count, imagePrice := 7, .15
		key.Group.ModelPricing[0].VideoImageInputPricing = &VideoImageInputPricing{FreeImages: 5, Price: &imagePrice}
		u.selection.Metadata.DurationSeconds = duration
		u.selection.Metadata.ReferenceImageCount = &count
		request := VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model"}`), IdempotencyKey: "per-request"}
		response, err := s.Submit(context.Background(), key, request)
		require.NoError(t, err)
		task, err := s.repo.Get(context.Background(), response.LocalTaskID)
		require.NoError(t, err)
		require.InDelta(t, 2.3, task.Hold.HoldAmount, 1e-12)
		require.False(t, task.Hold.VideoDeferredBilling)
		require.False(t, task.Hold.VideoTokenPrepay)
		u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`)}
		s.advance(context.Background(), task)
		require.Equal(t, "settled", task.BillingStatus)
		require.InDelta(t, 2.3, task.BillingResult.ActualAmountUSD, 1e-12)
		require.Equal(t, 1, b.captures)
		require.Len(t, logs.logs, 1)
		log := logs.logs["video_capture:"+task.ID]
		require.Equal(t, string(BillingModeVideoPerRequest), *log.BillingMode)
		require.Equal(t, 1, log.VideoCount)
		details := BuildUsageVideoBillingDetails(log, task.Quote, task.Metadata)
		require.NotNil(t, details)
		require.Equal(t, "request", details.Unit)
		require.Zero(t, details.DurationSeconds)
		require.Equal(t, 2, *details.BillableReferenceImageCount)
		_, err = s.Submit(context.Background(), key, request)
		require.NoError(t, err)
		s.advance(context.Background(), task)
		require.Equal(t, 1, b.captures)
		require.Equal(t, 1, u.submitted)
		require.Equal(t, 1, u.polls)
	}
}

// 统一按次价无需分辨率，返回的实际分辨率仅补展示；计价快照保持创建时原样。
func TestVideoPerRequestFallbackAcceptsReturnedResolution(t *testing.T) {
	s, key, u, b, logs := newVideoLifecycleFixture(BillingModeVideoPerRequest)
	price := 1.25
	key.Group.ModelPricing[0].VideoPrices = nil
	key.Group.ModelPricing[0].VideoFallbackPrice = &price
	u.selection.Metadata.DurationSeconds, u.selection.Metadata.Resolution = 0, ""
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	price = 100
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Resolution: "1080P"}, Body: []byte(`{"status":"completed"}`)}
	s.advance(context.Background(), task)
	require.Equal(t, "settled", task.BillingStatus)
	require.Empty(t, task.Quote.Resolution)
	require.Equal(t, "1080p", task.Metadata.Resolution)
	require.Equal(t, 1, b.captures)
	require.Equal(t, 2.5, task.BillingResult.ActualAmountUSD)
	details := BuildUsageVideoBillingDetails(logs.logs["video_capture:"+task.ID], task.Quote, task.Metadata)
	require.Equal(t, "1080p", details.Resolution)
}

// 失败与确认取消释放完整一次费用；HTTP 查询异常不会把仍在运行的任务免费释放。
func TestVideoPerRequestFailureAndCancellationReleaseOnlyOnce(t *testing.T) {
	for _, terminal := range []string{"failed", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			s, key, u, b, logs := newVideoLifecycleFixture(BillingModeVideoPerRequest)
			response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			u.poll = &VideoUpstreamResponse{StatusCode: http.StatusServiceUnavailable}
			s.advance(context.Background(), task)
			require.Zero(t, b.releases)
			if terminal == "cancelled" {
				u.cancel = &VideoUpstreamResponse{StatusCode: 200, Status: "processing"}
				_, err = s.Cancel(context.Background(), key, task.ID, "compat")
				require.NoError(t, err)
				require.Zero(t, b.releases)
				u.cancel.Status = terminal
				_, err = s.Cancel(context.Background(), key, task.ID, "compat")
				require.NoError(t, err)
				task, err = s.repo.Get(context.Background(), task.ID)
				require.NoError(t, err)
			} else {
				u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: terminal}
				s.advance(context.Background(), task)
			}
			require.Equal(t, "released", task.BillingStatus)
			s.advance(context.Background(), task)
			require.Equal(t, 1, b.releases)
			require.Zero(t, b.captures)
			require.Empty(t, logs.logs)
		})
	}
}

func TestVideoPerRequestUsageLogRecoveryDoesNotRecharge(t *testing.T) {
	s, key, u, b, logs := newVideoLifecycleFixture(BillingModeVideoPerRequest)
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed"}
	logs.err = errors.New("temporary database failure")
	s.advance(context.Background(), task)
	require.Equal(t, 1, b.captures)
	require.False(t, task.EffectsDone)
	logs.err = nil
	s.advance(context.Background(), task)
	require.Equal(t, 1, b.captures)
	require.True(t, task.EffectsDone)
	require.Len(t, logs.logs, 1)
}

func TestVideoPerRequestMissingPricingBlocksPaidSubmission(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
	key.Group.ModelPricing[0].VideoPrices = nil
	_, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.Error(t, err)
	require.Zero(t, b.reserves)
	require.Zero(t, u.submitted)
}

// 有条件的按次价不因“已完成”而跳过档位核验，未知受理也不得提前退款。
func TestVideoPerRequestMismatchAndUnknownSubmissionRetainHold(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoPerRequest)
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Resolution: "1080p"}}
	s.advance(context.Background(), task)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.True(t, task.PricingMismatch)
	require.Zero(t, b.captures)
	require.Zero(t, b.releases)

	s, key, u, b, _ = newVideoLifecycleFixture(BillingModeVideoPerRequest)
	u.submitErr = errors.New("connection lost after upstream accepted the request")
	response, err = s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err = s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	s.advance(context.Background(), task)
	require.Equal(t, "submission_unknown", task.Status)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Equal(t, 1, u.submitted)
	require.Zero(t, u.polls)
	require.Zero(t, b.captures)
	require.Zero(t, b.releases)
}
