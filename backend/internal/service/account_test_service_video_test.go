//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 测试全部使用内存上游替身，不访问真实供应商或生成付费任务。
type videoAccountTestHTTP struct {
	requests []*http.Request
	bodies   [][]byte
	handle   func(*http.Request, []byte) (*http.Response, error)
}

func (u *videoAccountTestHTTP) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.requests = append(u.requests, r)
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	u.bodies = append(u.bodies, body)
	return u.handle(r, body)
}

func (u *videoAccountTestHTTP) DoWithTLS(r *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, proxy, id, concurrency)
}

func videoAccountTestJSON(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func videoAccountTestInt(value int) *int { return &value }

func videoAccountTestService(account *Account, handle func(*http.Request, []byte) (*http.Response, error)) (*AccountTestService, *videoAccountTestHTTP) {
	svc, _ := adaptiveCNAccountTestService(account)
	upstream := &videoAccountTestHTTP{handle: handle}
	svc.httpUpstream = upstream
	return svc, upstream
}

func TestVideoAccountTestSixProtocolsUseConfiguredEndpointAndMappedModel(t *testing.T) {
	fixtures := []struct {
		endpoint                          VideoEndpoint
		create, poll, accepted, completed string
	}{
		{VideoEndpointCompat, "/v1/video/generations", "/v1/video/generations/task-1", `{"id":"task-1","status":"pending"}`, `{"task_id":"task-1","status":"completed","data":[{"url":"https://cdn.example/video.mp4"}]}`},
		{VideoEndpointOpenAIVideos, "/v1/videos", "/v1/videos/task-1", `{"id":"task-1","status":"queued"}`, `{"id":"task-1","status":"completed","video":{"url":"https://cdn.example/video.mp4"}}`},
		{VideoEndpointSeedance, "/api/v3/contents/generations/tasks", "/api/v3/contents/generations/tasks/task-1", `{"id":"task-1","status":"queued"}`, `{"id":"task-1","status":"succeeded","content":{"video_url":"https://cdn.example/video.mp4"}}`},
		{VideoEndpointKling, "/omni-video/real-model", "/tasks?task_ids=task-1", `{"code":0,"data":{"task_id":"task-1","task_status":"submitted"}}`, `{"code":0,"data":{"task_id":"task-1","task_status":"succeed","task_result":{"videos":[{"url":"https://cdn.example/video.mp4"}]}}}`},
		{VideoEndpointWan, "/api/v1/services/aigc/video-generation/video-synthesis", "/api/v1/tasks/task-1", `{"output":{"task_id":"task-1","task_status":"PENDING"}}`, `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","video_url":"https://cdn.example/video.mp4"}}`},
		{VideoEndpointMiniMax, "/v2/video_generation", "/v2/query/video_generation/task-1", `{"task":{"id":"task-1","status":"pending"}}`, `{"task":{"id":"task-1","status":"completed","content":{"url":"https://cdn.example/video.mp4"}}}`},
	}
	for _, f := range fixtures {
		t.Run(string(f.endpoint), func(t *testing.T) {
			account := videoFixtureAccount(f.endpoint)
			account.Credentials["model_mapping"] = map[string]any{"visible-model": "real-model"}
			account.Credentials["model_whitelist"] = []string{"real-model"}
			account.Credentials["video_model_bindings"] = map[string]any{"real-model": string(f.endpoint)}
			account.Credentials["video_model_paths"] = map[string]any{"real-model": "/omni-video/{model}"}
			account.Credentials["video_base_urls"] = map[string]string{string(f.endpoint): "https://selected.example/prefix"}
			account.Credentials["header_override_enabled"] = true
			account.Credentials["header_overrides"] = map[string]any{"X-Test-Tenant": "test-tenant"}
			before, _ := json.Marshal(account)
			imageURL := grokModesImageDataURL(t, 8)
			svc, upstream := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
				require.True(t, HTTPUpstreamRedirectsDisabled(r.Context()))
				require.Equal(t, "Bearer fixture-secret", r.Header.Get("Authorization"))
				require.Equal(t, "test-tenant", getHeaderRaw(r.Header, "x-test-tenant"))
				require.Equal(t, "selected.example", r.URL.Host)
				if r.Method == http.MethodPost {
					require.Equal(t, "/prefix"+f.create, r.URL.RequestURI())
					require.Equal(t, "real-model", gjson.GetBytes(body, "model").String())
					metadata, err := ParseVideoEndpointRequestMetadata(body, "", f.endpoint)
					require.NoError(t, err)
					require.Equal(t, float64(7), metadata.DurationSeconds)
					require.Equal(t, "768p", metadata.Resolution)
					require.NotNil(t, metadata.ReferenceImageCount)
					require.Equal(t, 1, *metadata.ReferenceImageCount)
					require.Contains(t, string(body), "a unique testing prompt")
					if f.endpoint == VideoEndpointWan {
						require.Equal(t, "enable", r.Header.Get("X-DashScope-Async"))
						require.False(t, gjson.GetBytes(body, "prompt").Exists())
					}
					return videoAccountTestJSON(202, f.accepted), nil
				}
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/prefix"+f.poll, r.URL.RequestURI())
				return videoAccountTestJSON(200, f.completed), nil
			})
			c, recorder := grokModesContext(context.Background())
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "visible-model", "a unique testing prompt", "video", "", "", AccountTestOptions{
				VideoEndpoint: string(f.endpoint), Duration: videoAccountTestInt(7), Resolution: "768p", Ratio: "16:9", ImageDataURL: imageURL,
			})
			require.NoError(t, err)
			require.Len(t, upstream.requests, 2)
			require.Contains(t, recorder.Body.String(), `"type":"video"`)
			require.Contains(t, recorder.Body.String(), `"success":true`)
			require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
			require.NotContains(t, recorder.Body.String(), "fixture-secret")
			after, _ := json.Marshal(account)
			require.JSONEq(t, string(before), string(after))
		})
	}
}

