//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 运行时能力扩展不回写原配置，并保留各端点的原默认值及参考素材上限。
func TestCreativeVideoCanvasExpandedCapabilitiesPreserveDefaults(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax, VideoEndpointVidu} {
		t.Run(string(endpoint), func(t *testing.T) {
			original, err := normalizeCreativeVideoCapabilities(endpoint, nil)
			require.NoError(t, err)
			before, err := json.Marshal(original)
			require.NoError(t, err)
			caps := creativeVideoCanvasCapabilities(original)
			require.True(t, caps.AllowCustomResolution)
			require.False(t, caps.AllowCustomAspectRatio)
			require.Len(t, caps.Durations, 60)
			require.Equal(t, float64(1), caps.Durations[0])
			require.Equal(t, float64(60), caps.Durations[59])
			require.Equal(t, original.Durations[0], caps.DefaultDuration)
			require.Equal(t, original.Resolutions[0], caps.DefaultResolution)
			require.Equal(t, original.AspectRatios[0], caps.DefaultAspectRatio)
			require.Equal(t, original.MaxReferenceImages, caps.MaxReferenceImages)
			require.Subset(t, caps.Resolutions, []string{"480p", "720p", "1080p"})
			require.Subset(t, caps.AspectRatios, []string{"1:1", "3:4", "4:3", "16:9", "9:16", "21:9", "adaptive"})
			after, err := json.Marshal(original)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
			require.NotContains(t, string(after), "allow_custom_")
		})
	}
	configured := &CreativeVideoCapabilities{Durations: []float64{10, 5}, Resolutions: []string{"2k", "768p", "1280x720"}, AspectRatios: []string{"9:21", "3:2"}, MaxReferenceImages: 0}
	caps := creativeVideoCanvasCapabilities(configured)
	require.Equal(t, float64(5), caps.DefaultDuration)
	require.Equal(t, "2k", caps.DefaultResolution)
	require.Equal(t, "9:21", caps.DefaultAspectRatio)
	require.Subset(t, caps.Resolutions, configured.Resolutions)
	require.Subset(t, caps.AspectRatios, configured.AspectRatios)

	s, e, _, _, _ := creativeVideoTestFixture(t)
	models, err := s.public.ListModels(context.Background(), e.UserID)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
	require.True(t, models.Data[0].VideoCapabilities.AllowCustomResolution)
	require.Equal(t, float64(5), models.Data[0].VideoCapabilities.DefaultDuration)
	require.Equal(t, "720p", models.Data[0].VideoCapabilities.DefaultResolution)
}

// 自定义参数保持原始含义；不同端点仅移动字段，不降档、不截短时长或改变比例。
func TestCreativeVideoCanvasCustomParametersKeepProtocolDimensions(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax, VideoEndpointVidu} {
		t.Run(string(endpoint), func(t *testing.T) {
			original, err := normalizeCreativeVideoCapabilities(endpoint, nil)
			require.NoError(t, err)
			model := &CreativeModelPublic{VideoEndpoint: endpoint, VideoCapabilities: creativeVideoCanvasCapabilities(original)}
			e := creativeVideoTestExecution()
			e.Params.Duration, e.Params.Resolution, e.Params.AspectRatio = 60, "768P", "4:3"
			_, canonical, _, err := creativeVideoCanvasRequest(e, model, nil, 8000)
			require.NoError(t, err)
			// 画布提交规范请求，内置协议转换后字段位置与原端点合同一致。
			body, err := videoBuiltinRequestBody(canonical, VideoUpstreamTarget{Version: 2, Endpoint: endpoint, ProtocolAdapter: VideoProtocolAdapterLegacy})
			require.NoError(t, err)
			resolutionKey, durationKey, ratioKey := "resolution", "duration", "ratio"
			if endpoint == VideoEndpointWan {
				resolutionKey, durationKey, ratioKey = "parameters.resolution", "parameters.duration", "parameters.ratio"
			}
			if endpoint == VideoEndpointKling || endpoint == VideoEndpointVidu {
				ratioKey = "aspect_ratio"
			}
			require.Equal(t, "768p", gjson.GetBytes(body, resolutionKey).String())
			require.Equal(t, float64(60), gjson.GetBytes(body, durationKey).Float())
			require.Equal(t, "4:3", gjson.GetBytes(body, ratioKey).String())
			require.Equal(t, "768P", e.Params.Resolution)
		})
	}
}

