//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 使用真实选号、协议转换及任务协调器，HTTP 与持久化替身隔离所有外部生成和资金操作。
func videoProfileCompatibilityFixture(t *testing.T, endpoint VideoEndpoint, profile, tier, completed string) (*VideoTaskService, *APIKey, *Account, *videoAccountTestHTTP, *videoLifecycleBilling, *videoLifecycleLogs) {
	t.Helper()
	svc, key, _, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
	key.Group.Status = StatusActive
	price, imagePrice := .3, .15
	key.Group.ModelPricing = []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: BillingModeVideo,
		VideoPrices: []VideoPriceTier{{Resolution: tier, Price: &price}}, VideoImageInputPricing: &VideoImageInputPricing{Price: &imagePrice}}}
	account := videoFixtureAccount(endpoint)
	account.GroupIDs = []int64{key.Group.ID}
	account.Credentials["model_mapping"] = map[string]any{"video-model": "provider-model"}
	account.Credentials["model_whitelist"] = []string{"provider-model"}
	account.Credentials["video_model_bindings"] = map[string]string{"provider-model": string(endpoint)}
	if profile != "" {
		account.Credentials["video_openai_profile"] = profile
	}
	httpFixture := &videoAccountTestHTTP{handle: func(req *http.Request, _ []byte) (*http.Response, error) {
		switch req.Method {
		case http.MethodPost:
			return videoAccountTestJSON(http.StatusOK, `{"id":"profile-task","status":"queued"}`), nil
		case http.MethodGet:
			return videoAccountTestJSON(http.StatusOK, completed), nil
		default:
			t.Fatalf("测试不应发起 %s 请求", req.Method)
			return nil, nil
		}
	}}
	selection := &videoSelectionFixture{accounts: []Account{*account}}
	svc.upstream = NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: selection, concurrencyService: NewConcurrencyService(nil), httpUpstream: httpFixture})
	svc.accounts = videoLifecycleAccounts{account: account}
	return svc, key, account, httpFixture, billing, logs
}