// 使用完整管理员测试链路确认显式 H3 配置转换生效，模型映射与单次提交行为保持不变。
func TestVideoAccountTestMetasoH3UsesConfiguredProfile(t *testing.T) {
	for _, test := range []struct {
		resolution, ratio, size string
		duration                *int
		wantSeconds             int64
	}{
		{"768p", "16:9", "1280x720", nil, 5},
		{"768p", "9:16", "720x1280", videoAccountTestInt(7), 7},
		{"2k", "16:9", "1792x1024", videoAccountTestInt(15), 15},
		{"2K", "9:16", "1024x1792", videoAccountTestInt(5), 5},
	} {
		t.Run(test.resolution+"_"+test.ratio, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
			account.Credentials["model_mapping"] = map[string]any{"visible-h3": "MiniMax-H3"}
			account.Credentials["model_whitelist"] = []string{"MiniMax-H3"}
			before, err := json.Marshal(account)
			require.NoError(t, err)
			imageURL := grokModesImageDataURL(t, 8)
			svc, upstream := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
				if r.Method == http.MethodPost {
					require.Equal(t, "MiniMax-H3", gjson.GetBytes(body, "model").String())
					require.Equal(t, test.wantSeconds, gjson.GetBytes(body, "seconds").Int())
					require.Equal(t, test.size, gjson.GetBytes(body, "size").String())
					require.Equal(t, imageURL, gjson.GetBytes(body, "input_reference.image_url").String())
					for _, field := range []string{"duration", "resolution", "ratio", "images"} {
						require.False(t, gjson.GetBytes(body, field).Exists(), "legacy field %s must not reach H3", field)
					}
					return videoAccountTestJSON(200, `{"id":"h3-test","status":"queued"}`), nil
				}
				require.Equal(t, http.MethodGet, r.Method)
				return videoAccountTestJSON(200, `{"id":"h3-test","status":"completed","video":{"url":"https://cdn.example/h3.mp4"}}`), nil
			})
			c, recorder := grokModesContext(context.Background())
			err = svc.TestAccountConnectionWithOptions(c, account.ID, "visible-h3", "A red ball.", "video", "", "", AccountTestOptions{
				VideoEndpoint: "openai_videos", Duration: test.duration, Resolution: test.resolution, Ratio: test.ratio, ImageDataURL: imageURL,
			})
			require.NoError(t, err)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, 1, strings.Count(recorder.Body.String(), `"success":true`))
			require.NotContains(t, recorder.Body.String(), "fixture-secret")
			after, err := json.Marshal(account)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
		})
	}
}