// 自定义范围只是画布准入，不能绕过参考图权限、类型、数量和其它参数的校验。
func TestCreativeVideoCanvasCustomParametersRejectInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CreativeCanvasExecution)
	}{
		{"resolution_zero", func(e *CreativeCanvasExecution) { e.Params.Resolution = "0p" }},
		{"resolution_overflow", func(e *CreativeCanvasExecution) { e.Params.Resolution = "65537p" }},
		{"resolution_decimal", func(e *CreativeCanvasExecution) { e.Params.Resolution = "768.5p" }},
		{"resolution_leading_zero", func(e *CreativeCanvasExecution) { e.Params.Resolution = "0768p" }},
		{"resolution_missing_unit", func(e *CreativeCanvasExecution) { e.Params.Resolution = "768" }},
		{"resolution_native_tier", func(e *CreativeCanvasExecution) { e.Params.Resolution = "4k" }},
		{"resolution_free_dimensions", func(e *CreativeCanvasExecution) { e.Params.Resolution = "1366x768" }},
		{"ratio_zero", func(e *CreativeCanvasExecution) { e.Params.AspectRatio = "0:1" }},
		{"ratio_decimal", func(e *CreativeCanvasExecution) { e.Params.AspectRatio = "1.5:1" }},
		{"ratio_overflow", func(e *CreativeCanvasExecution) { e.Params.AspectRatio = "1000:1" }},
		{"ratio_leading_zero", func(e *CreativeCanvasExecution) { e.Params.AspectRatio = "01:1" }},
		{"ratio_custom_removed", func(e *CreativeCanvasExecution) { e.Params.AspectRatio = "32:9" }},
		{"duration_zero", func(e *CreativeCanvasExecution) { e.Params.Duration = 0 }},
		{"duration_overflow", func(e *CreativeCanvasExecution) { e.Params.Duration = 61 }},
		{"duration_decimal", func(e *CreativeCanvasExecution) { e.Params.Duration = 4.5 }},
		{"missing_asset", func(e *CreativeCanvasExecution) {
			e.Params.References = []CreativeCanvasReference{{AssetID: "foreign"}}
		}},
		{"image_parameter", func(e *CreativeCanvasExecution) { e.Params.ImageSize = "4K" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e, upstream, billing, _ := creativeVideoTestFixture(t)
			e.Params.Duration, e.Params.Resolution, e.Params.AspectRatio = 60, "768p", "4:3"
			tc.mutate(e)
			_, err := s.Submit(context.Background(), e, nil)
			require.Error(t, err)
			require.Zero(t, upstream.submitted)
			require.Zero(t, billing.reserves)
		})
	}
	original, err := normalizeCreativeVideoCapabilities(VideoEndpointCompat, nil)
	require.NoError(t, err)
	caps := creativeVideoCanvasCapabilities(original)
	for _, value := range []string{"1p", "65536p", " 768P "} {
		_, valid := creativeVideoCanvasResolution(caps, value)
		require.True(t, valid, value)
	}
	for _, value := range []string{"4:3", "16:9", "adaptive"} {
		_, valid := creativeVideoCanvasAspectRatio(caps, value)
		require.True(t, valid, value)
	}
}

// 选择器替身读取实际桥接请求，使价卡测试不会误用旧夹具固定的 720p/8 秒。
type creativeCustomVideoUpstream struct {
	*videoLifecycleUpstream
	body []byte
}

func (u *creativeCustomVideoUpstream) SelectAccount(_ context.Context, _ *APIKey, request VideoTaskSubmitRequest) (*VideoUpstreamSelection, error) {
	u.body = append([]byte(nil), request.Body...)
	u.selection.Metadata.Resolution = gjson.GetBytes(request.Body, "resolution").String()
	u.selection.Metadata.DurationSeconds = gjson.GetBytes(request.Body, "duration").Float()
	return u.selection, nil
}

// 扩展参数继续使用原任务的精确档位报价及幂等账本，缺价时不得生成或预扣。
func TestCreativeVideoCanvasCustomPricingAndLifecycle(t *testing.T) {
	for _, terminal := range []string{"missing_price", "completed", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			s, e, upstream, billing, _ := creativeVideoTestFixture(t)
			e.Params.Duration, e.Params.Resolution, e.Params.AspectRatio = 60, "768p", "4:3"
			selector := &creativeCustomVideoUpstream{videoLifecycleUpstream: upstream}
			tasks := s.tasks.(*VideoTaskService)
			tasks.upstream = selector
			key := s.public.ApiKeyRepo.(*creativeFakeManagedKeyRepo).key
			if terminal != "missing_price" {
				price := 3.0
				key.Group.ModelPricing[0].VideoPrices = append(key.Group.ModelPricing[0].VideoPrices, VideoPriceTier{Resolution: "768p", Price: &price})
			}
			id, err := s.Submit(context.Background(), e, nil)
			require.Equal(t, "768p", gjson.GetBytes(selector.body, "resolution").String())
			// 画布提交统一规范请求，比例字段为 aspect_ratio，具体协议字段由账号绑定转换。
			require.Equal(t, "4:3", gjson.GetBytes(selector.body, "aspect_ratio").String())
			if terminal == "missing_price" {
				require.Error(t, err)
				require.Empty(t, id)
				require.Zero(t, upstream.submitted)
				require.Zero(t, billing.reserves)
				return
			}
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, "768p", task.Quote.Resolution)
			require.Equal(t, float64(60), task.Metadata.DurationSeconds)
			require.Equal(t, float64(360), task.Hold.HoldAmount)
			replayed, err := s.Submit(context.Background(), e, nil)
			require.NoError(t, err)
			require.Equal(t, id, replayed)
			require.Equal(t, 1, upstream.submitted)
			require.Equal(t, 1, billing.reserves)
			upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: terminal, Metadata: VideoRequestMetadata{DurationSeconds: 60, Resolution: "768p"}, VideoURL: "https://unused.example/video.mp4", Body: []byte(`{"status":"` + terminal + `"}`)}
			tasks.advance(context.Background(), task)
			fresh, err := s.repo.Get(context.Background(), id)
			require.NoError(t, err)
			tasks.advance(context.Background(), fresh)
			if terminal == "completed" {
				require.Equal(t, "settled", fresh.BillingStatus)
				require.Equal(t, float64(360), fresh.BillingResult.ActualAmountUSD)
				require.Equal(t, 1, billing.captures)
				require.Zero(t, billing.releases)
			} else {
				require.Equal(t, "released", fresh.BillingStatus)
				require.Equal(t, 1, billing.releases)
				require.Zero(t, billing.captures)
			}
		})
	}
}
