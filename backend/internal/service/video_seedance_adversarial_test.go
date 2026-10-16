//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 异常 JSON 必须在任何供应商请求之前拒绝，不能让不同解析器对计费参数取不同值。
func TestVideoSeedanceAdversarialJSONParameters(t *testing.T) {
	target := officialProtocolTarget(VideoEndpointSeedance, VideoProtocolAdapterSeedance)
	target.Model = "doubao-seedance-2-0-mini-260615"
	for name, body := range map[string]string{
		"duplicate_resolution":         `{"model":"alias","prompt":"p","resolution":"720p","resolution":"768p"}`,
		"escaped_duplicate_resolution": `{"model":"alias","prompt":"p","resolution":"720p","resol\u0075tion":"768p"}`,
		"case_duplicate_resolution":    `{"model":"alias","prompt":"p","resolution":"720p","Resolution":"768p"}`,
		"case_resolution":              `{"model":"alias","prompt":"p","RESOLUTION":"768p"}`,
		"case_duration":                `{"model":"alias","prompt":"p","Duration":5,"resolution":"720p"}`,
		"case_nested_media":            `{"model":"alias","resolution":"720p","content":[{"type":"image_url","image_url":{"URL":"https://example/image.png"}}]}`,
		"duplicate_nested_media":       `{"model":"alias","resolution":"720p","content":[{"type":"text","text":"p","text":"q"}]}`,
		"resolution_null":              `{"model":"alias","prompt":"p","resolution":null}`,
		"resolution_bool":              `{"model":"alias","prompt":"p","resolution":false}`,
		"resolution_array":             `{"model":"alias","prompt":"p","resolution":["720p"]}`,
		"resolution_object":            `{"model":"alias","prompt":"p","resolution":{"value":"720p"}}`,
		"resolution_number":            `{"model":"alias","prompt":"p","resolution":720}`,
		"resolution_blank":             `{"model":"alias","prompt":"p","resolution":"  "}`,
		"resolution_control":           `{"model":"alias","prompt":"p","resolution":"720p\u0000"}`,
		"resolution_suffix":            `{"model":"alias","prompt":"p","resolution":"720p --duration 30"}`,
		"resolution_unsupported":       `{"model":"alias","prompt":"p","resolution":"768p"}`,
		"mini_1080p":                   `{"model":"alias","prompt":"p","resolution":"1080p"}`,
		"mini_4k":                      `{"model":"alias","prompt":"p","resolution":"4k"}`,
		"conflicting_size_alias":       `{"model":"alias","prompt":"p","resolution":"720p","size":"1080p"}`,
		"nested_parameter_resolution":  `{"model":"alias","prompt":"p","resolution":"720p","parameters":{"resolution":"1080p"}}`,
		"duration_null":                `{"model":"alias","prompt":"p","resolution":"720p","duration":null}`,
		"duration_bool":                `{"model":"alias","prompt":"p","resolution":"720p","duration":true}`,
		"duration_string":              `{"model":"alias","prompt":"p","resolution":"720p","duration":"5"}`,
		"duration_fraction":            `{"model":"alias","prompt":"p","resolution":"720p","duration":5.1}`,
		"duration_overflow":            `{"model":"alias","prompt":"p","resolution":"720p","duration":1e400}`,
		"duration_zero":                `{"model":"alias","prompt":"p","resolution":"720p","duration":0}`,
		"model_null":                   `{"model":null,"prompt":"p","resolution":"720p"}`,
		"duplicate_model":              `{"model":"alias","model":"other","prompt":"p","resolution":"720p"}`,
		"array_request":                `[{"model":"alias","prompt":"p","resolution":"720p"}]`,
		"trailing_json":                `{"model":"alias","prompt":"p","resolution":"720p"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := PrepareVideoProtocolRequest([]byte(body), "", target)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		})
	}
}

// 映射后的模型与模型级方案共同决定档位规则，公开别名不能误用另一模型的校验或覆盖配置。
func TestVideoSeedanceAdversarialFinalModelAndAdapterOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, requested, mapped, endpointAdapter, overrideModel, overrideAdapter, resolution, wantAdapter string
		valid                                                                                             bool
	}{
		{"mapped_mini_rejects", "public", "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterSeedance, "", "", "1080p", "", false},
		{"mapped_fast_rejects", "public", "doubao-seedance-2-0-fast-260128", VideoProtocolAdapterSeedance, "", "", "1080p", "", false},
		{"mapped_deployment_not_guessed", "doubao-seedance-2-0-mini", "ep-custom-deployment", VideoProtocolAdapterSeedance, "", "", "1080p", VideoProtocolAdapterSeedance, true},
		{"mapped_model_not_public_name", "doubao-seedance-2-0-mini", "doubao-seedance-2-5-260628", VideoProtocolAdapterSeedance, "", "", "1080p", VideoProtocolAdapterSeedance, true},
		{"final_model_selects_legacy", "public", "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterSeedance, "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterLegacy, "768p", VideoProtocolAdapterLegacy, true},
		{"final_model_selects_kemei", "public", "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterSeedance, "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterKemei, "768p", VideoProtocolAdapterKemei, true},
		{"final_model_selects_official", "public", "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterLegacy, "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterSeedance, "768p", "", false},
		{"public_override_does_not_relax", "public", "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterSeedance, "public", VideoProtocolAdapterLegacy, "768p", "", false},
		{"public_override_does_not_tighten", "public", "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterLegacy, "public", VideoProtocolAdapterSeedance, "768p", VideoProtocolAdapterLegacy, true},
		{"known_mini_normalizes_case", "public", "doubao-seedance-2-0-mini-260615", VideoProtocolAdapterSeedance, "", "", " 720P ", VideoProtocolAdapterSeedance, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointSeedance)
			account.Credentials["model_mapping"] = map[string]any{tc.requested: tc.mapped}
			account.Credentials["model_whitelist"] = []string{tc.mapped}
			account.Credentials["video_endpoint_adapters"] = map[string]string{"seedance": tc.endpointAdapter}
			if tc.overrideModel != "" {
				account.Credentials["video_model_adapters"] = map[string]any{tc.overrideModel: map[string]string{"seedance": tc.overrideAdapter}}
			}
			upstream := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil)})
			key := &APIKey{GroupID: &account.GroupIDs[0], Group: &Group{ID: account.GroupIDs[0], Platform: PlatformVideo, Status: StatusActive}}
			request := VideoTaskSubmitRequest{Native: true, InboundProtocol: "seedance", Body: []byte(fmt.Sprintf(`{"model":%q,"prompt":"p","resolution":%q,"duration":5,"ratio":"16:9"}`, tc.requested, tc.resolution))}
			selected, err := upstream.SelectAccount(context.Background(), key, request)
			if !tc.valid {
				require.Error(t, err)
				require.Equal(t, "VIDEO_PROTOCOL_PARAMETERS_INVALID", infraerrors.Reason(err))
				require.Nil(t, selected)
				return
			}
			require.NoError(t, err)
			defer selected.Release()
			require.Equal(t, tc.wantAdapter, selected.Target.ProtocolAdapter)
			require.Equal(t, tc.mapped, selected.Target.Model)
			require.Equal(t, tc.mapped, selected.Metadata.Model)
			require.Equal(t, tc.mapped, gjson.GetBytes(selected.Body, "model").String())
			require.Equal(t, gjson.GetBytes(selected.Body, "resolution").String(), selected.Metadata.Resolution)
			require.Equal(t, "/api/v3/contents/generations/tasks", selected.Target.CreatePath)
		})
	}
}

// 官方候选不支持的档位仍可由同组显式原有协议账号接收；不能全局收紧旧渠道或偷偷改写为 720p。
func TestVideoSeedanceAdversarialCandidateProtocolIsolation(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterKemei} {
		t.Run(adapter, func(t *testing.T) {
			first, second := videoFixtureAccount(VideoEndpointSeedance), videoFixtureAccount(VideoEndpointSeedance)
			first.ID, first.Priority, second.ID, second.Priority = 11, 1, 12, 2
			first.Credentials["video_endpoint_adapters"] = map[string]string{"seedance": VideoProtocolAdapterSeedance}
			second.Credentials["video_endpoint_adapters"] = map[string]string{"seedance": adapter}
			upstream := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*second, *first}}, concurrencyService: NewConcurrencyService(nil)})
			key := &APIKey{GroupID: &first.GroupIDs[0], Group: &Group{ID: first.GroupIDs[0], Platform: PlatformVideo, Status: StatusActive}}
			body := []byte(`{"model":"doubao-seedance-2-0-mini-260615","prompt":"p","resolution":"768p","duration":5,"ratio":"16:9"}`)
			selected, err := upstream.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{Native: true, InboundProtocol: "seedance", Body: body})
			require.NoError(t, err)
			defer selected.Release()
			require.Equal(t, second.ID, selected.Account.ID)
			require.Equal(t, adapter, selected.Target.ProtocolAdapter)
			require.Equal(t, "768p", selected.Metadata.Resolution)
			require.Equal(t, "768p", gjson.GetBytes(selected.Body, "resolution").String())
			if adapter == VideoProtocolAdapterLegacy {
				require.JSONEq(t, string(body), string(selected.Body))
			}
		})
	}
}

// 每次选号返回独立账号值，避免测试替身自身的排序或派生缓存产生并发写。
type videoSeedanceAdversarialAccounts struct {
	AccountRepository
	account *Account
}

func (r *videoSeedanceAdversarialAccounts) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return []Account{*r.account}, nil
}

// 即使保护回归，替身也只记录调用并报错，绝不触发真实 HTTP 或上游付费任务。
type videoSeedanceAdversarialHTTP struct{ calls atomic.Int64 }

func (h *videoSeedanceAdversarialHTTP) Do(*http.Request, string, int64, int) (*http.Response, error) {
	h.calls.Add(1)
	return nil, fmt.Errorf("无效 Seedance 请求越过前置校验")
}

func (h *videoSeedanceAdversarialHTTP) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return h.Do(req, proxy, accountID, concurrency)
}

// 正式协调器并发接收非法请求仍须零建任务、零预扣、零扣费、零退款和零上游调用。
func TestVideoSeedanceAdversarialConcurrentRejectsBeforeBilling(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoPerRequest, BillingModeVideoToken} {
		t.Run(string(mode), func(t *testing.T) {
			svc, key, _, billing, logs := newVideoLifecycleFixture(mode)
			key.Group.Status = StatusActive
			account := videoFixtureAccount(VideoEndpointSeedance)
			account.GroupIDs = []int64{key.Group.ID}
			account.Credentials["model_mapping"] = map[string]any{"video-model": "doubao-seedance-2-0-mini-260615"}
			account.Credentials["video_endpoint_adapters"] = map[string]string{"seedance": VideoProtocolAdapterSeedance}
			httpFixture := &videoSeedanceAdversarialHTTP{}
			svc.upstream = NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSeedanceAdversarialAccounts{account: account}, concurrencyService: NewConcurrencyService(nil), httpUpstream: httpFixture})
			bodies := []string{
				`{"model":"video-model","prompt":"p","duration":5,"resolution":"768p"}`,
				`{"model":"video-model","prompt":"p","duration":5,"resolution":"1080p"}`,
				`{"model":"video-model","prompt":"p","duration":5,"resolution":"720p","resolution":"768p"}`,
				`{"model":"video-model","prompt":"p","duration":5,"Resolution":"768p"}`,
				`{"model":"video-model","prompt":"p","duration":5,"resolution":null}`,
				`{"model":"video-model","prompt":"p","duration":false,"resolution":"720p"}`,
				`{"model":"video-model","prompt":"p","duration":2,"frames":289,"resolution":"720p"}`,
				`{"model":"video-model","prompt":"p","duration":2,"frames":30}`,
				`{"model":"video-model","prompt":"p","duration":2,"Frames":289}`,
				`{"model":"video-model","prompt":"p","duration":2,"frames":"289"}`,
			}
			reasons := []string{"VIDEO_PROTOCOL_PARAMETERS_INVALID", "VIDEO_PROTOCOL_PARAMETERS_INVALID", "VIDEO_JSON_AMBIGUOUS", "VIDEO_JSON_AMBIGUOUS", "VIDEO_PROTOCOL_PARAMETERS_INVALID", "VIDEO_DURATION_INVALID", "VIDEO_PROTOCOL_PARAMETERS_INVALID", "VIDEO_PROTOCOL_PARAMETERS_INVALID", "VIDEO_PROTOCOL_PARAMETERS_INVALID", "VIDEO_PROTOCOL_PARAMETERS_INVALID"}
			const count = 192
			start := make(chan struct{})
			errors := make(chan error, count)
			var group sync.WaitGroup
			for index := range count {
				group.Add(1)
				go func() {
					defer group.Done()
					<-start
					response, err := svc.Submit(context.Background(), key, VideoTaskSubmitRequest{Native: true, InboundProtocol: "seedance", IdempotencyKey: fmt.Sprintf("invalid-seedance-%d", index%16), Body: []byte(bodies[index%len(bodies)])})
					if response != nil || infraerrors.Code(err) != http.StatusBadRequest || infraerrors.Reason(err) != reasons[index%len(reasons)] {
						errors <- fmt.Errorf("第 %d 个非法请求未按参数错误拒绝，response=%v, error=%v", index, response, err)
					}
				}()
			}
			close(start)
			group.Wait()
			close(errors)
			for err := range errors {
				t.Error(err)
			}
			require.Zero(t, httpFixture.calls.Load())
			require.Empty(t, svc.repo.(*videoLifecycleRepo).tasks)
			require.Zero(t, billing.reserves)
			require.Zero(t, billing.captures)
			require.Zero(t, billing.releases)
			require.Empty(t, logs.logs)
			t.Logf("并发请求=%d，计费模式=%s，预扣/扣费/退款/上游调用均为 0", count, mode)
		})
	}
}
