package service

import (
	"context"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 统一入口只按模型绑定选择协议：精确模型优先，其次 * 默认；未绑定的模型在转发与智能路由中同时拒绝。
func TestVideoModelProtocolBindingSelectionAndSmartEligibility(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointCompat)
	account.Credentials = map[string]any{"api_key": "fixture-secret", "base_url": "https://relay.example/prefix",
		"model_mapping": map[string]any{"alias": "actual", "other-alias": "other"},
		"video_model_protocols": map[string]any{
			"actual": map[string]any{"protocol": "seedance.seedance_v1", "base_url": "https://ark.example/api/v3"},
			"*":      map[string]any{"protocol": "openai_videos.openai_v1"},
		}}
	require.NoError(t, normalizeVideoCredentials(account))
	svc := NewVideoUpstreamService(&OpenAIGatewayService{})
	target, err := svc.resolveTarget(context.Background(), account, "actual")
	require.NoError(t, err)
	require.Equal(t, VideoEndpointSeedance, target.Endpoint)
	require.Equal(t, "https://ark.example/api/v3", target.BaseURL)
	require.Equal(t, 2, target.Version)
	fallback, err := svc.resolveTarget(context.Background(), account, "other")
	require.NoError(t, err)
	require.Equal(t, VideoEndpointOpenAIVideos, fallback.Endpoint)
	require.True(t, smartRoutingAccountEndpointEligible(context.Background(), account, "alias", "/v1/videos"))
	// 旧原生与旧统一入口已下线，即使模型已绑定也不能被智能路由选中。
	for _, retired := range []string{"/v1/video/generations", "/api/v3/contents/generations/tasks"} {
		require.False(t, smartRoutingAccountEndpointEligible(context.Background(), account, "alias", retired))
	}
	delete(account.Credentials["video_model_protocols"].(map[string]any), "*")
	_, err = svc.resolveTarget(context.Background(), account, "other")
	require.Equal(t, "VIDEO_ENDPOINT_MODEL_MISMATCH", infraerrors.Reason(err))
	require.False(t, smartRoutingAccountEndpointEligible(context.Background(), account, "other-alias", "/v1/videos"))
}

// 旧端点配置迁移为模型协议：多端点按 openai_videos、compat、首个原生端点的顺序选择，并清理旧字段。
func TestVideoLegacyEndpointConfigurationMigratesToModelProtocols(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled []string
		binding any
		want    string
	}{
		// 原样协议在原生端点换为官方协议，在 compat 与 openai_videos 换为通用扁平格式模板。
		{"native binding keeps native", []string{"compat", "seedance"}, "seedance", "seedance"},
		{"array prefers compat", []string{"seedance", "compat"}, []string{"seedance", "compat"}, "profile:generic-flat"},
		{"unbound prefers plural", []string{"compat", "openai_videos"}, nil, "profile:generic-flat-videos"},
		{"unbound single native", []string{"wan"}, nil, "wan"},
		{"unbound first native", []string{"seedance", "wan"}, nil, "seedance"},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			account.Credentials["video_endpoints"] = test.enabled
			if test.binding != nil {
				account.Credentials["video_model_bindings"] = map[string]any{"actual": test.binding}
			}
			require.NoError(t, normalizeVideoCredentials(account))
			for _, key := range videoLegacyCredentialKeys {
				require.NotContains(t, account.Credentials, key)
			}
			cfg, err := account.VideoConfiguration()
			require.NoError(t, err)
			binding, ok := cfg.Protocol("actual")
			require.True(t, ok)
			require.Equal(t, test.want, videoProtocolLabel(binding))
		})
	}
}