// 缺省和显式保真、H3 两种协议形态均覆盖无图、URL、Data URL 和明确空数组。
func TestVideoProfileCompatibilityGenerationContract(t *testing.T) {
	profiles := []struct{ name, value string }{{"legacy_default", ""}, {"explicit_passthrough", VideoOpenAIProfilePassthrough}, {"metaso_h3", VideoOpenAIProfileMetasoH3}}
	images := []struct {
		name, value string
		count       int
	}{
		{"text_only", "", 0}, {"url", "https://assets.example/reference.jpg", 1},
		{"data_url", "data:image/png;base64,iVBORw0KGgo=", 1}, {"empty_array", "", 0}, {"legacy_null", "", 0},
	}
	for _, profile := range profiles {
		for _, standard := range []bool{false, true} {
			for _, image := range images {
				// 保真模式兼容原有 null=无图；显式 H3 的 null 在拒绝用例单独验证。
				if image.name == "legacy_null" && profile.value == VideoOpenAIProfileMetasoH3 {
					continue
				}
				name := fmt.Sprintf("%s/%s/%s", profile.name, map[bool]string{false: "legacy_fields", true: "standard_fields"}[standard], image.name)
				t.Run(name, func(t *testing.T) {
					body := ` { "model":"video-model", "prompt":"video-model stays in the prompt", "future":{"n":9007199254740993}, `
					if standard {
						body += `"seconds":"5", "size":"1280x720"`
					} else {
						body += `"duration":5, "resolution":"768p", "ratio":"16:9"`
					}
					if image.count > 0 {
						if standard {
							body += fmt.Sprintf(`, "input_reference":{"image_url":%q}`, image.value)
						} else {
							body += fmt.Sprintf(`, "images":[%q]`, image.value)
						}
					} else if image.name == "empty_array" {
						body += `, "images":[]`
					} else if image.name == "legacy_null" {
						body += `, "images":null`
					}
					body += " } "
					tier := "768p"
					if standard && profile.value != VideoOpenAIProfileMetasoH3 {
						tier = "1280x720"
					}
					completed := `{"id":"profile-task","status":"completed","duration":4,"resolution":"768p","video":{"url":"https://assets.example/video.mp4"}}`
					if standard || profile.value == VideoOpenAIProfileMetasoH3 {
						completed = `{"id":"profile-task","status":"completed","seconds":"4","size":"1280x720","video":{"url":"https://assets.example/video.mp4"}}`
					}
					svc, key, account, upstream, billing, logs := videoProfileCompatibilityFixture(t, VideoEndpointOpenAIVideos, profile.value, tier, completed)
					ctx := context.Background()
					req := VideoTaskSubmitRequest{Body: []byte(body), InboundProtocol: "openai_videos", IdempotencyKey: "stable-profile-request"}
					created, err := svc.Submit(ctx, key, req)
					require.NoError(t, err)
					require.NotNil(t, created)
					require.Len(t, upstream.requests, 1)
					require.Equal(t, http.MethodPost, upstream.requests[0].Method)
					require.Equal(t, "/prefix/v1/videos", upstream.requests[0].URL.Path)
					require.Equal(t, "Bearer fixture-secret", upstream.requests[0].Header.Get("Authorization"))
					sent := upstream.bodies[0]
					require.Equal(t, "provider-model", gjson.GetBytes(sent, "model").String())
					require.Equal(t, "video-model stays in the prompt", gjson.GetBytes(sent, "prompt").String())
					require.Equal(t, "9007199254740993", gjson.GetBytes(sent, "future.n").Raw)
					if profile.value == VideoOpenAIProfileMetasoH3 {
						require.Equal(t, int64(5), gjson.GetBytes(sent, "seconds").Int())
						require.Equal(t, "1280x720", gjson.GetBytes(sent, "size").String())
						require.Equal(t, image.count > 0, gjson.GetBytes(sent, "input_reference").Exists())
						if image.count > 0 {
							require.Equal(t, image.value, gjson.GetBytes(sent, "input_reference.image_url").String())
						}
						for _, field := range []string{"duration", "resolution", "ratio", "images", "image_urls"} {
							require.False(t, gjson.GetBytes(sent, field).Exists())
						}
					} else {
						// 只允许模型字段变化，其它原始空白、扩展数字和媒体形式必须逐字保留。
						want := strings.Replace(body, `"model":"video-model"`, `"model":"provider-model"`, 1)
						require.Equal(t, want, string(sent))
					}
					task, err := svc.repo.Get(ctx, created.LocalTaskID)
					require.NoError(t, err)
					require.Equal(t, "queued", task.Status)
					require.Equal(t, "reserved", task.BillingStatus)
					// 旧 OpenAI profile 在读取时换算为模型绑定，任务冻结第 2 版目标与显式方案，不再记录旧 profile 字段。
					wantAdapter := VideoProtocolAdapterLegacy
					if profile.value == VideoOpenAIProfileMetasoH3 {
						wantAdapter = VideoProtocolAdapterMetasoH3
					}
					require.Equal(t, 2, task.Target.Version)
					require.Equal(t, wantAdapter, task.Target.ProtocolAdapter)
					require.Empty(t, task.Target.OpenAIProfile)
					require.Equal(t, VideoEndpointOpenAIVideos, task.Target.Endpoint)
					require.Equal(t, "provider-model", task.Target.Model)
					require.Equal(t, "video-model", task.Quote.Model)
					require.Equal(t, PricingSourceGroup, task.Quote.Source)
					require.Equal(t, tier, task.Quote.Resolution)
					require.Equal(t, .3, task.Quote.UnitPrice)
					require.Equal(t, image.count, *task.Metadata.ReferenceImageCount)
					require.Equal(t, image.count, *task.Quote.ReferenceImageCount)
					require.False(t, task.Metadata.HasReferenceVideo)
					imageCost := .15 * float64(image.count)
					require.InDelta(t, 3+imageCost, task.Hold.HoldAmount, 1e-12)
					quoteBefore, err := json.Marshal(task.Quote)
					require.NoError(t, err)
					// 受理后修改账号协议和实时价卡，轮询结算仍须使用原任务合同。
					account.Credentials["video_openai_profile"] = VideoOpenAIProfilePassthrough
					if profile.value != VideoOpenAIProfileMetasoH3 {
						account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
					}
					*key.Group.ModelPricing[0].VideoPrices[0].Price = 99
					*key.Group.ModelPricing[0].VideoImageInputPricing.Price = 88
					restarted := *svc
					restarted.advance(ctx, task)
					settled, err := svc.repo.Get(ctx, task.ID)
					require.NoError(t, err)
					require.Equal(t, "completed", settled.Status)
					require.Equal(t, "settled", settled.BillingStatus)
					require.False(t, settled.PricingMismatch)
					require.True(t, settled.EffectsDone)
					require.Equal(t, 4., settled.Metadata.DurationSeconds)
					require.InDelta(t, 2.4+imageCost, settled.BillingResult.ActualAmountUSD, 1e-12)
					quoteAfter, err := json.Marshal(settled.Quote)
					require.NoError(t, err)
					require.Equal(t, quoteBefore, quoteAfter)
					require.Len(t, upstream.requests, 2)
					require.Equal(t, http.MethodGet, upstream.requests[1].Method)
					require.Equal(t, "/prefix/v1/videos/profile-task", upstream.requests[1].URL.Path)
					_, err = svc.Query(ctx, key, settled.ID, "openai_videos")
					require.NoError(t, err)
					replayed, err := svc.Submit(ctx, key, req)
					require.NoError(t, err)
					require.Equal(t, created.LocalTaskID, replayed.LocalTaskID)
					restarted.advance(ctx, settled)
					require.Len(t, upstream.requests, 2, "幂等重放、已完成查询和恢复不能再生成")
					require.Equal(t, 1, billing.reserves)
					require.Equal(t, 1, billing.captures)
					require.Zero(t, billing.releases)
					require.Len(t, logs.logs, 1)
					log := logs.logs["video_capture:"+settled.ID]
					require.Equal(t, "video-model", log.Model)
					require.Equal(t, "provider-model", *log.UpstreamModel)
					require.Equal(t, 4, *log.VideoDurationSeconds)
					require.Equal(t, tier, *log.VideoResolution)
					require.InDelta(t, imageCost, log.ImageInputCost, 1e-12)
					require.InDelta(t, 1.2, log.OutputCost, 1e-12)
					require.InDelta(t, 2.4+imageCost, log.ActualCost, 1e-12)
				})
			}
		}
	}
}

