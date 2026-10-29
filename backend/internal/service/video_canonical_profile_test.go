//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 统一请求严格解析：别名换算、冲突与未知字段都在选号前拒绝。
func TestVideoCanonicalRequestParsing(t *testing.T) {
	req, err := ParseVideoCanonicalRequest([]byte(`{"model":"m","prompt":"p","seconds":"8","size":"1280x720","input_reference":{"image_url":"https://a.example/i.png"},"extra":{"camera":{"zoom":1}}}`))
	require.NoError(t, err)
	require.Equal(t, 8.0, *req.Duration)
	require.Equal(t, "1280x720", req.Size)
	require.Equal(t, []VideoCanonicalMedia{{URL: "https://a.example/i.png", Role: VideoMediaRoleFirstFrame}}, req.Images)
	require.JSONEq(t, `{"zoom":1}`, string(req.Extra["camera"]))
	req, err = ParseVideoCanonicalRequest([]byte(`{"model":"m","images":["https://a/1",{"url":"https://a/2","role":"last_frame"}],"videos":[{"url":"https://a/v","role":"reference_video"}]}`))
	require.NoError(t, err)
	require.Len(t, req.Images, 2)
	require.Equal(t, VideoMediaRoleLastFrame, req.Images[1].Role)
	ratioReq, err := ParseVideoCanonicalRequest([]byte(`{"model":"m","ratio":"9:16"}`))
	require.NoError(t, err)
	require.Equal(t, "9:16", ratioReq.AspectRatio, "旧客户端的 ratio 换算为 aspect_ratio")
	require.Len(t, req.Videos, 1)
	for name, body := range map[string]string{
		"unknown_field":       `{"model":"m","content":[]}`,
		"missing_model":       `{"prompt":"p"}`,
		"duration_conflict":   `{"model":"m","duration":5,"seconds":"8"}`,
		"negative_duration":   `{"model":"m","duration":-1}`,
		"images_and_ref":      `{"model":"m","images":["https://a"],"input_reference":{"image_url":"https://b"}}`,
		"bad_role":            `{"model":"m","images":[{"url":"https://a","role":"reference_video"}]}`,
		"video_image_role":    `{"model":"m","videos":[{"url":"https://a","role":"first_frame"}]}`,
		"seed_fraction":       `{"model":"m","seed":1.5}`,
		"seed_unsafe_integer": `{"model":"m","seed":9007199254740993}`,
		"seed_exponent":       `{"model":"m","seed":1e100}`,
		"extra_not_object":    `{"model":"m","extra":[1]}`,
		"duplicate_key":       `{"model":"m","model":"n"}`,
		"audio_string":        `{"model":"m","generate_audio":"yes"}`,
		"item_unknown_member": `{"model":"m","images":[{"url":"https://a","weight":1}]}`,
		"ratio_conflict":      `{"model":"m","ratio":"16:9","aspect_ratio":"9:16"}`,
	} {
		_, err := ParseVideoCanonicalRequest([]byte(body))
		require.Error(t, err, name)
	}
	// 安全整数边界内的 seed 精确保留，不经 float64 舍入。
	bounded, err := ParseVideoCanonicalRequest([]byte(`{"model":"m","seed":9007199254740991}`))
	require.NoError(t, err)
	require.Equal(t, int64(9007199254740991), *bounded.Seed)
}