func TestVideoAdaptiveBindingsRejectMalformedSets(t *testing.T) {
	for _, binding := range []any{"", "unknown", []string{}, []string{"compat", "compat"}, []string{"compat", "unknown"}, []string{"compat", "wan"}, []any{"compat", nil}, 42, true, nil, map[string]string{"protocol": "compat"}} {
		account := videoFixtureAccount(VideoEndpointCompat)
		account.Credentials["video_endpoints"] = []string{"compat", "seedance"}
		account.Credentials["video_model_bindings"] = map[string]any{"m": binding}
		require.Error(t, normalizeVideoCredentials(account), "binding=%#v", binding)
	}
	for _, protocols := range []map[string]any{
		{"m": map[string]any{"protocol": "compat"}},
		{"m": map[string]any{"protocol": "seedance.kling_v1"}},
		{"m": map[string]any{"protocol": "kling.kling_v1"}},
		{"m": map[string]any{"protocol": "compat.legacy_v1", "path": "/text-to-video/{model}"}},
		{"m*": map[string]any{"protocol": "compat.legacy_v1"}},
		{"m": map[string]any{"protocol": "profile:Bad Key"}},
		{"m": map[string]any{"protocol": "compat.legacy_v1", "base_url": "https://user:pass@example.com"}},
		{},
	} {
		account := videoFixtureAccount(VideoEndpointCompat)
		account.Credentials = map[string]any{"api_key": "k", "base_url": "https://relay.example", "video_model_protocols": protocols}
		require.Error(t, normalizeVideoCredentials(account), "protocols=%#v", protocols)
	}
	account := videoFixtureAccount(VideoEndpointCompat)
	account.Credentials["video_endpoints"] = []string{"compat", "seedance"}
	account.Credentials["video_model_bindings"] = map[string]any{"old": "seedance", "new": []string{"seedance", "compat"}}
	require.NoError(t, normalizeVideoCredentials(account))
	cfg, err := account.VideoConfiguration()
	require.NoError(t, err)
	old, _ := cfg.Protocol("old")
	require.Equal(t, VideoEndpointSeedance, old.Endpoint, "旧单字符串绑定迁移后不能扩张成其它端点")
	newer, _ := cfg.Protocol("new")
	require.Equal(t, "profile:generic-flat", videoProtocolLabel(newer))
}

// 创建后冻结的协议、地址和路径不随账号编辑改变；统一请求按冻结协议转换后才发送。
func TestVideoAdaptiveSameModelUsesInboundEndpointAndFrozenTarget(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointCompat)
	account.Credentials = map[string]any{"api_key": "fixture-secret", "base_url": "https://relay.example/prefix",
		"video_model_protocols": map[string]any{"m": map[string]any{"protocol": "seedance.legacy_v1", "base_url": "https://ark.example/api/v3"}}}
	fixture := &videoHTTPFixture{status: 202, response: `{"id":"owned","status":"queued"}`}
	repo := &videoSelectionFixture{accounts: []Account{*account}}
	svc := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: repo, concurrencyService: NewConcurrencyService(nil), httpUpstream: fixture})
	groupID := int64(5)
	key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformVideo, Status: StatusActive}}
	body := []byte(`{"model":"m","prompt":"waves","resolution":"720p","duration":5}`)
	canonical, err := ParseVideoCanonicalRequest(body)
	require.NoError(t, err)
	selected, err := svc.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{Body: body, Canonical: canonical, InboundProtocol: "openai_videos"})
	require.NoError(t, err)
	defer selected.Release()
	selected.Account.Credentials["video_model_protocols"] = map[string]any{"m": map[string]any{"protocol": "wan.wan_v1", "base_url": "https://changed.example"}}
	_, err = svc.Submit(context.Background(), selected)
	require.NoError(t, err)
	require.Equal(t, "https://ark.example/api/v3/contents/generations/tasks", fixture.request.URL.String())
	require.Equal(t, "waves", gjson.GetBytes(fixture.requestBody, "content.0.text").String())
	require.Equal(t, 1, fixture.calls)
}

