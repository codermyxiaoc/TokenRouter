//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// videoProfileAISettingStub 是内存设置仓储，只实现 AI 配置读写需要的方法。
type videoProfileAISettingStub struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
}

func (s *videoProfileAISettingStub) GetValue(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (s *videoProfileAISettingStub) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

// AI 配置不回显密钥，留空保留原密钥，启用时必须配齐地址、模型与密钥。
func TestVideoProfileAIConfig(t *testing.T) {
	settings := &videoProfileAISettingStub{}
	svc := NewVideoProfileAIService(settings)
	view, err := svc.GetConfig(t.Context())
	require.NoError(t, err)
	require.False(t, view.Enabled)
	require.False(t, view.APIKeyConfigured)
	_, err = svc.UpdateConfig(t.Context(), VideoProfileAIConfigInput{Enabled: true, BaseURL: "https://llm.example/v1", Model: "m"})
	require.Error(t, err, "启用时必须提供密钥")
	_, err = svc.UpdateConfig(t.Context(), VideoProfileAIConfigInput{BaseURL: "https://user:pw@llm.example/v1"})
	require.Error(t, err)
	view, err = svc.UpdateConfig(t.Context(), VideoProfileAIConfigInput{Enabled: true, BaseURL: "https://llm.example/v1/", Model: "m", APIKey: "sk-secret"})
	require.NoError(t, err)
	require.True(t, view.APIKeyConfigured)
	require.Equal(t, "https://llm.example/v1", view.BaseURL)
	raw, _ := json.Marshal(view)
	require.NotContains(t, string(raw), "sk-secret")
	view, err = svc.UpdateConfig(t.Context(), VideoProfileAIConfigInput{Enabled: true, BaseURL: "https://llm.example/v1", Model: "m2"})
	require.NoError(t, err)
	require.True(t, view.APIKeyConfigured, "留空密钥保留原值")
	view, err = svc.UpdateConfig(t.Context(), VideoProfileAIConfigInput{BaseURL: "https://llm.example/v1", Model: "m2", ClearAPIKey: true})
	require.NoError(t, err)
	require.False(t, view.APIKeyConfigured)
	endpoint, err := videoProfileAIEndpoint("https://llm.example/v1/chat/completions")
	require.NoError(t, err)
	require.Equal(t, "https://llm.example/v1/chat/completions", endpoint)
}

// 生成结果先按模板校验，失败时把错误反馈给模型修正；最终只返回草稿，不保存模板。
func TestVideoProfileAIGenerateRetriesUntilValid(t *testing.T) {
	valid, _ := json.Marshal(videoPresetGenericFlat("/v1/video/generations", "/v1/video/generations/{task_id}", ""))
	replies := []string{
		"```json\n" + `{"profile_key":"vendor-x","name":"厂商 X","notes":"n","spec":{"auth":{"scheme":"basic"}}}` + "\n```",
		`{"profile_key":"vendor-x","name":"厂商 X","notes":"按文档映射","spec":` + string(valid) + `}`,
	}
	var calls int
	var lastBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer sk-secret", r.Header.Get("Authorization"))
		lastBody, _ = io.ReadAll(r.Body)
		reply := replies[min(calls, len(replies)-1)]
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": reply}}}})
	}))
	defer server.Close()
	settings := &videoProfileAISettingStub{}
	svc := NewVideoProfileAIService(settings)
	_, err := svc.Generate(t.Context(), VideoProfileAIGenerateInput{Document: "doc"})
	require.Error(t, err, "未启用时拒绝")
	_, err = svc.UpdateConfig(t.Context(), VideoProfileAIConfigInput{Enabled: true, BaseURL: server.URL + "/v1", Model: "m", APIKey: "sk-secret"})
	require.NoError(t, err)
	result, err := svc.Generate(t.Context(), VideoProfileAIGenerateInput{Document: "POST /v1/video/generations ...", Instructions: "只支持文生"})
	require.NoError(t, err)
	require.True(t, result.Valid, result.ValidationError)
	require.Equal(t, 2, result.Attempts)
	require.Equal(t, "vendor-x", result.ProfileKey)
	require.Equal(t, "厂商 X", result.Name)
	var spec VideoProtocolProfileSpec
	require.NoError(t, json.Unmarshal(result.Spec, &spec))
	require.NoError(t, spec.Validate())
	// 第二轮请求携带上一轮的校验错误，系统提示包含预置模板范例。
	messages := gjson.GetBytes(lastBody, "messages").Array()
	require.Len(t, messages, 4)
	require.Contains(t, messages[3].Get("content").String(), "模板校验失败")
	require.Contains(t, messages[0].Get("content").String(), "kemei-kling-omni")
	require.Contains(t, messages[1].Get("content").String(), "只支持文生")
}