// P2-05：模板数值转换拒绝溢出、非有限数和超出安全范围的整数，不能静默改写后发往上游。
func TestVideoProfileValueConversionRejectsOverflow(t *testing.T) {
	for _, tc := range []struct {
		value any
		kind  string
	}{
		{json.RawMessage(`1e100`), "int"},
		{"+Inf", "int"},
		{"NaN", "int"},
		{json.RawMessage(`9007199254740993`), "int"},
		{"9007199254740993", "string_int"},
		{"+Inf", "string_number"},
		{"-Inf", "number"},
		{"NaN", "string_number"},
	} {
		_, err := convertVideoProfileValue(tc.value, tc.kind)
		require.Error(t, err, "%v as %s", tc.value, tc.kind)
	}
	for _, tc := range []struct {
		value any
		kind  string
		want  any
	}{
		{json.RawMessage(`9007199254740991`), "int", int64(9007199254740991)},
		{"42", "int", int64(42)},
		{json.RawMessage(`5`), "string_int", "5"},
		{json.RawMessage(`2.5`), "string_number", "2.5"},
	} {
		got, err := convertVideoProfileValue(tc.value, tc.kind)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
}

// 内置协议按端点转换；端点无法表达的字段直接拒绝，可美方案保留素材角色。
func TestVideoBuiltinRequestBodyConversions(t *testing.T) {
	duration := 5.0
	base := VideoCanonicalRequest{Model: "m", Prompt: "p", Duration: &duration, Resolution: "720p", AspectRatio: "16:9",
		Images: []VideoCanonicalMedia{{URL: "https://a/first.png", Role: VideoMediaRoleFirstFrame}, {URL: "https://a/last.png", Role: VideoMediaRoleLastFrame}}}
	body, err := videoBuiltinRequestBody(&base, VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointCompat, ProtocolAdapter: VideoProtocolAdapterKemei})
	require.NoError(t, err)
	require.Equal(t, "last_frame", gjson.GetBytes(body, "images.1.role").String())
	require.Equal(t, "16:9", gjson.GetBytes(body, "ratio").String())
	// 普通兼容协议不能表达尾帧，必须拒绝而不是丢掉角色。
	_, err = videoBuiltinRequestBody(&base, VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointCompat, ProtocolAdapter: VideoProtocolAdapterLegacy})
	require.Equal(t, "VIDEO_PARAMETER_UNSUPPORTED", infraerrors.Reason(err))
	body, err = videoBuiltinRequestBody(&base, VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointSeedance, ProtocolAdapter: VideoProtocolAdapterSeedance})
	require.NoError(t, err)
	require.Equal(t, "p", gjson.GetBytes(body, "content.0.text").String())
	require.Equal(t, "last_frame", gjson.GetBytes(body, "content.2.role").String())
	single := base
	single.Images = base.Images[:1]
	body, err = videoBuiltinRequestBody(&single, VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointWan, ProtocolAdapter: VideoProtocolAdapterWan})
	require.NoError(t, err)
	require.Equal(t, "https://a/first.png", gjson.GetBytes(body, "input.img_url").String())
	require.Equal(t, 5.0, gjson.GetBytes(body, "parameters.duration").Float())
	body, err = videoBuiltinRequestBody(&single, VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointKling, ProtocolAdapter: VideoProtocolAdapterLegacy, CreatePath: "/image-to-video/m"})
	require.NoError(t, err)
	require.Equal(t, "https://a/first.png", gjson.GetBytes(body, "image").String())
	body, err = videoBuiltinRequestBody(&single, VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointKling, ProtocolAdapter: VideoProtocolAdapterLegacy, CreatePath: "/omni-video/m"})
	require.NoError(t, err)
	require.Equal(t, "https://a/first.png", gjson.GetBytes(body, "image_list.0.image_url").String())
	sized := VideoCanonicalRequest{Model: "m", Size: "1280x720"}
	_, err = videoBuiltinRequestBody(&sized, VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointSeedance, ProtocolAdapter: VideoProtocolAdapterSeedance})
	require.Error(t, err)
	extra := VideoCanonicalRequest{Model: "m", Extra: map[string]json.RawMessage{"model": json.RawMessage(`"x"`)}}
	_, err = videoBuiltinRequestBody(&extra, VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointCompat, ProtocolAdapter: VideoProtocolAdapterLegacy})
	require.Equal(t, "VIDEO_PARAMETER_CONFLICT", infraerrors.Reason(err))
}

// videoTestProfileSpec 描述“创建走 /v1/video/generations、查询走 /v1/tasks/{task_id}”的上游。
func videoTestProfileSpec() VideoProtocolProfileSpec {
	duration := 5.0
	return VideoProtocolProfileSpec{
		Auth:         VideoProfileAuth{Scheme: "header", Header: "X-Api-Key"},
		Capabilities: &VideoProfileCapabilities{Durations: []float64{5, 10}, MaxImages: 1, ImageRoles: []string{VideoMediaRoleFirstFrame}},
		Create: VideoProfileCreate{
			Path:     "/v1/video/generations",
			Defaults: VideoProfileDefaults{Duration: &duration},
			Body: []VideoProfileRule{
				{From: "model", To: "model"},
				{From: "prompt", To: "input.prompt", Required: true},
				{From: "duration", To: "parameters.duration", Type: "int"},
				{From: "resolution", To: "parameters.resolution", Map: map[string]any{"720p": "720P"}},
				{From: "images[role=first_frame]", To: "input.img_url"},
				{From: "extra.style", To: "parameters.style"},
				{Const: json.RawMessage(`true`), To: "parameters.prompt_extend"},
			},
			Response: VideoProfileCreateResponse{TaskID: []string{"data.task_id"}, Refs: map[string]string{"region": "data.region"},
				RejectedWhen: []VideoProfileCondition{{Path: "code", NotIn: []any{0.0}}}},
		},
		Poll: VideoProfilePoll{
			Path:  "/v1/tasks/{task_id}",
			Query: map[string]string{"region": "{ref.region}"},
			Response: VideoProfilePollResponse{Root: VideoProfilePaths{"data"}, TaskID: []string{"task_id"}, Status: VideoProfilePaths{"task_status"},
				StatusMap: map[string]string{"SUCCESS": "completed", "FAILURE": "failed", "RUNNING": "processing"},
				VideoURL:  []string{"output.url"}, Seconds: []string{"usage.duration"}, Tokens: []string{"usage.completion_tokens"}},
		},
		Content: VideoProfileContent{Mode: "url"},
	}
}