// 模型名称不能暗中启用适配；同一账号保存的 H3 配置也不能影响兼容旧路径。
func TestVideoAccountTestMetasoH3DoesNotChangePassthrough(t *testing.T) {
	for _, test := range []struct {
		name, profile string
		endpoint      VideoEndpoint
	}{
		{"legacy-default", "", VideoEndpointOpenAIVideos},
		{"explicit-passthrough", VideoOpenAIProfilePassthrough, VideoEndpointOpenAIVideos},
		{"other-endpoint", VideoOpenAIProfileMetasoH3, VideoEndpointCompat},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := videoFixtureAccount(test.endpoint)
			if test.profile != "" {
				account.Credentials["video_openai_profile"] = test.profile
			}
			imageURL := grokModesImageDataURL(t, 8)
			svc, upstream := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, int64(5), gjson.GetBytes(body, "duration").Int())
				require.Equal(t, "540p", gjson.GetBytes(body, "resolution").String())
				require.Equal(t, "1:1", gjson.GetBytes(body, "ratio").String())
				require.Equal(t, imageURL, gjson.GetBytes(body, "images.0").String())
				for _, field := range []string{"seconds", "size", "input_reference"} {
					require.False(t, gjson.GetBytes(body, field).Exists())
				}
				return videoAccountTestJSON(200, `{"id":"legacy-test","status":"completed","video":{"url":"https://cdn.example/video.mp4"}}`), nil
			})
			c, recorder := grokModesContext(context.Background())
			require.NoError(t, svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3", "A red ball.", "video", "", "", AccountTestOptions{
				VideoEndpoint: string(test.endpoint), Resolution: "540p", Ratio: "1:1", ImageDataURL: imageURL,
			}))
			require.Len(t, upstream.requests, 1)
			require.Contains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

// 参数转换失败必须在任何上游请求之前结束，不能创建可能收费的无效测试任务。
func TestVideoAccountTestMetasoH3RejectsUnsupportedGeometryBeforeSubmit(t *testing.T) {
	for _, test := range []struct{ resolution, ratio string }{
		{"768p", "1:1"}, {"768p", "adaptive"}, {"540p", "16:9"}, {"", "16:9"}, {"768p", ""},
	} {
		t.Run(test.resolution+"_"+test.ratio, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
			svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
				t.Fatal("invalid H3 parameters must not create an upstream task")
				return nil, nil
			})
			c, recorder := grokModesContext(context.Background())
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3", "A red ball.", "video", "", "", AccountTestOptions{
				VideoEndpoint: "openai_videos", Resolution: test.resolution, Ratio: test.ratio,
			}))
			require.Empty(t, upstream.requests)
			require.Contains(t, recorder.Body.String(), `"type":"error"`)
			require.NotContains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

