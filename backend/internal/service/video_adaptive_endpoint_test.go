package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 旧单字符串、显式多端点和未绑定继承都必须在转发与智能路由中采用同一个决定。
func TestVideoAdaptiveEndpointSelectionAndSmartEligibility(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled []string
		binding any
		path    string
		want    VideoEndpoint
		reason  string
	}{
		{"legacy native binding rejects compat", []string{"compat", "seedance"}, "seedance", "/v1/video/generations", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"legacy native binding keeps native", []string{"compat", "seedance"}, "seedance", "/api/v3/contents/generations/tasks", VideoEndpointSeedance, ""},
		{"legacy string rejects other native", []string{"compat", "seedance"}, "compat", "/api/v3/contents/generations/tasks", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"array chooses compat regardless of order", []string{"seedance", "compat"}, []string{"seedance", "compat"}, "/v1/video/generations", VideoEndpointCompat, ""},
		{"array chooses requested native", []string{"seedance", "compat"}, []string{"seedance", "compat"}, "/api/v3/contents/generations/tasks", VideoEndpointSeedance, ""},
		{"unbound inherits compat", []string{"seedance", "compat"}, nil, "/v1/video/generations", VideoEndpointCompat, ""},
		{"unbound inherits native", []string{"seedance", "compat"}, nil, "/api/v3/contents/generations/tasks", VideoEndpointSeedance, ""},
		{"single native cannot enable compat", []string{"wan"}, nil, "/v1/video/generations", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"single native cannot enable plural", []string{"wan"}, nil, "/v1/videos", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"unbound multiple natives reject compat", []string{"seedance", "wan"}, nil, "/v1/video/generations", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"bound multiple natives reject compat", []string{"compat", "seedance", "wan"}, []string{"seedance", "wan"}, "/v1/video/generations", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"compat does not enable plural", []string{"compat"}, nil, "/v1/videos", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"plural does not enable compat", []string{"openai_videos"}, nil, "/v1/video/generations", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"plural explicitly enabled", []string{"openai_videos"}, nil, "/v1/videos", VideoEndpointOpenAIVideos, ""},
		{"both OpenAI endpoints select compat", []string{"openai_videos", "compat"}, nil, "/v1/video/generations", VideoEndpointCompat, ""},
		{"both OpenAI endpoints select plural", []string{"compat", "openai_videos"}, nil, "/v1/videos", VideoEndpointOpenAIVideos, ""},
		{"compat binding excludes plural", []string{"compat", "openai_videos"}, "compat", "/v1/videos", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"plural binding excludes compat", []string{"compat", "openai_videos"}, "openai_videos", "/v1/video/generations", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"native binding excludes enabled plural", []string{"openai_videos", "seedance"}, "seedance", "/v1/videos", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
		{"native chooses among multiple natives", []string{"seedance", "wan"}, []string{"seedance", "wan"}, "/api/v1/services/aigc/video-generation/video-synthesis", VideoEndpointWan, ""},
		{"model binding narrows enabled", []string{"compat", "seedance", "wan"}, []string{"compat", "wan"}, "/api/v3/contents/generations/tasks", "", "VIDEO_ENDPOINT_MODEL_MISMATCH"},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			account.Credentials["video_endpoints"] = test.enabled
			account.Credentials["model_mapping"] = map[string]any{"alias": "actual"}
			if test.binding != nil {
				account.Credentials["video_model_bindings"] = map[string]any{"actual": test.binding}
			}
			require.NoError(t, normalizeVideoCredentials(account))
			cfg, err := account.VideoConfiguration()
			require.NoError(t, err)
			route, ok := MatchVideoGatewayRoute(http.MethodPost, test.path)
			require.True(t, ok)
			endpoint, err := cfg.SelectEndpoint("actual", route.Protocol, route.Native)
			if test.reason != "" {
				require.Equal(t, test.reason, infraerrors.Reason(err))
				require.False(t, smartRoutingAccountEndpointEligible(context.Background(), account, "alias", test.path))
				// 真实目标解析与智能路由必须同时拒绝，不能只隐藏展示或调度入口。
				_, targetErr := NewVideoUpstreamService(&OpenAIGatewayService{}).resolveTarget(account, "actual", VideoTaskSubmitRequest{InboundProtocol: route.Protocol, Native: route.Native, NativePath: route.PathTemplate})
				require.Equal(t, test.reason, infraerrors.Reason(targetErr))
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, endpoint)
			require.True(t, smartRoutingAccountEndpointEligible(context.Background(), account, "alias", test.path))
			target, err := NewVideoUpstreamService(&OpenAIGatewayService{}).resolveTarget(account, "actual", VideoTaskSubmitRequest{InboundProtocol: route.Protocol, Native: route.Native, NativePath: route.PathTemplate})
			require.NoError(t, err)
			require.Equal(t, endpoint, target.Endpoint)
		})
	}
}