// 模板保存校验拒绝不安全路径、敏感请求头、非法占位符和缺少终态的状态映射。
func TestVideoProfileSpecValidation(t *testing.T) {
	require.NoError(t, videoTestProfileSpec().Validate())
	for name, mutate := range map[string]func(*VideoProtocolProfileSpec){
		"absolute_host":     func(s *VideoProtocolProfileSpec) { s.Create.Path = "https://evil.example/x" },
		"dot_dot":           func(s *VideoProtocolProfileSpec) { s.Poll.Path = "/v1/../tasks/{task_id}" },
		"query_in_path":     func(s *VideoProtocolProfileSpec) { s.Poll.Path = "/v1/tasks/{task_id}?a=1" },
		"unknown_ref":       func(s *VideoProtocolProfileSpec) { s.Poll.Path = "/v1/tasks/{ref.missing}" },
		"task_in_create":    func(s *VideoProtocolProfileSpec) { s.Create.Path = "/v1/{task_id}" },
		"no_completed":      func(s *VideoProtocolProfileSpec) { s.Poll.Response.StatusMap = map[string]string{"FAILURE": "failed"} },
		"terminal_unknown":  func(s *VideoProtocolProfileSpec) { s.Poll.Response.StatusMap["X"] = "done" },
		"auth_header":       func(s *VideoProtocolProfileSpec) { s.Headers = map[string]string{"Authorization": "Bearer x"} },
		"bad_scheme":        func(s *VideoProtocolProfileSpec) { s.Auth = VideoProfileAuth{Scheme: "basic"} },
		"no_task_id":        func(s *VideoProtocolProfileSpec) { s.Create.Response.TaskID = nil },
		"bad_source":        func(s *VideoProtocolProfileSpec) { s.Create.Body[0].From = "secret" },
		"gjson_query":       func(s *VideoProtocolProfileSpec) { s.Poll.Response.Status = VideoProfilePaths{"data.#(status)"} },
		"url_without_paths": func(s *VideoProtocolProfileSpec) { s.Poll.Response.VideoURL = nil },
		"path_content":      func(s *VideoProtocolProfileSpec) { s.Content = VideoProfileContent{Mode: "path", Path: "/content"} },
	} {
		spec := videoTestProfileSpec()
		mutate(&spec)
		require.Error(t, spec.Validate(), name)
	}
}