// 上游模型持续给出无效模板时返回最后的草稿与校验错误，不报 500；上游错误不回显密钥。
func TestVideoProfileAIGenerateReportsInvalidDraftAndUpstreamErrors(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key sk-secret"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"name":"x","spec":{"create":{},"poll":{}}}`}}}})
	}))
	defer server.Close()
	svc := NewVideoProfileAIService(&videoProfileAISettingStub{})
	_, err := svc.UpdateConfig(t.Context(), VideoProfileAIConfigInput{Enabled: true, BaseURL: server.URL, Model: "m", APIKey: "sk-secret"})
	require.NoError(t, err)
	result, err := svc.Generate(t.Context(), VideoProfileAIGenerateInput{Document: "doc"})
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.NotEmpty(t, result.ValidationError)
	require.Equal(t, videoProfileAIMaxAttempts, result.Attempts)
	status = http.StatusUnauthorized
	_, err = svc.Generate(t.Context(), VideoProfileAIGenerateInput{Document: "doc"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "sk-secret")
}

// 系统创建且未被接管的预置模板随内置定义升级；管理员保存过的键名不覆盖。
func TestVideoProtocolPresetUpgrade(t *testing.T) {
	repo := &videoProfileMemoryRepo{}
	svc := NewVideoProtocolProfileService(repo)
	// 模拟旧版本：kemei-wan 缺少创建请求头，metaso-h3 已被管理员保存过新版本。
	oldWan := videoPresetKemeiWan()
	oldWan.Create.Headers = nil
	_, err := svc.Save(t.Context(), VideoProtocolProfileInput{ProfileKey: "kemei-wan", Name: "可美万相", Spec: oldWan, Activate: true}, 0)
	require.NoError(t, err)
	admin := videoPresetMetasoH3()
	admin.Poll.IntervalSeconds = 30
	_, err = svc.Save(t.Context(), VideoProtocolProfileInput{ProfileKey: "metaso-h3", Name: "H3", Spec: admin, Activate: true}, 7)
	require.NoError(t, err)
	_, err = EnsureVideoProtocolPresets(t.Context(), svc)
	require.NoError(t, err)
	wan, err := svc.Resolve(t.Context(), "kemei-wan", 0)
	require.NoError(t, err)
	require.Equal(t, 2, wan.Version)
	require.Equal(t, "enable", wan.Spec.Create.Headers["X-DashScope-Async"])
	h3, err := svc.Resolve(t.Context(), "metaso-h3", 0)
	require.NoError(t, err)
	require.Equal(t, 1, h3.Version)
	require.Equal(t, 30, h3.Spec.Poll.IntervalSeconds, "管理员保存的版本不能被覆盖")
	created, err := EnsureVideoProtocolPresets(t.Context(), svc)
	require.NoError(t, err)
	require.Zero(t, created, "已是最新定义时不再新建版本")
}

