//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 复现旧表单仅修改单个参数后的请求，缺省集合不应阻止映射模型保存。
func TestCreativeVideoPartialCapabilitiesUseEndpointDefaults(t *testing.T) {
	for _, tc := range []struct {
		endpoint   VideoEndpoint
		duration   float64
		resolution string
	}{
		{VideoEndpointCompat, 5, "720p"},
		{VideoEndpointOpenAIVideos, 4, "720p"},
		{VideoEndpointMiniMax, 6, "768p"},
		{VideoEndpointSeedance, 5, "720p"},
		{VideoEndpointVidu, 5, "720p"},
	} {
		t.Run(string(tc.endpoint), func(t *testing.T) {
			input := &CreativeVideoCapabilities{MaxReferenceImages: 0}
			got, err := normalizeCreativeVideoCapabilities(tc.endpoint, input)
			require.NoError(t, err)
			require.Equal(t, []float64{tc.duration}, got.Durations)
			require.Equal(t, []string{tc.resolution}, got.Resolutions)
			require.Equal(t, []string{"16:9", "9:16"}, got.AspectRatios)
			require.Zero(t, got.MaxReferenceImages)
			require.Equal(t, &CreativeVideoCapabilities{MaxReferenceImages: 0}, input)
		})
	}
	for name, input := range map[string]*CreativeVideoCapabilities{
		"only_duration":   {Durations: []float64{10, 5}},
		"only_resolution": {Resolutions: []string{"1080P", "2K"}},
		"only_ratio":      {AspectRatios: []string{"9:16"}},
		"only_references": {MaxReferenceImages: 1},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeCreativeVideoCapabilities(VideoEndpointOpenAIVideos, input)
			require.NoError(t, err)
			require.NotEmpty(t, got.Durations)
			require.NotEmpty(t, got.Resolutions)
			require.NotEmpty(t, got.AspectRatios)
			require.Equal(t, input.MaxReferenceImages, got.MaxReferenceImages)
			if len(input.Durations) > 0 {
				require.Equal(t, input.Durations, got.Durations)
			}
			if len(input.Resolutions) > 0 {
				require.Equal(t, []string{"1080p", "2k"}, got.Resolutions)
			}
			if len(input.AspectRatios) > 0 {
				require.Equal(t, input.AspectRatios, got.AspectRatios)
			}
		})
	}
}

// 补齐缺省值不能掩盖非法的已填写参数，错误须指出具体设置和字段。
func TestCreativeVideoPartialCapabilitiesRejectInvalidValues(t *testing.T) {
	for name, input := range map[string]*CreativeVideoCapabilities{
		"duration":                 {Durations: []float64{61}},
		"resolution":               {Resolutions: []string{"not-a-resolution"}},
		"ratio":                    {AspectRatios: []string{"0:1"}},
		"references":               {MaxReferenceImages: -1},
		"reference_endpoint_limit": {MaxReferenceImages: 2},
		"duplicate_resolution":     {Resolutions: []string{"720", "720p"}},
		"duplicate_duration":       {Durations: []float64{5, 5}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeCreativeModelSettings([]CreativeModelSetting{{GroupID: 3, Model: "minimax-h3-y", MediaType: "video", VideoEndpoint: VideoEndpointOpenAIVideos, Operations: []string{CreativeOperationGenerate}, VideoCapabilities: input}})
			require.Error(t, err)
			require.Contains(t, err.Error(), `group 3, model "minimax-h3-y"`)
		})
	}
}

// 映射别名应从候选到持久化、公开目录完整保留；参数默认值由选定端点提供。
func TestCreativeVideoMappedCandidateSettingsRoundTrip(t *testing.T) {
	s, e, upstream, billing, _ := creativeVideoTestFixture(t)
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.GroupIDs = []int64{3}
	account.Credentials["model_mapping"] = map[string]any{"minimax-h3-y": "minimax-h3"}
	account.Credentials["model_whitelist"] = []string{"minimax-h3"}
	account.Credentials["video_model_bindings"] = map[string]any{"minimax-h3": string(VideoEndpointOpenAIVideos)}
	s.public.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{3: {*account}}}
	candidates, err := s.public.ListCreativeModelCandidates(context.Background())
	require.NoError(t, err)
	// 最终白名单还允许直接请求上游名，候选可同时包含它和映射别名。
	var candidate CreativeModelCandidate
	for _, item := range candidates {
		if item.Model == "minimax-h3-y" {
			candidate = item
		}
	}
	require.Equal(t, "minimax-h3-y", candidate.Model)
	require.Equal(t, VideoEndpointOpenAIVideos, candidate.VideoEndpoint)
	raw, err := json.Marshal(candidate)
	require.NoError(t, err)
	require.Equal(t, float64(4), gjson.GetBytes(raw, "video_capabilities.durations.0").Float())
	require.Equal(t, "720p", gjson.GetBytes(raw, "video_capabilities.resolutions.0").String())

	// 管理员只将最多参考图改为零，其它空集合按原端点默认补齐。
	settings, err := s.public.NormalizeCreativeModelSettingsForSave(context.Background(), []CreativeModelSetting{{GroupID: 3, Model: candidate.Model, MediaType: "video", VideoEndpoint: candidate.VideoEndpoint, Operations: candidate.Operations, VideoCapabilities: &CreativeVideoCapabilities{MaxReferenceImages: 0}}})
	require.NoError(t, err)
	persisted, _, err := marshalCreativeModelSettings(settings)
	require.NoError(t, err)
	s.public.Settings.(*creativeFakeSettingReader).models = parseCreativeModelSettings(persisted)
	models, err := s.public.ListModels(context.Background(), e.UserID)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
	require.Equal(t, "minimax-h3-y", models.Data[0].Model)
	require.Equal(t, float64(4), models.Data[0].VideoCapabilities.DefaultDuration)
	require.Zero(t, models.Data[0].MaxReferenceImages)
	require.Zero(t, upstream.submitted)
	require.Zero(t, billing.reserves)
}