// 引擎按模板生成请求体，计价维度只取被发送的字段；未映射的非空字段拒绝而不是丢弃。
func TestVideoProfileEngineBuildsBodyAndBilling(t *testing.T) {
	spec := videoTestProfileSpec()
	req, err := ParseVideoCanonicalRequest([]byte(`{"model":"vendor-model","prompt":"sea","resolution":"720p","images":[{"url":"https://a/f.png","role":"first_frame"}],"extra":{"style":"anime"}}`))
	require.NoError(t, err)
	body, metadata, err := buildVideoProfileCreateBody(spec, req, false)
	require.NoError(t, err)
	require.Equal(t, "sea", gjson.GetBytes(body, "input.prompt").String())
	require.Equal(t, int64(5), gjson.GetBytes(body, "parameters.duration").Int(), "默认时长写入请求并参与计价")
	require.Equal(t, "720P", gjson.GetBytes(body, "parameters.resolution").String())
	require.Equal(t, "https://a/f.png", gjson.GetBytes(body, "input.img_url").String())
	require.Equal(t, "anime", gjson.GetBytes(body, "parameters.style").String())
	require.True(t, gjson.GetBytes(body, "parameters.prompt_extend").Bool())
	require.Equal(t, 5.0, metadata.DurationSeconds)
	require.Equal(t, "720p", metadata.Resolution)
	require.Equal(t, 1, *metadata.ReferenceImageCount)
	for name, raw := range map[string]string{
		"unmapped_negative": `{"model":"m","prompt":"p","negative_prompt":"blur"}`,
		"unmapped_extra":    `{"model":"m","prompt":"p","extra":{"other":1}}`,
		"unmapped_image":    `{"model":"m","prompt":"p","images":[{"url":"https://a/r.png","role":"reference_image"}]}`,
		"map_miss":          `{"model":"m","prompt":"p","resolution":"1080p"}`,
		"capability":        `{"model":"m","prompt":"p","duration":7}`,
		"required":          `{"model":"m"}`,
	} {
		req, err := ParseVideoCanonicalRequest([]byte(raw))
		require.NoError(t, err)
		_, _, err = buildVideoProfileCreateBody(spec, req, false)
		require.Error(t, err, name)
	}
}

func videoTestProfileTarget(t *testing.T, spec VideoProtocolProfileSpec) VideoUpstreamTarget {
	t.Helper()
	snapshot, err := json.Marshal(spec)
	require.NoError(t, err)
	hash, err := spec.Hash()
	require.NoError(t, err)
	return VideoUpstreamTarget{Version: videoTargetVersionProfile, Model: "vendor-model", AccountID: 11, BaseURL: "https://vendor.example",
		CreatePath: spec.Create.Path, ProtocolAdapter: VideoProfileProtocolID("vendor_x", 1), ProfileSnapshot: snapshot, ProfileHash: hash}
}

// 创建与查询响应按模板解析身份、状态、产物与用量；冲突 ID 与他人任务整包拒绝。
func TestVideoProfileEngineParsesResponses(t *testing.T) {
	target := videoTestProfileTarget(t, videoTestProfileSpec())
	require.NoError(t, ValidateVideoProtocolTarget(target))
	created, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"code":0,"data":{"task_id":"t-1","region":"cn"}}`), "")
	require.NoError(t, err)
	require.Equal(t, "t-1", created.TaskID)
	require.Equal(t, "cn", created.TaskRefs["region"])
	rejected, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"code":4001,"message":"bad"}`), "")
	require.NoError(t, err)
	require.Equal(t, "failed", rejected.Status, "没有任务 ID 且满足未受理条件才释放")
	require.True(t, VideoProtocolResponseHasConflictingTaskIDs(target, []byte(`{"data":{"task_id":7}}`)) == false)
	target.TaskRefs = created.TaskRefs
	path, err := VideoProtocolTaskPath(target, "t-1")
	require.NoError(t, err)
	require.Equal(t, "/v1/tasks/t-1?region=cn", path)
	polled, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"data":{"task_id":"t-1","task_status":"success","output":{"url":"https://cdn.example/v.mp4"},"usage":{"duration":5,"completion_tokens":1200}}}`), "t-1")
	require.NoError(t, err)
	require.Equal(t, "completed", polled.Status, "状态映射不区分大小写")
	require.Equal(t, "https://cdn.example/v.mp4", polled.VideoURL)
	require.Equal(t, 5.0, polled.Metadata.DurationSeconds)
	require.Equal(t, int64(1200), *polled.Metadata.Tokens)
	unknown, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"data":{"task_id":"t-1","task_status":"GENERATING"}}`), "t-1")
	require.NoError(t, err)
	require.Empty(t, unknown.Status, "未识别状态不能推进终态")
	require.False(t, VideoProtocolResponseMatchesTask(target, []byte(`{"data":{"task_id":"other","task_status":"SUCCESS"}}`), "t-1"))
	// 被篡改的快照不能再用于查询。
	tampered := target
	tampered.ProfileHash = strings.Repeat("0", 64)
	_, err = VideoProtocolTaskPath(tampered, "t-1")
	require.Error(t, err)
}

// videoProfileMemoryRepo 是内存模板仓储，模拟版本递增与唯一启用版本。
type videoProfileMemoryRepo struct {
	mu       sync.Mutex
	profiles []VideoProtocolProfile
}