// 排队包常使用零秒和空尺寸占位；测试应继续查询，按完成包的真实元数据判断成功。
func TestVideoAccountTestMetasoH3AllowsQueuedPlaceholderMetadata(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
	queued := []byte(`{"id":"h3-placeholder","status":"queued","seconds":0,"size":""}`)
	parsed := ExtractVideoUpstreamResponseWithProfile(VideoEndpointOpenAIVideos, queued, "", VideoOpenAIProfileMetasoH3)
	parsed.Body, parsed.StatusCode = queued, http.StatusOK
	require.NotEmpty(t, parsed.MetadataError, "夹具必须覆盖已被解析器标记的排队占位值")
	require.NoError(t, videoAccountTestResponseError(VideoEndpointOpenAIVideos, parsed))
	svc, upstream := videoAccountTestService(account, func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == http.MethodPost {
			return videoAccountTestJSON(http.StatusOK, string(queued)), nil
		}
		require.Equal(t, http.MethodGet, r.Method)
		return videoAccountTestJSON(http.StatusOK, `{"id":"h3-placeholder","status":"completed","seconds":5,"size":"1280x720","video":{"url":"https://cdn.example/video.mp4"}}`), nil
	})
	c, recorder := grokModesContext(context.Background())
	require.NoError(t, svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3", "A red ball.", "video", "", "", AccountTestOptions{
		VideoEndpoint: "openai_videos", Resolution: "768p", Ratio: "16:9",
	}))
	require.Len(t, upstream.requests, 2)
	require.Contains(t, recorder.Body.String(), `"type":"video"`)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"success":true`))
	require.NotContains(t, recorder.Body.String(), `"type":"error"`)
}

// 管理员诊断与正式任务使用同一元数据校验，错误的完成包不得显示成功或预览。
func TestVideoAccountTestMetasoH3RejectsInvalidCompletionMetadata(t *testing.T) {
	for _, stage := range []string{"create", "poll"} {
		for _, fields := range []string{`"seconds":"invalid"`, `"size":"800x800"`, `"seconds":5,"duration":8`} {
			t.Run(stage+"_"+fields, func(t *testing.T) {
				account := videoFixtureAccount(VideoEndpointOpenAIVideos)
				account.Credentials["video_openai_profile"] = VideoOpenAIProfileMetasoH3
				svc, upstream := videoAccountTestService(account, func(r *http.Request, _ []byte) (*http.Response, error) {
					if stage == "poll" && r.Method == http.MethodPost {
						return videoAccountTestJSON(200, `{"id":"h3-invalid","status":"queued"}`), nil
					}
					return videoAccountTestJSON(200, `{"id":"h3-invalid","status":"completed",`+fields+`,"video":{"url":"https://cdn.example/video.mp4"},"debug":"fixture-secret"}`), nil
				})
				c, recorder := grokModesContext(context.Background())
				require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3", "A red ball.", "video", "", "", AccountTestOptions{
					VideoEndpoint: "openai_videos", Resolution: "768p", Ratio: "16:9",
				}))
				wantRequests := 1
				if stage == "poll" {
					wantRequests = 2
				}
				require.Len(t, upstream.requests, wantRequests)
				require.Contains(t, recorder.Body.String(), "上游视频时长、尺寸或用量数据异常")
				require.NotContains(t, recorder.Body.String(), `"success":true`)
				require.NotContains(t, recorder.Body.String(), `"type":"video"`)
				require.NotContains(t, recorder.Body.String(), "fixture-secret")
			})
		}
	}
}

func TestVideoAccountTestRejectsConfigurationBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name, model, testType, endpoint, mode string
		options                               AccountTestOptions
		change                                func(*Account)
	}{
		{name: "missing-model", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat"}},
		// 管理员不再选择端点：模型未绑定协议、绑定协议无效或旧绑定指向未启用端点时，均在任何上游请求前拒绝。
		{name: "unbound-model", model: "allowed", testType: "video", change: func(a *Account) {
			delete(a.Credentials, "video_endpoints")
			a.Credentials["video_model_protocols"] = map[string]any{"other": map[string]any{"protocol": "compat.legacy_v1"}}
		}},
		{name: "invalid-protocol-binding", model: "allowed", testType: "video", change: func(a *Account) {
			delete(a.Credentials, "video_endpoints")
			a.Credentials["video_model_protocols"] = map[string]any{"allowed": map[string]any{"protocol": "seedance.unknown_v9"}}
		}},
		{name: "legacy-binding-to-disabled-endpoint", model: "allowed", testType: "video", change: func(a *Account) {
			a.Credentials["video_model_bindings"] = map[string]any{"allowed": "seedance"}
		}},
		{name: "whitelist", model: "forbidden", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat"}},
		{name: "wildcard", model: "*", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat"}},
		{name: "implicit", model: "allowed", options: AccountTestOptions{VideoEndpoint: "compat"}},
		{name: "text", model: "allowed", testType: "text", options: AccountTestOptions{VideoEndpoint: "compat"}},
		{name: "text-endpoint", model: "allowed", testType: "video", endpoint: "responses", options: AccountTestOptions{VideoEndpoint: "compat"}},
		{name: "compact", model: "allowed", testType: "video", mode: "compact", options: AccountTestOptions{VideoEndpoint: "compat"}},
		{name: "zero-duration", model: "allowed", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat", Duration: videoAccountTestInt(0)}},
		{name: "negative-duration", model: "allowed", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat", Duration: videoAccountTestInt(-1)}},
		{name: "large-duration", model: "allowed", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat", Duration: videoAccountTestInt(61)}},
		{name: "invalid-resolution", model: "allowed", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat", Resolution: "720p\nother"}},
		{name: "invalid-ratio", model: "allowed", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat", Ratio: "0:1"}},
		{name: "external-image", model: "allowed", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat", ImageDataURL: "http://127.0.0.1/private"}},
		{name: "invalid-image", model: "allowed", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat", ImageDataURL: "data:image/png;base64,aGVsbG8="}},
		{name: "audio", model: "allowed", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat", AudioDataURL: "invalid"}},
		{name: "wrong-account-type", model: "allowed", testType: "video", options: AccountTestOptions{VideoEndpoint: "compat"}, change: func(a *Account) { a.Type = AccountTypeOAuth }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			account.Credentials["model_whitelist"] = []string{"allowed"}
			if test.change != nil {
				test.change(account)
			}
			svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
				t.Fatal("unexpected paid request")
				return nil, nil
			})
			c, recorder := grokModesContext(context.Background())
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, test.model, "test", test.testType, test.mode, test.endpoint, test.options))
			require.Empty(t, upstream.requests)
			require.Contains(t, recorder.Body.String(), `"type":"error"`)
			require.NotContains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestVideoAccountTestNeverRetriesFailedOrAmbiguousCreate(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		err        error
	}{
		{"http-error", `{"error":{"message":"secret-override"}}`, 503, nil},
		{"http-error-with-id", `{"id":"accepted-task","status":"processing"}`, 503, nil},
		{"redirect", `{"location":"https://other.example"}`, 307, nil},
		{"business-error", `{"error":{"message":"secret-override"}}`, 200, nil},
		{"invalid-json", `not-json-secret-override`, 200, nil},
		{"missing-id", `{"status":"pending"}`, 200, nil},
		{"conflicting-id", `{"id":"one","task_id":"two","status":"pending"}`, 200, nil},
		{"duplicate-id", `{"id":"one","id":"two","status":"pending"}`, 200, nil},
		{"unsafe-id", `{"id":"../private","status":"pending"}`, 200, nil},
		{"transport", ``, 0, errors.New("connection reset")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			// 允许安全错误摘要后，使用实际认证头值验证脱敏，而不是禁止所有可读错误信息。
			account.Credentials["header_override_enabled"] = true
			account.Credentials["header_overrides"] = map[string]any{"X-Custom-Auth": "secret-override"}
			svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
				if test.err != nil {
					return nil, test.err
				}
				return videoAccountTestJSON(test.status, test.body), nil
			})
			c, recorder := grokModesContext(context.Background())
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", AccountTestOptions{VideoEndpoint: "compat"}))
			require.Len(t, upstream.requests, 1)
			require.NotContains(t, recorder.Body.String(), `"success":true`)
			require.NotContains(t, recorder.Body.String(), "secret-override")
			if test.name == "http-error-with-id" {
				require.Contains(t, recorder.Body.String(), "accepted-task")
			}
		})
	}
}

func TestVideoAccountTestPollFailureAndCancellationNeverResubmit(t *testing.T) {
	for _, body := range []string{
		`{"id":"task-1","status":"failed"}`, `{"id":"task-1","status":"cancelled"}`, `{"id":"task-1","status":"expired"}`,
		`{"id":"another-task","status":"completed","video":{"url":"https://cdn.example/foreign.mp4"}}`,
		`{"id":"task-1","status":"completed","error":{"message":"secret"}}`,
		`{"id":"task-1","status":"completed"}`, `{"id":"task-1","status":"completed","video":{"url":"http://127.0.0.1/private"}}`,
		`{"id":"task-1"}`, `{"id":"task-1","status":"processing"}`, `{"status":"pending"}`, "<html>error</html>",
	} {
		t.Run(body, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointCompat)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			svc, upstream := videoAccountTestService(account, func(r *http.Request, payload []byte) (*http.Response, error) {
				if r.Method == http.MethodPost {
					require.Equal(t, int64(5), gjson.GetBytes(payload, "duration").Int())
					require.False(t, gjson.GetBytes(payload, "resolution").Exists())
					return videoAccountTestJSON(202, `{"id":"task-1","status":"pending"}`), nil
				}
				cancel()
				return videoAccountTestJSON(200, body), nil
			})
			c, recorder := grokModesContext(ctx)
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", AccountTestOptions{VideoEndpoint: "compat"}))
			require.Len(t, upstream.requests, 2)
			require.NotContains(t, recorder.Body.String(), `"success":true`)
			require.NotContains(t, recorder.Body.String(), "foreign.mp4")
		})
	}
}

func TestVideoAccountTestOpenAIContentUsesFixedAuthenticatedPath(t *testing.T) {
	for _, variant := range []string{"video", "html", "private-redirect", "empty"} {
		t.Run(variant, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			svc, upstream := videoAccountTestService(account, func(r *http.Request, _ []byte) (*http.Response, error) {
				require.True(t, HTTPUpstreamRedirectsDisabled(r.Context()))
				if r.Method == http.MethodPost {
					return videoAccountTestJSON(202, `{"id":"task-1","status":"queued"}`), nil
				}
				if !strings.HasSuffix(r.URL.Path, "/content") {
					return videoAccountTestJSON(200, `{"id":"task-1","status":"completed"}`), nil
				}
				require.Equal(t, "/prefix/v1/videos/task-1/content", r.URL.Path)
				require.Equal(t, "Bearer fixture-secret", r.Header.Get("Authorization"))
				resp := videoAccountTestJSON(200, "test-video")
				resp.Header.Set("Content-Type", "video/webm")
				switch variant {
				case "html":
					resp.Header.Set("Content-Type", "text/html")
				case "empty":
					resp.Body = io.NopCloser(strings.NewReader(""))
				case "private-redirect":
					resp.StatusCode = 302
					resp.Header.Set("Location", "http://127.0.0.1/private")
				}
				return resp, nil
			})
			c, recorder := grokModesContext(context.Background())
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", AccountTestOptions{VideoEndpoint: "openai_videos"})
			if variant == "video" {
				require.NoError(t, err)
				require.Contains(t, recorder.Body.String(), "data:video/webm;base64,")
			} else {
				require.Error(t, err)
				require.NotContains(t, recorder.Body.String(), `"success":true`)
			}
			require.Len(t, upstream.requests, 3)
			require.NotContains(t, recorder.Body.String(), "fixture-secret")
		})
	}
}

func TestVideoAccountTestLegacyEntryCannotGenerate(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointCompat)
	svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
		t.Fatal("legacy tests cannot generate video")
		return nil, nil
	})
	c, recorder := grokModesContext(context.Background())
	require.Error(t, svc.TestAccountConnection(c, account.ID, "m", "", ""))
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), `"type":"error"`)
}

func TestVideoAccountTestBusinessErrorsCannotBecomeSuccessfulObservations(t *testing.T) {
	for _, test := range []struct {
		endpoint VideoEndpoint
		body     string
	}{
		{VideoEndpointKling, `{"code":1001,"data":{"task_id":"task-1","task_status":"succeed"}}`},
		{VideoEndpointMiniMax, `{"base_resp":{"status_code":1001},"task":{"id":"task-1","status":"completed"}}`},
		{VideoEndpointWan, `{"code":"InvalidParameter","output":{"task_id":"task-1","task_status":"SUCCEEDED"}}`},
		{VideoEndpointWan, `{"output":{"code":"Failed","task_id":"task-1","task_status":"SUCCEEDED"}}`},
	} {
		t.Run(string(test.endpoint)+test.body, func(t *testing.T) {
			require.Error(t, videoAccountTestResponseError(test.endpoint, &VideoUpstreamResponse{StatusCode: 200, Body: []byte(test.body)}))
		})
	}
	// 兼容中继的 code 扩展不被当成其它供应商的业务码。
	require.NoError(t, videoAccountTestResponseError(VideoEndpointCompat, &VideoUpstreamResponse{StatusCode: 200, Body: []byte(`{"code":200,"status":"completed"}`)}))
}

func TestVideoAccountTestKlingRespectsConfiguredOperations(t *testing.T) {
	for _, test := range []struct {
		name, configured, wantPath, imageField string
		image, rejected                        bool
	}{
		// 操作路径只来自模型绑定的 path；未配置路径的模型没有绑定，测试在请求前拒绝，不再按是否带图推断默认操作。
		{name: "unbound-text", rejected: true},
		{name: "unbound-reference", image: true, rejected: true},
		{name: "configured-official-text", configured: "/v1/videos/text2video", wantPath: "/v1/videos/text2video"},
		{name: "configured-official-reference", configured: "/v1/videos/omni-video", wantPath: "/v1/videos/omni-video", imageField: "image_list.0.image_url", image: true},
		{name: "configured-reference", configured: "/image-to-video/{model}", wantPath: "/image-to-video/m", imageField: "image", image: true},
		{name: "configured-text", configured: "/text-to-video/{model}", wantPath: "/text-to-video/m"},
		{name: "image-required", configured: "/image-to-video/{model}", rejected: true},
		{name: "text-rejects-image", configured: "/text-to-video/{model}", image: true, rejected: true},
		{name: "invalid-template", configured: "/private/{model}", rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointKling)
			if test.configured != "" {
				account.Credentials["video_model_paths"] = map[string]string{"m": test.configured}
			}
			options := AccountTestOptions{VideoEndpoint: "kling"}
			if test.image {
				options.ImageDataURL = grokModesImageDataURL(t, 8)
			}
			svc, upstream := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "/prefix"+test.wantPath, r.URL.Path)
				if test.image {
					require.Equal(t, options.ImageDataURL, gjson.GetBytes(body, test.imageField).String())
				}
				return videoAccountTestJSON(200, `{"data":{"task_id":"task-1","task_status":"succeed","task_result":{"videos":[{"url":"https://cdn.example/video.mp4"}]}}}`), nil
			})
			c, _ := grokModesContext(context.Background())
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", options)
			if test.rejected {
				require.Error(t, err)
				require.Empty(t, upstream.requests)
			} else {
				require.NoError(t, err)
				require.Len(t, upstream.requests, 1)
			}
		})
	}
}

// 可美管理员试跑与正式请求使用相同的原生 contents；操作路径只来自模型绑定，未配置路径时请求前拒绝。
func TestVideoAccountTestKemeiKlingOperations(t *testing.T) {
	for _, test := range []struct {
		name, configured, wantPath string
		image, rejected            bool
	}{
		{name: "unbound-text", rejected: true},
		{name: "unbound-image", image: true, rejected: true},
		{name: "configured-text", configured: "/text-to-video/{model}", wantPath: "/text-to-video/m"},
		{name: "text-rejects-image", configured: "/text-to-video/{model}", image: true, rejected: true},
		{name: "configured-image", configured: "/image-to-video/{model}", wantPath: "/image-to-video/m", image: true},
		{name: "image-required", configured: "/image-to-video/{model}", rejected: true},
		{name: "omni-text", configured: "/omni-video/{model}", wantPath: "/omni-video/m"},
		{name: "omni-image", configured: "/omni-video/{model}", wantPath: "/omni-video/m", image: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointKling)
			account.Credentials["video_endpoint_adapters"] = map[string]string{"kling": VideoProtocolAdapterKemei}
			if test.configured != "" {
				account.Credentials["video_model_paths"] = map[string]string{"m": test.configured}
			}
			options := AccountTestOptions{VideoEndpoint: "kling", Duration: videoAccountTestInt(6), Resolution: "720p", Ratio: "16:9"}
			if test.image {
				options.ImageDataURL = grokModesImageDataURL(t, 8)
			}
			svc, upstream := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "/prefix"+test.wantPath, r.URL.Path)
				require.False(t, gjson.GetBytes(body, "model").Exists(), "Kling model is carried in the operation path")
				require.Equal(t, int64(6), gjson.GetBytes(body, "settings.duration").Int())
				require.Equal(t, "720p", gjson.GetBytes(body, "settings.resolution").String())
				require.Equal(t, "16:9", gjson.GetBytes(body, "settings.aspect_ratio").String())
				for _, field := range []string{"image_list", "image", "images", "duration", "resolution", "aspect_ratio"} {
					require.False(t, gjson.GetBytes(body, field).Exists(), "unexpected untranslated %s", field)
				}
				if strings.HasPrefix(test.wantPath, "/text-to-video/") {
					require.Equal(t, "a distinct test prompt", gjson.GetBytes(body, "prompt").String())
					require.False(t, gjson.GetBytes(body, "contents").Exists())
				} else {
					contents := gjson.GetBytes(body, "contents").Array()
					require.Equal(t, "prompt", contents[0].Get("type").String())
					require.Equal(t, "a distinct test prompt", contents[0].Get("text").String())
					if test.image {
						require.Len(t, contents, 2)
						require.Equal(t, "first_frame", contents[1].Get("type").String())
						require.Equal(t, options.ImageDataURL, contents[1].Get("url").String())
					} else {
						require.Len(t, contents, 1)
					}
				}
				return videoAccountTestJSON(200, `{"id":"task-1","status":"completed","data":[{"url":"https://cdn.example/video.mp4"}],"duration":6,"resolution":"720p"}`), nil
			})
			c, recorder := grokModesContext(context.Background())
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "m", "a distinct test prompt", "video", "", "", options)
			if test.rejected {
				require.Error(t, err)
				require.Empty(t, upstream.requests)
			} else {
				require.NoError(t, err)
				require.Len(t, upstream.requests, 1)
				require.Contains(t, recorder.Body.String(), `"success":true`)
			}
		})
	}
}

// 大流以固定字节产生，不创建上游服务器或在测试夹具中预先分配大块内存。
type videoAccountTestLargeBody struct {
	remaining int64
	read      int64
	closed    bool
}

func (b *videoAccountTestLargeBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.remaining)
	clear(p[:n])
	b.remaining -= n
	b.read += n
	return int(n), nil
}

func (b *videoAccountTestLargeBody) Close() error { b.closed = true; return nil }

func TestVideoAccountTestContentSizeIsBoundedAndClosed(t *testing.T) {
	for _, knownLength := range []bool{true, false} {
		t.Run(map[bool]string{true: "declared-too-large", false: "stream-too-large"}[knownLength], func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			body := &videoAccountTestLargeBody{remaining: maxGrokTestVideoBytes + 1}
			svc, upstream := videoAccountTestService(account, func(r *http.Request, _ []byte) (*http.Response, error) {
				if r.Method == http.MethodPost {
					return videoAccountTestJSON(200, `{"id":"task-1","status":"completed"}`), nil
				}
				require.Equal(t, "/prefix/v1/videos/task-1/content", r.URL.Path)
				length := int64(-1)
				if knownLength {
					length = maxGrokTestVideoBytes + 1
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"video/mp4"}}, Body: body, ContentLength: length}, nil
			})
			c, recorder := grokModesContext(context.Background())
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", AccountTestOptions{VideoEndpoint: "openai_videos"}))
			require.True(t, body.closed)
			if knownLength {
				require.Zero(t, body.read)
			} else {
				require.Equal(t, int64(maxGrokTestVideoBytes+1), body.read)
			}
			require.Len(t, upstream.requests, 2)
			require.NotContains(t, recorder.Body.String(), `"type":"video"`)
		})
	}
}
