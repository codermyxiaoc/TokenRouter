package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 智能选组必须按最终上游模型核对显式端点，不能把一个 OpenAI 入口当作另一个入口的授权。
func TestVideoSmartRoutingReadinessEndpointAndModelBindings(t *testing.T) {
	for _, tc := range []struct {
		name, path, model string
		endpoints         []string
		bindings          map[string]any
		want              SmartRoutingGroupAvailability
	}{
		{"compat_enabled", "/v1/video/generations", "client-video", []string{"compat"}, nil, SmartRoutingGroupAvailability{true, true}},
		{"compat_does_not_grant_videos", "/v1/videos", "client-video", []string{"compat"}, nil, SmartRoutingGroupAvailability{true, false}},
		{"videos_enabled", "/v1/videos", "client-video", []string{"openai_videos"}, nil, SmartRoutingGroupAvailability{true, true}},
		{"videos_does_not_grant_compat", "/v1/video/generations", "client-video", []string{"openai_videos"}, nil, SmartRoutingGroupAvailability{true, false}},
		{"native_does_not_grant_compat", "/v1/video/generations", "client-video", []string{"seedance"}, nil, SmartRoutingGroupAvailability{true, false}},
		{"mapped_binding_excludes_compat", "/v1/video/generations", "client-video", []string{"compat", "seedance"}, map[string]any{"upstream-video": "seedance"}, SmartRoutingGroupAvailability{true, false}},
		{"mapped_binding_allows_native", "/api/v3/contents/generations/tasks", "client-video", []string{"compat", "seedance"}, map[string]any{"upstream-video": "seedance"}, SmartRoutingGroupAvailability{true, true}},
		{"unknown_model", "/v1/video/generations", "not-configured", []string{"compat"}, nil, SmartRoutingGroupAvailability{}},
		{"text_responses_excluded", "/v1/responses", "client-video", []string{"compat"}, nil, SmartRoutingGroupAvailability{}},
		{"text_chat_excluded", "/v1/chat/completions", "client-video", []string{"compat"}, nil, SmartRoutingGroupAvailability{}},
		{"text_messages_excluded", "/v1/messages", "client-video", []string{"compat"}, nil, SmartRoutingGroupAvailability{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			account.Credentials["model_mapping"] = map[string]any{"client-video": "upstream-video"}
			account.Credentials["video_endpoints"] = tc.endpoints
			if tc.bindings != nil {
				account.Credentials["video_model_bindings"] = tc.bindings
			}
			repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{5: {*account}}}
			svc := NewSmartRoutingService(&GatewayService{accountRepo: repo}, nil)
			got, err := svc.EvaluateSmartRoutingGroup(context.Background(), &Group{ID: 5, Platform: PlatformVideo, Status: StatusActive}, tc.model, tc.path)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// 预检只读取多个账号的并发计数；一个账号满载不会误判整个视频分组不可用。
func TestVideoSmartRoutingReadinessMultipleAccounts(t *testing.T) {
	first, second := videoFixtureAccount(VideoEndpointCompat), videoFixtureAccount(VideoEndpointCompat)
	first.ID, second.ID = 11, 12
	for _, account := range []*Account{first, second} {
		account.Concurrency = 1
		account.Credentials["model_mapping"] = map[string]any{"client-video": "upstream-video"}
	}
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{5: {*first, *second}}}
	cache := &smartRoutingConcurrencyCache{counts: map[int64]int{11: 1, 12: 0}}
	svc := NewSmartRoutingService(&GatewayService{accountRepo: repo, concurrencyService: NewConcurrencyService(cache)}, nil)
	group := &Group{ID: 5, Platform: PlatformVideo, Status: StatusActive}
	got, err := svc.EvaluateSmartRoutingGroup(context.Background(), group, "client-video", "/v1/video/generations")
	require.NoError(t, err)
	require.Equal(t, SmartRoutingGroupAvailability{true, true}, got)
	cache.counts[12] = 1
	got, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "client-video", "/v1/video/generations")
	require.NoError(t, err)
	require.Equal(t, SmartRoutingGroupAvailability{true, false}, got)
	cache.err = errors.New("fixture concurrency unavailable")
	_, err = svc.EvaluateSmartRoutingGroup(context.Background(), group, "client-video", "/v1/video/generations")
	require.ErrorContains(t, err, "fixture concurrency unavailable", "读取故障不得伪装成没有可用账号")
	require.Equal(t, []int64{5, 5, 5}, repo.groups)
}