func (r *videoProfileMemoryRepo) CreateVersion(_ context.Context, p *VideoProtocolProfile) (*VideoProtocolProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	version := 0
	for _, existing := range r.profiles {
		if existing.ProfileKey == p.ProfileKey && existing.Version > version {
			version = existing.Version
		}
	}
	saved := *p
	saved.Version, saved.ID = version+1, int64(len(r.profiles)+1)
	r.profiles = append(r.profiles, saved)
	return &saved, nil
}

func (r *videoProfileMemoryRepo) Get(_ context.Context, key string, version int) (*VideoProtocolProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.profiles {
		if p.ProfileKey == key && (version == 0 && p.Status == VideoProtocolProfileActive || version > 0 && p.Version == version) {
			copied := p
			return &copied, nil
		}
	}
	return nil, ErrVideoProtocolProfileNotFound
}

func (r *videoProfileMemoryRepo) List(context.Context) ([]VideoProtocolProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]VideoProtocolProfile(nil), r.profiles...), nil
}

func (r *videoProfileMemoryRepo) SetStatus(_ context.Context, key string, version int, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	found := false
	for i := range r.profiles {
		p := &r.profiles[i]
		if p.ProfileKey != key {
			continue
		}
		if p.Version == version {
			p.Status, found = status, true
		} else if status == VideoProtocolProfileActive && p.Status == VideoProtocolProfileActive {
			// 被替换的启用版本降为草稿，锁定版本的绑定仍可使用。
			p.Status = VideoProtocolProfileDraft
		}
	}
	if !found {
		return ErrVideoProtocolProfileNotFound
	}
	return nil
}

func (r *videoProfileMemoryRepo) Delete(_ context.Context, key string, version int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.profiles[:0]
	removed := 0
	for _, p := range r.profiles {
		if p.ProfileKey == key && (version == 0 || p.Version == version) {
			removed++
			continue
		}
		kept = append(kept, p)
	}
	r.profiles = kept
	if removed == 0 {
		return ErrVideoProtocolProfileNotFound
	}
	return nil
}

// 模板版本：保存即新版本，启用切换唯一启用版本，停用版本不能用于新任务，锁定版本仍可解析。
func TestVideoProfileServiceVersionsAndResolve(t *testing.T) {
	repo := &videoProfileMemoryRepo{}
	svc := NewVideoProtocolProfileService(repo)
	ctx := context.Background()
	_, err := svc.Resolve(ctx, "vendor_x", 0)
	require.Error(t, err)
	first, err := svc.Save(ctx, VideoProtocolProfileInput{ProfileKey: "vendor_x", Name: "Vendor X", Spec: videoTestProfileSpec(), Activate: true}, 1)
	require.NoError(t, err)
	require.Equal(t, 1, first.Version)
	second, err := svc.Save(ctx, VideoProtocolProfileInput{ProfileKey: "vendor_x", Name: "Vendor X", Spec: videoTestProfileSpec()}, 1)
	require.NoError(t, err)
	require.Equal(t, 2, second.Version)
	active, err := svc.Resolve(ctx, "vendor_x", 0)
	require.NoError(t, err)
	require.Equal(t, 1, active.Version, "未启用的新版本不影响当前启用版本")
	require.NoError(t, svc.SetStatus(ctx, "vendor_x", 2, VideoProtocolProfileActive))
	active, err = svc.Resolve(ctx, "vendor_x", 0)
	require.NoError(t, err)
	require.Equal(t, 2, active.Version)
	pinned, err := svc.Resolve(ctx, "vendor_x", 1)
	require.NoError(t, err)
	require.Equal(t, 1, pinned.Version)
	require.NoError(t, svc.SetStatus(ctx, "vendor_x", 1, VideoProtocolProfileDisabled))
	_, err = svc.Resolve(ctx, "vendor_x", 1)
	require.Error(t, err, "停用版本不能用于新任务")
	_, err = svc.Save(ctx, VideoProtocolProfileInput{ProfileKey: "Bad Key", Name: "x", Spec: videoTestProfileSpec()}, 1)
	require.Error(t, err)
	preview, err := svc.Preview(VideoProfilePreviewInput{Spec: videoTestProfileSpec(), Model: "vendor-model",
		Request:        json.RawMessage(`{"model":"public","prompt":"p"}`),
		CreateResponse: json.RawMessage(`{"code":0,"data":{"task_id":"t-9","region":"us"}}`),
		PollResponse:   json.RawMessage(`{"data":{"task_id":"t-9","task_status":"RUNNING"}}`)})
	require.NoError(t, err)
	require.Equal(t, "vendor-model", gjson.GetBytes(preview.CreateBody, "model").String())
	require.Equal(t, "t-9", preview.Create.TaskID)
	require.Equal(t, "/v1/tasks/t-9?region=us", preview.PollPath)
	require.Equal(t, "processing", preview.Poll.Status)
	require.True(t, preview.Poll.MatchesTask)
}