// 冲突和非法输入必须在创建本地任务、资金预留及 HTTP POST 之前失败。
func TestVideoProfileCompatibilityRejectsBeforeReserve(t *testing.T) {
	for _, profile := range []string{"", VideoOpenAIProfilePassthrough, VideoOpenAIProfileMetasoH3} {
		cases := []struct{ name, fields string }{
			{"duration_conflict", `"duration":5,"seconds":8,"resolution":"768p","ratio":"16:9"`},
			{"resolution_conflict", `"seconds":5,"size":"1280x720","resolution":"2k"`},
			{"zero_seconds", `"seconds":0,"size":"1280x720"`},
			{"fraction_seconds", `"seconds":5.5,"size":"1280x720"`},
			{"seconds_case", `"Seconds":5,"size":"1280x720"`},
			{"duplicate_seconds", `"seconds":5,"seconds":5,"size":"1280x720"`},
			{"unknown_image_shape", `"seconds":5,"size":"1280x720","input_reference":{}`},
		}
		if profile == VideoOpenAIProfileMetasoH3 {
			cases = append(cases, []struct{ name, fields string }{
				{"null_images", `"duration":5,"resolution":"768p","ratio":"16:9","images":null`},
				{"missing_orientation", `"duration":5,"resolution":"768p"`},
				{"unsupported_geometry", `"seconds":5,"size":"1920x1080"`},
				{"two_images", `"seconds":5,"size":"1280x720","images":["a","b"]`},
				{"conflicting_image_aliases", `"seconds":5,"size":"1280x720","images":["a"],"input_reference":{"image_url":"b"}`},
			}...)
		}
		for _, tc := range cases {
			t.Run(profile+"/"+tc.name, func(t *testing.T) {
				tier := "1280x720"
				if profile == VideoOpenAIProfileMetasoH3 || strings.Contains(tc.fields, `"resolution":"768p"`) {
					tier = "768p"
				}
				svc, key, _, upstream, billing, logs := videoProfileCompatibilityFixture(t, VideoEndpointOpenAIVideos, profile, tier, "")
				_, err := svc.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model",` + tc.fields + `}`), InboundProtocol: "openai_videos"})
				require.Error(t, err)
				require.Empty(t, billing.repo.tasks)
				require.Empty(t, upstream.requests)
				require.Zero(t, billing.reserves)
				require.Zero(t, billing.captures)
				require.Zero(t, billing.releases)
				require.Empty(t, logs.logs)
			})
		}
	}
}

// 统一入口 /v1/videos 按模型绑定选择协议：只启用兼容端点的账号经 compat 绑定转发到 /v1/video/generations，
// 保存的 H3 profile 只属于 openai_videos 端点，不会改写兼容协议的请求体。
func TestVideoProfileCompatibilityLegacyEndpointIsolation(t *testing.T) {
	for _, profile := range []string{"", VideoOpenAIProfilePassthrough, VideoOpenAIProfileMetasoH3} {
		for _, hasImage := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/image_%t", profile, hasImage), func(t *testing.T) {
				body := `{"model":"video-model","prompt":"legacy","duration":5,"resolution":"540p","ratio":"1:1"`
				if hasImage {
					body += `,"images":["https://assets.example/reference.jpg"]`
				}
				body += `}`
				svc, key, _, upstream, billing, _ := videoProfileCompatibilityFixture(t, VideoEndpointCompat, profile, "540p", "")
				response, err := svc.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(body), InboundProtocol: "openai_videos"})
				require.NoError(t, err)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, "/prefix/v1/video/generations", upstream.requests[0].URL.Path)
				require.Equal(t, strings.Replace(body, `"model":"video-model"`, `"model":"provider-model"`, 1), string(upstream.bodies[0]))
				task, err := svc.repo.Get(context.Background(), response.LocalTaskID)
				require.NoError(t, err)
				require.Empty(t, task.Target.OpenAIProfile)
				require.Equal(t, VideoEndpointCompat, task.Target.Endpoint)
				require.Equal(t, VideoProtocolAdapterLegacy, task.Target.ProtocolAdapter)
				require.Equal(t, "540p", task.Metadata.Resolution)
				require.Equal(t, map[bool]int{false: 0, true: 1}[hasImage], *task.Metadata.ReferenceImageCount)
				require.Equal(t, 1, billing.reserves)
			})
		}
	}
}