// 内部调用省略协议时仅等同旧统一入口，不能恢复唯一原生或另一个 OpenAI 端点的隐式兼容。
func TestVideoAdaptiveEmptyProtocolRequiresExplicitCompat(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		endpoints []VideoEndpoint
		binding   VideoEndpointSet
		allowed   bool
	}{
		{"explicit compat", []VideoEndpoint{VideoEndpointCompat}, nil, true},
		{"only plural", []VideoEndpoint{VideoEndpointOpenAIVideos}, nil, false},
		{"only native", []VideoEndpoint{VideoEndpointSeedance}, nil, false},
		{"binding keeps compat", []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos}, VideoEndpointSet{VideoEndpointCompat}, true},
		{"binding excludes compat", []VideoEndpoint{VideoEndpointCompat, VideoEndpointSeedance}, VideoEndpointSet{VideoEndpointSeedance}, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			cfg := VideoAccountConfiguration{Endpoints: fixture.endpoints}
			if fixture.binding != nil {
				cfg.ModelBindings = map[string]VideoEndpointSet{"m": fixture.binding}
			}
			got, err := cfg.SelectEndpoint("m", "", false)
			if !fixture.allowed {
				require.Equal(t, "VIDEO_ENDPOINT_MODEL_MISMATCH", infraerrors.Reason(err))
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, VideoEndpointCompat, got)
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
	account := videoFixtureAccount(VideoEndpointCompat)
	account.Credentials["video_endpoints"] = []string{"compat", "seedance"}
	account.Credentials["video_model_bindings"] = map[string]any{"old": "seedance", "new": []string{"seedance", "compat"}}
	require.NoError(t, normalizeVideoCredentials(account))
	cfg, err := account.VideoConfiguration()
	require.NoError(t, err)
	require.Equal(t, VideoEndpointSet{VideoEndpointSeedance}, cfg.ModelBindings["old"])
	require.Equal(t, "seedance", account.Credentials["video_model_bindings"].(map[string]any)["old"], "读取不能把旧单字符串扩张成账号全部端点")
}

// 同一最终模型根据入站路径选择不同上游URL，正文不转换，创建后冻结的端点不随账号编辑改变。
func TestVideoAdaptiveSameModelUsesInboundEndpointAndFrozenTarget(t *testing.T) {
	for _, native := range []bool{false, true} {
		account := videoFixtureAccount(VideoEndpointCompat)
		account.Credentials["video_endpoints"] = []string{"compat", "seedance"}
		account.Credentials["video_model_bindings"] = map[string]any{"m": []string{"compat", "seedance"}}
		account.Credentials["video_base_urls"] = map[string]string{"compat": "https://compat.example/v1", "seedance": "https://ark.example/api/v3"}
		fixture := &videoHTTPFixture{status: 202, response: `{"id":"owned","status":"queued"}`}
		repo := &videoSelectionFixture{accounts: []Account{*account}}
		svc := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: repo, concurrencyService: NewConcurrencyService(nil), httpUpstream: fixture})
		groupID := int64(5)
		key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformVideo, Status: StatusActive}}
		body := []byte(`{"model":"m","content":[{"type":"text","text":"waves"}],"resolution":"720p","duration":5,"future":false}`)
		req := VideoTaskSubmitRequest{Body: body, InboundProtocol: "unified"}
		wantURL := "https://compat.example/v1/video/generations"
		if native {
			req.Native = true
			req.InboundProtocol = "seedance"
			wantURL = "https://ark.example/api/v3/contents/generations/tasks"
		}
		selected, err := svc.SelectAccount(context.Background(), key, req)
		require.NoError(t, err)
		defer selected.Release()
		selected.Account.Credentials["video_endpoints"] = []string{"wan"}
		selected.Account.Credentials["video_base_urls"] = map[string]string{"wan": "https://changed.example"}
		_, err = svc.Submit(context.Background(), selected)
		require.NoError(t, err)
		require.Equal(t, wantURL, fixture.request.URL.String())
		require.JSONEq(t, string(body), string(fixture.requestBody))
		require.Equal(t, 1, fixture.calls)
	}
}

func TestVideoAdaptiveSelectedProtocolControlsMetadata(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointWan)
	account.Credentials["video_endpoints"] = []string{"seedance", "wan"}
	account.Credentials["video_model_bindings"] = map[string]any{"m": []string{"seedance", "wan"}}
	repo := &videoSelectionFixture{accounts: []Account{*account}}
	svc := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: repo, concurrencyService: NewConcurrencyService(nil)})
	groupID := int64(5)
	key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformVideo, Status: StatusActive}}
	body := []byte(`{"model":"m","resolution":"480p","duration":5,"content":[{"type":"image_url","image_url":{"url":"image"}}],"parameters":{"resolution":"1080p","duration":10},"input":{"media":[{"type":"video","url":"video"}]}}`)
	for _, endpoint := range []VideoEndpoint{VideoEndpointWan, VideoEndpointSeedance} {
		selected, err := svc.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{Body: body, Native: true, InboundProtocol: string(endpoint)})
		require.NoError(t, err)
		defer selected.Release()
		require.Equal(t, endpoint, selected.Target.Endpoint)
		if endpoint == VideoEndpointWan {
			require.Equal(t, "1080p", selected.Metadata.Resolution)
			require.Equal(t, float64(10), selected.Metadata.DurationSeconds)
			require.True(t, selected.Metadata.HasReferenceVideo)
		} else {
			require.Equal(t, "480p", selected.Metadata.Resolution)
			require.Equal(t, float64(5), selected.Metadata.DurationSeconds)
			require.False(t, selected.Metadata.HasReferenceVideo)
		}
		require.Equal(t, "480p", gjson.GetBytes(selected.Body, "resolution").String())
		require.Equal(t, "1080p", gjson.GetBytes(selected.Body, "parameters.resolution").String())
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