// 账号绑定模板协议后，统一请求按冻结快照创建、查询，鉴权按模板方式携带账号密钥。
func TestVideoProfileUpstreamEndToEnd(t *testing.T) {
	repo := &videoProfileMemoryRepo{}
	profiles := NewVideoProtocolProfileService(repo)
	_, err := profiles.Save(context.Background(), VideoProtocolProfileInput{ProfileKey: "vendor_x", Name: "Vendor X", Spec: videoTestProfileSpec(), Activate: true}, 1)
	require.NoError(t, err)
	account := videoFixtureAccount(VideoEndpointCompat)
	account.Credentials = map[string]any{"api_key": "fixture-secret", "base_url": "https://vendor.example/api",
		"video_model_protocols": map[string]any{"vendor-model": map[string]any{"protocol": "profile:vendor_x"}}}
	require.NoError(t, normalizeVideoCredentials(account))
	fixture := &videoHTTPFixture{status: 200, response: `{"code":0,"data":{"task_id":"t-1","region":"cn"}}`}
	svc := ProvideVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil), httpUpstream: fixture}, profiles)
	groupID := int64(5)
	key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformVideo, Status: StatusActive}}
	body := []byte(`{"model":"vendor-model","prompt":"sea","duration":10}`)
	canonical, err := ParseVideoCanonicalRequest(body)
	require.NoError(t, err)
	selected, err := svc.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{Body: body, Canonical: canonical, InboundProtocol: "openai_videos"})
	require.NoError(t, err)
	defer selected.Release()
	require.Equal(t, videoTargetVersionProfile, selected.Target.Version)
	require.Equal(t, "profile:vendor_x@1", selected.Target.ProtocolAdapter)
	require.Equal(t, 10.0, selected.Metadata.DurationSeconds)
	// 模板之后被停用或改版，不影响已冻结的快照。
	require.NoError(t, profiles.SetStatus(context.Background(), "vendor_x", 1, VideoProtocolProfileDisabled))
	created, err := svc.Submit(context.Background(), selected)
	require.NoError(t, err)
	require.Equal(t, "https://vendor.example/api/v1/video/generations", fixture.request.URL.String())
	require.Equal(t, "fixture-secret", fixture.request.Header.Get("X-Api-Key"))
	require.Empty(t, fixture.request.Header.Get("Authorization"))
	require.Equal(t, int64(10), gjson.GetBytes(fixture.requestBody, "parameters.duration").Int())
	require.Equal(t, "t-1", created.TaskID)
	target := selected.Target
	target.TaskRefs = created.TaskRefs
	fixture.response = `{"data":{"task_id":"t-1","task_status":"SUCCESS","output":{"url":"https://cdn.example/v.mp4"}}}`
	polled, err := svc.Poll(context.Background(), account, target, "t-1")
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, fixture.request.Method)
	require.Equal(t, "/api/v1/tasks/t-1", fixture.request.URL.Path)
	require.Equal(t, "cn", fixture.request.URL.Query().Get("region"))
	require.Equal(t, "completed", polled.Status)
	require.Equal(t, "https://cdn.example/v.mp4", polled.VideoURL)
	// 停用后新的选号不能再使用该模板。
	_, err = svc.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{Body: body, Canonical: canonical, InboundProtocol: "openai_videos"})
	require.Error(t, err)
}

// R4：模板整数的指数和带零小数写法按十进制精确求值，超出安全范围拒绝，不能舍入后发往上游。
func TestVideoProfileExponentIntegerNotRounded(t *testing.T) {
	for _, raw := range []string{`9007199254740993e0`, `9007199254740993.0`, `1.5`} {
		for _, kind := range []string{"int", "string_int"} {
			_, err := convertVideoProfileValue(json.RawMessage(raw), kind)
			require.Error(t, err, "%s as %s", raw, kind)
		}
	}
	got, err := convertVideoProfileValue(json.RawMessage(`5e0`), "int")
	require.NoError(t, err)
	require.Equal(t, int64(5), got)
	got, err = convertVideoProfileValue("12.0", "string_int")
	require.NoError(t, err)
	require.Equal(t, "12", got)
}