// 复现旧字段请求遇到标准响应的升级边界：默认配置不能把精确像素猜成旧价档。
// 此断言记录当前待核对行为，不能据此宣称旧账号与 v3.13 的结算完全兼容。
func TestVideoProfileCompatibilityLegacyRequestStandardResponse(t *testing.T) {
	for _, profile := range []struct{ name, value string }{{"legacy_default", ""}, {"explicit_passthrough", VideoOpenAIProfilePassthrough}, {"metaso_h3", VideoOpenAIProfileMetasoH3}} {
		for _, hasImage := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/image_%t", profile.name, hasImage), func(t *testing.T) {
				body := `{"model":"video-model","prompt":"legacy client","duration":5,"resolution":"768p","ratio":"16:9"`
				imageCost := 0.
				if hasImage {
					body += `,"images":["https://assets.example/reference.jpg"]`
					imageCost = .15
				}
				body += `}`
				completed := `{"id":"profile-task","status":"completed","seconds":"4","size":"1280x720","video":{"url":"https://assets.example/video.mp4"}}`
				svc, key, account, upstream, billing, logs := videoProfileCompatibilityFixture(t, VideoEndpointOpenAIVideos, profile.value, "768p", completed)
				ctx := context.Background()
				req := VideoTaskSubmitRequest{Body: []byte(body), InboundProtocol: "openai_videos", IdempotencyKey: "legacy-standard-boundary"}
				created, err := svc.Submit(ctx, key, req)
				require.NoError(t, err)
				task, err := svc.repo.Get(ctx, created.LocalTaskID)
				require.NoError(t, err)
				require.InDelta(t, 3+imageCost, task.Hold.HoldAmount, 1e-12)
				require.Equal(t, "768p", task.Quote.Resolution)
				svc.advance(ctx, task)
				observed, err := svc.repo.Get(ctx, task.ID)
				require.NoError(t, err)
				require.Equal(t, "completed", observed.Status)
				require.Equal(t, 4., observed.Metadata.DurationSeconds)
				require.Equal(t, "1280x720", observed.Metadata.Size)
				require.Equal(t, completed, string(observed.RawResponse))
				require.Equal(t, 1, billing.reserves)
				require.Zero(t, billing.releases)
				if profile.value == VideoOpenAIProfileMetasoH3 {
					require.Equal(t, "settled", observed.BillingStatus)
					require.False(t, observed.PricingMismatch)
					require.InDelta(t, 2.4+imageCost, observed.BillingResult.ActualAmountUSD, 1e-12)
					require.Equal(t, 1, billing.captures)
					require.Len(t, logs.logs, 1)
				} else {
					require.Equal(t, "reconciliation", observed.BillingStatus)
					require.True(t, observed.PricingMismatch)
					require.Empty(t, observed.ResponseMetadataError, "响应格式有效，阻断来自实际像素与冻结价档不一致")
					require.InDelta(t, 3+imageCost, observed.Hold.HoldAmount, 1e-12)
					require.Nil(t, observed.BillingResult)
					require.Zero(t, billing.captures)
					require.Empty(t, logs.logs)
					// 后改账号不能重解释已经受理的任务；重启恢复仍只查询，不重提、不退款。
					account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
					restarted := *svc
					restarted.advance(ctx, observed)
					recovered, err := svc.repo.Get(ctx, task.ID)
					require.NoError(t, err)
					require.True(t, recovered.PricingMismatch)
					require.Equal(t, "reconciliation", recovered.BillingStatus)
					require.InDelta(t, 3+imageCost, recovered.Hold.HoldAmount, 1e-12)
					require.Zero(t, billing.captures)
					require.Zero(t, billing.releases)
					require.Empty(t, logs.logs)
				}
				_, err = svc.Submit(ctx, key, req)
				require.NoError(t, err)
				posts := 0
				for _, request := range upstream.requests {
					if request.Method == http.MethodPost {
						posts++
					}
				}
				require.Equal(t, 1, posts)
				require.Equal(t, 1, billing.reserves)
			})
		}
	}
}