// 模板协议的创建请求头只随 POST 发送，查询不携带。
func TestVideoProfileCreateHeadersOnlyOnPost(t *testing.T) {
	target := videoPresetTarget(t, "kemei-wan", "wan2.6")
	var methods = map[string]string{}
	fixture := &videoAccountTestHTTP{handle: func(r *http.Request, _ []byte) (*http.Response, error) {
		methods[r.Method] = r.Header.Get("X-DashScope-Async")
		return videoAccountTestJSON(http.StatusOK, `{"output":{"task_id":"t1","task_status":"PENDING"}}`), nil
	}}
	svc := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
	account := videoFixtureAccount(VideoEndpointWan)
	account.ID = target.AccountID
	body, _, err := PrepareVideoProtocolRequest([]byte(`{"model":"wan2.6","prompt":"p"}`), "", target)
	require.NoError(t, err)
	_, err = svc.Submit(t.Context(), &VideoUpstreamSelection{Account: account, Target: target, Body: body})
	require.NoError(t, err)
	_, err = svc.Poll(t.Context(), account, target, "t1")
	require.NoError(t, err)
	require.Equal(t, "enable", methods[http.MethodPost])
	require.Empty(t, methods[http.MethodGet])
}

// 管理员测试：模板声明内容路径时，完成却没有直链也能用原账号凭据下载预览，不再误报 no usable video URL。
func TestVideoAccountTestProfileContentFallback(t *testing.T) {
	target := videoPresetTarget(t, "metaso-h3", "h3")
	path, credentialed, err := videoContentPath(target, "2115426782870712320")
	require.NoError(t, err)
	require.True(t, credentialed)
	require.Equal(t, "/v1/videos/2115426782870712320/content", path)
}

// 完成但计费维度到期仍不可信时按未交付释放，成品地址在结算前不对外返回。
func TestVideoUnreliableCompletionReleasesAfterDeadline(t *testing.T) {
	s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideo)
	key.Group.RateMultiplier = 1
	upstream.selection.Target.Endpoint = VideoEndpointMiniMax
	upstream.selection.Metadata.DurationSeconds = 5
	created, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{
		Native: true, InboundProtocol: string(VideoEndpointMiniMax), NativePath: "/v2/video_generation",
		Body: []byte(`{"model":"video-model","resolution":"720p","duration":5}`),
	})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), created.LocalTaskID)
	require.NoError(t, err)
	// 完成包给出与报价不同的档位：进入待核对，地址不得返回给用户。
	body := []byte(`{"task":{"id":"upstream-task","status":"succeeded","resolution":"1080p","content":{"url":"https://cdn.example/video.mp4"},"usage":{"output_seconds":5}}}`)
	upstream.poll = ExtractVideoUpstreamResponse(VideoEndpointMiniMax, body, task.UpstreamTaskID)
	upstream.poll.StatusCode, upstream.poll.Body = http.StatusOK, body
	s.advance(context.Background(), task)
	require.Equal(t, "completed", task.Status)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Zero(t, billing.captures)
	response := videoTaskResponse(task, false)
	require.Empty(t, gjson.GetBytes(response.Body, "data").Array(), "待核对任务不能返回成品地址")
	// 超过最长处理时间仍不可信：失败并释放一次预扣，不扣费。
	task.CreatedAt = time.Now().Add(-48 * time.Hour)
	s.advance(context.Background(), task)
	require.Equal(t, "failed", task.Status)
	require.Equal(t, "released", task.BillingStatus)
	require.Equal(t, 1, billing.releases)
	require.Zero(t, billing.captures)
	require.True(t, strings.Contains(task.ErrorMessage, "预扣已释放"))
}

// 旧方案改绑为模板后，创作台已保存在原端点下的模型设置继续生效，参考图大小提示来自模板能力。
func TestCreativeVideoCatalogKeepsLegacySettingsForTemplateBindings(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		endpoint VideoEndpoint
		limit    int64
	}{
		{"profile:metaso-h3", VideoEndpointOpenAIVideos, CreativeVideoH3ReferenceImageLimit},
		{"profile:kemei-seedance", VideoEndpointSeedance, 0},
		{"profile:generic-flat", VideoEndpointCompat, 0},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			s, e, _, _, _ := creativeVideoTestFixture(t)
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.GroupIDs = []int64{e.Params.GroupID}
			account.Credentials = map[string]any{"api_key": "k", "base_url": "https://relay.example",
				"model_mapping":         map[string]any{e.Params.Model: "upstream-model"},
				"video_model_protocols": map[string]any{"upstream-model": map[string]any{"protocol": tc.protocol}}}
			s.public.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{e.Params.GroupID: {*account}}}
			s.public.Settings.(*creativeFakeSettingReader).models[0].VideoEndpoint = tc.endpoint
			models, err := s.public.ListModels(context.Background(), e.UserID)
			require.NoError(t, err)
			require.Len(t, models.Data, 1, "模型不能因改绑模板从创作台消失")
			require.Equal(t, tc.endpoint, models.Data[0].VideoEndpoint)
			require.Equal(t, tc.limit, models.Data[0].VideoCapabilities.MaxReferenceImageBytes)
		})
	}
}