// 同一份统一请求在不同协议账号上转换为各自端点格式，计价维度取自真正发送的请求体。
func TestVideoAdaptiveSelectedProtocolControlsMetadata(t *testing.T) {
	body := []byte(`{"model":"m","prompt":"x","resolution":"1080p","duration":10,"images":[{"url":"https://asset.example/image.png"}]}`)
	canonical, err := ParseVideoCanonicalRequest(body)
	require.NoError(t, err)
	for _, protocol := range []string{"wan.wan_v1", "seedance.seedance_v1"} {
		account := videoFixtureAccount(VideoEndpointWan)
		account.Credentials = map[string]any{"api_key": "fixture-secret", "base_url": "https://relay.example/prefix",
			"video_model_protocols": map[string]any{"m": map[string]any{"protocol": protocol}}}
		repo := &videoSelectionFixture{accounts: []Account{*account}}
		svc := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: repo, concurrencyService: NewConcurrencyService(nil)})
		groupID := int64(5)
		key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformVideo, Status: StatusActive}}
		selected, err := svc.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{Body: body, Canonical: canonical, InboundProtocol: "openai_videos"})
		require.NoError(t, err)
		// Wan 原生请求体使用大写分辨率（1080P），计费按大小写无关识别，这里只断言档位一致。
		resolution, ok := LookupVideoBillingResolution(selected.Metadata.Resolution)
		require.True(t, ok)
		require.Equal(t, VideoBillingResolution1080P, resolution)
		require.Equal(t, float64(10), selected.Metadata.DurationSeconds)
		require.NotNil(t, selected.Metadata.ReferenceImageCount)
		require.Equal(t, 1, *selected.Metadata.ReferenceImageCount)
		if protocol == "wan.wan_v1" {
			require.Equal(t, "https://asset.example/image.png", gjson.GetBytes(selected.Body, "input.img_url").String())
		} else {
			require.Equal(t, "image_url", gjson.GetBytes(selected.Body, "content.1.type").String())
		}
		selected.Release()
	}
}

// 创建与编辑可保存空的可选预算，清空后不能恢复旧值或顺带删除密钥。
func TestVideoOptionalBudgetNullCreatesAndClearsThroughCredentialMerge(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointCompat)
	account.Credentials["video_max_duration_seconds"] = nil
	account.Credentials["video_max_output_tokens"] = nil
	require.NoError(t, normalizeCNProviderCredentials(account, true))
	account.Credentials["video_max_duration_seconds"] = 30
	account.Credentials["video_max_output_tokens"] = 1000000
	incoming := map[string]any{
		"base_url": account.GetCredential("base_url"), "video_endpoints": []string{"compat", "seedance"},
		"video_max_duration_seconds": nil, "video_max_output_tokens": nil,
	}
	account.Credentials = MergePreservingSensitiveCreds(account.Credentials, incoming)
	require.NoError(t, normalizeCNProviderCredentials(account, false))
	require.Equal(t, "fixture-secret", account.GetCredential("api_key"))
	require.NotContains(t, account.Credentials, "video_max_output_tokens", "保存账号移除旧 Token 预算字段")
	for _, field := range []string{"video_max_duration_seconds"} {
		value, present := account.Credentials[field]
		require.True(t, present)
		require.Nil(t, value)
		require.Zero(t, videoCredentialNumber(account, field), "清空后不沿用旧上限")
		for _, invalid := range []any{0, -1, "not-a-number", false} {
			account.Credentials[field] = invalid
			require.Error(t, normalizeVideoCredentials(account))
			account.Credentials[field] = nil
		}
	}
	account.Credentials["video_max_pending_tasks"] = nil
	require.Error(t, normalizeVideoCredentials(account))
}

func TestVideoOptionalNullDurationBudgetStillBlocksAutomaticDuration(t *testing.T) {
	s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideo)
	upstream.selection.Account.Credentials["video_max_output_tokens"] = nil
	upstream.selection.Account.Credentials["video_max_duration_seconds"] = nil
	upstream.selection.Metadata.DurationSeconds = -1
	_, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","resolution":"720p","duration":-1}`)})
	require.ErrorIs(t, err, ErrVideoTaskBudget)
	require.Zero(t, billing.reserves)
	require.Zero(t, upstream.submitted)
}

// videoProtocolLabel 把解析后的绑定显示为端点名或 profile:键名，便于断言迁移结果。
func videoProtocolLabel(p videoResolvedProtocol) string {
	if p.IsProfile() {
		return videoProfileProtocolPrefix + p.ProfileKey
	}
	return string(p.Endpoint)
}