// 画布按各预置模板提交的规范请求都能生成上游请求，且计价维度与画布参数一致。
func TestCreativeVideoCanvasRequestsWorkWithPresetTemplates(t *testing.T) {
	for _, tc := range []struct {
		preset, model, resolution, ratio string
		duration                         float64
		withImage                        bool
	}{
		{"metaso-h3", "h3", "768p", "16:9", 6, true},
		{"yezonghui", "yz", "720p", "16:9", 5, false},
		{"generic-flat", "m", "720p", "16:9", 5, true},
		{"generic-flat-videos", "m", "1080p", "9:16", 8, false},
		{"kemei-compat", "m", "720p", "16:9", 5, true},
		{"kemei-seedance", "seed", "1080p", "16:9", 5, true},
		{"kemei-minimax", "mm", "768p", "16:9", 6, false},
		{"kemei-wan", "wan", "720p", "16:9", 5, true},
		{"kemei-kling-text", "kling-v3", "1080p", "16:9", 10, false},
		{"kemei-kling-image", "kling-v3", "720p", "16:9", 5, true},
		{"kemei-kling-omni", "kling-v3", "720p", "16:9", 5, true},
		{"kemei-vidu-img2video", "viduq3-pro", "720p", "16:9", 5, true},
		{"kemei-vidu-text2video", "viduq3-pro", "720p", "16:9", 5, false},
	} {
		t.Run(tc.preset, func(t *testing.T) {
			e := creativeVideoTestExecution()
			e.Params.Model, e.Params.Endpoint = tc.model, string(CreativeVideoEndpointUnified)
			e.Params.Duration, e.Params.Resolution, e.Params.AspectRatio = tc.duration, tc.resolution, tc.ratio
			caps := &CreativeVideoCapabilities{Durations: []float64{tc.duration}, Resolutions: []string{tc.resolution}, AspectRatios: []string{tc.ratio}, MaxReferenceImages: 1}
			model := &CreativeModelPublic{Model: tc.model, VideoEndpoint: CreativeVideoEndpointUnified, VideoCapabilities: caps}
			var assets []CreativeCanvasInputAsset
			if tc.withImage {
				e.Params.References = []CreativeCanvasReference{{AssetID: "asset-one"}}
				assets = []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{ID: "asset-one", UserID: e.UserID, CanvasID: e.CanvasID, MediaType: "image", MIMEType: "image/png"}, Content: makeTestPNG(t, 16, 16)}}
			}
			_, canonical, _, err := creativeVideoCanvasRequest(e, model, assets, 8000)
			require.NoError(t, err)
			canonical.Model = tc.model
			target := videoPresetTarget(t, tc.preset, tc.model)
			body, err := videoCanonicalBodyForTarget(canonical, target)
			require.NoError(t, err)
			if tc.withImage {
				// 只收公网素材的模板由画布先发布短期链接，这里用公网地址代替发布结果。
				body = []byte(strings.Replace(string(body), gjson.GetBytes(body, "images.0.url").String(), "https://img.example/a.png", 1))
			}
			_, metadata, err := PrepareVideoProtocolRequest(body, "", target)
			require.NoError(t, err)
			require.Equal(t, tc.duration, metadata.DurationSeconds)
			tier, _ := NormalizeVideoPriceResolution(metadata.Resolution)
			require.Equal(t, tc.resolution, tier)
			if tc.withImage {
				require.Equal(t, 1, *metadata.ReferenceImageCount)
			}
		})
	}
}
