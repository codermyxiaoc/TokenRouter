package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 视频协议测试使用内存HTTP替身，绝不创建真实上游任务。
type videoHTTPFixture struct {
	request     *http.Request
	requestBody []byte
	response    string
	status      int
	calls       int
}

func (f *videoHTTPFixture) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	f.request = req
	f.calls++
	f.requestBody, _ = io.ReadAll(req.Body)
	return &http.Response{StatusCode: f.status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(f.response))}, nil
}
func (f *videoHTTPFixture) DoWithTLS(req *http.Request, proxy string, id int64, limit int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(req, proxy, id, limit)
}

func videoFixtureAccount(endpoint VideoEndpoint) *Account {
	return &Account{ID: 11, Platform: PlatformVideo, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{5}, Credentials: map[string]any{"api_key": "fixture-secret", "base_url": "https://relay.example/prefix", "video_endpoints": []string{string(endpoint)}}}
}

func TestVideoNativeProtocolFixturesPreservePayloadAndResponse(t *testing.T) {
	fixtures := []struct {
		endpoint                     VideoEndpoint
		path, body, response, taskID string
	}{
		{VideoEndpointCompat, "/v1/video/generations", `{"model":"m","prompt":"x","images":["https://asset/img"],"future":{"enabled":false}}`, `{"id":"compat-task","status":"pending","future":1}`, "compat-task"},
		{VideoEndpointSeedance, "/api/v3/contents/generations/tasks", `{"model":"m","content":[{"type":"text","text":"x"},{"type":"video_url","video_url":{"url":"https://asset/video"}}],"duration":-1,"watermark":false}`, `{"id":"ark-task","status":"queued","model":"m","future":true}`, "ark-task"},
		{VideoEndpointKling, "/omni-video/m", `{"model":"m","prompt":"x","camera_control":{"zoom":0},"future":null}`, `{"data":{"task_id":"kling-task","task_status":"submitted"},"code":0}`, "kling-task"},
		{VideoEndpointWan, "/api/v1/services/aigc/video-generation/video-synthesis", `{"model":"m","input":{"prompt":"x","video_url":"https://asset/video"},"parameters":{"duration":5,"resolution":"1080P"},"future":false}`, `{"output":{"task_id":"wan-task","task_status":"PENDING"},"request_id":"trace-only"}`, "wan-task"},
		{VideoEndpointMiniMax, "/v2/video_generation", `{"model":"m","content":[{"type":"text","text":"x"}],"resolution":"2K","duration":5,"aigc_watermark":false}`, `{"task_id":"minimax-task","base_resp":{"status_code":0},"future":"keep"}`, "minimax-task"},
	}
	for _, f := range fixtures {
		t.Run(string(f.endpoint), func(t *testing.T) {
			httpFixture := &videoHTTPFixture{status: http.StatusAccepted, response: f.response}
			svc := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: httpFixture})
			account := videoFixtureAccount(f.endpoint)
			target := VideoUpstreamTarget{Version: 1, Endpoint: f.endpoint, BaseURL: account.GetCredential("base_url"), CreatePath: f.path, Model: "m", AccountID: account.ID}
			got, err := svc.Submit(context.Background(), &VideoUpstreamSelection{Account: account, Target: target, Body: []byte(f.body)})
			require.NoError(t, err)
			require.Equal(t, f.taskID, got.TaskID)
			require.Equal(t, f.body, string(httpFixture.requestBody))
			require.Equal(t, f.response, string(got.Body))
			require.Equal(t, http.StatusAccepted, got.StatusCode)
			require.Equal(t, "/prefix"+f.path, httpFixture.request.URL.Path)
			require.Equal(t, "Bearer fixture-secret", httpFixture.request.Header.Get("Authorization"))
			require.Equal(t, 1, httpFixture.calls)
			if f.endpoint == VideoEndpointWan {
				require.Equal(t, "enable", httpFixture.request.Header.Get("X-DashScope-Async"))
			}
		})
	}
}

func TestVideoMetadataPreservesMissingUsageAndOnlyCountsVideo(t *testing.T) {
	for _, body := range []string{`{"model":"m","images":["img"],"audios":["audio"]}`, `{"model":"m","content":[{"type":"audio_url","audio_url":{"url":"audio"}},{"type":"image_url","image_url":{"url":"img"}}]}`} {
		got, err := ParseVideoRequestMetadata([]byte(body), "")
		require.NoError(t, err)
		require.False(t, got.HasReferenceVideo)
	}
	for _, body := range []string{`{"model":"m","videos":[{"url":"video"}]}`, `{"model":"m","input":{"video_url":"video"}}`, `{"model":"m","content":[{"type":"video_url","video_url":{"url":"video"}}]}`} {
		got, err := ParseVideoRequestMetadata([]byte(body), "")
		require.NoError(t, err)
		require.True(t, got.HasReferenceVideo)
	}
	require.Nil(t, ExtractVideoUpstreamResponse(VideoEndpointSeedance, []byte(`{"status":"succeeded"}`), "id").Metadata.Tokens)
	got := ExtractVideoUpstreamResponse(VideoEndpointSeedance, []byte(`{"status":"succeeded","usage":{"completion_tokens":0}}`), "id")
	require.NotNil(t, got.Metadata.Tokens)
	require.Zero(t, *got.Metadata.Tokens)
	_, err := ParseVideoRequestMetadata([]byte(`{"model":"other"}`), "path-model")
	require.Error(t, err)
}

type videoSelectionFixture struct {
	AccountRepository
	accounts []Account
}

func (r *videoSelectionFixture) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return r.accounts, nil
}

func TestVideoSelectionUsesMappedModelAndRejectsDisabledEndpoint(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointSeedance)
	account.Credentials["video_endpoints"] = []string{"seedance", "minimax"}
	account.Credentials["model_mapping"] = map[string]any{"alias": "real"}
	account.Credentials["video_model_bindings"] = map[string]string{"real": "minimax"}
	repo := &videoSelectionFixture{accounts: []Account{*account}}
	svc := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: repo, concurrencyService: NewConcurrencyService(nil)})
	key := &APIKey{GroupID: &account.GroupIDs[0], Group: &Group{ID: 5, Platform: PlatformVideo, Status: StatusActive}}
	req := VideoTaskSubmitRequest{Body: []byte(`{"model":"alias","content":[{"type":"text","text":"alias"}],"extension":false}`), InboundProtocol: "minimax", Native: true}
	ctx := context.WithValue(context.Background(), ctxkey.ClientModel, "group/client-alias")
	got, err := svc.SelectAccount(ctx, key, req)
	require.NoError(t, err)
	defer got.Release()
	require.Equal(t, VideoEndpointMiniMax, got.Target.Endpoint)
	require.Equal(t, "group/client-alias", got.RequestedModel)
	require.Equal(t, "alias", got.InternalModel)
	require.Equal(t, "real", gjson.GetBytes(got.Body, "model").String())
	require.Equal(t, "alias", gjson.GetBytes(got.Body, "content.0.text").String())
	// 模型改为只允许 Seedance 后，MiniMax 入站不能越过模型绑定。
	repo.accounts[0].Credentials["video_model_bindings"] = map[string]string{"real": "seedance"}
	_, err = svc.SelectAccount(context.Background(), key, req)
	require.Error(t, err)
	_, err = svc.SelectAccount(context.Background(), &APIKey{GroupID: key.GroupID, Group: &Group{ID: 5, Platform: PlatformOpenAI, Status: StatusActive}}, req)
	require.Error(t, err)
}

func TestVideoUpstreamErrorObservationRetainsAccountAndNoReplay(t *testing.T) {
	fixture := &videoHTTPFixture{status: 502, response: `{"error":{"message":"provider unavailable"}}`}
	account := videoFixtureAccount(VideoEndpointMiniMax)
	svc := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
	var observations []VideoUpstreamObservation
	ctx := WithVideoUpstreamObserver(context.Background(), func(o VideoUpstreamObservation) { observations = append(observations, o) })
	_, err := svc.Submit(ctx, &VideoUpstreamSelection{Account: account, Target: VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointMiniMax, BaseURL: "https://relay.example", CreatePath: "/v2/video_generation", Model: "actual", AccountID: account.ID}, Body: []byte(`{"model":"actual"}`)})
	require.NoError(t, err)
	require.Equal(t, 1, fixture.calls)
	require.Len(t, observations, 2)
	last := observations[1]
	require.Equal(t, account.ID, last.AccountID)
	require.Equal(t, "actual", last.Model)
	require.Equal(t, "/v2/video_generation", last.Endpoint)
	require.Equal(t, 502, last.StatusCode)
	require.Contains(t, last.ErrorMessage, "provider unavailable")
}

// 数据库拒绝未付费创建的账号容量后，重选必须跳过该账号，不能再次卡在最高优先级。
func TestVideoSelectionExcludesPendingFullAccountsBeforeDispatch(t *testing.T) {
	first, second := videoFixtureAccount(VideoEndpointSeedance), videoFixtureAccount(VideoEndpointSeedance)
	first.ID, first.Priority = 11, 1
	second.ID, second.Priority = 12, 2
	repo := &videoSelectionFixture{accounts: []Account{*second, *first}}
	svc := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: repo, concurrencyService: NewConcurrencyService(nil)})
	groupID := int64(5)
	key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformVideo, Status: StatusActive}}
	req := VideoTaskSubmitRequest{Body: []byte(`{"model":"m","duration":5,"resolution":"720p"}`), InboundProtocol: "seedance", Native: true, IdempotencyKey: "stable"}
	hash := videoPayloadHash(req)
	selected, err := svc.SelectAccount(context.Background(), key, req)
	require.NoError(t, err)
	require.Equal(t, int64(11), selected.Account.ID)
	selected.Release()
	req.ExcludedAccountIDs = map[int64]struct{}{11: {}}
	selected, err = svc.SelectAccount(context.Background(), key, req)
	require.NoError(t, err)
	require.Equal(t, int64(12), selected.Account.ID)
	selected.Release()
	require.Equal(t, hash, videoPayloadHash(req))
	req.ExcludedAccountIDs[12] = struct{}{}
	_, err = svc.SelectAccount(context.Background(), key, req)
	require.Error(t, err)
}

func TestVideoNativeStatusAndURLContracts(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax} {
		_, err := videoTaskPath(VideoUpstreamTarget{Endpoint: endpoint}, "../other")
		require.Error(t, err)
	}
	require.Equal(t, "https://relay.example/proxy/api/v3/contents/generations/tasks", videoEndpointURL("https://relay.example/proxy/api/v3", "/api/v3/contents/generations/tasks"))
	// 原生响应整包保真，混合其它任务时必须整体拒绝，不能只挑出本任务状态后泄漏其余原包。
	got := ExtractVideoUpstreamResponse(VideoEndpointKling, []byte(`{"data":[{"task_id":"other","task_status":"failed"},{"task_id":"owned","task_status":"succeed","task_result":{"videos":[{"url":"https://cdn/video"}]}}]}`), "owned")
	require.Equal(t, "owned", got.TaskID)
	require.Empty(t, got.Status)
	require.Empty(t, got.VideoURL)
	// 仅包含目标任务的合法列表仍可正常解析完成状态与媒体链接。
	got = ExtractVideoUpstreamResponse(VideoEndpointKling, []byte(`{"data":[{"task_id":"owned","task_status":"succeed","task_result":{"videos":[{"url":"https://cdn/video"}]}}]}`), "owned")
	require.Equal(t, "completed", got.Status)
	require.Equal(t, "https://cdn/video", got.VideoURL)
	_, ok := MatchVideoGatewayRoute(http.MethodGet, "/api/v1/tasks/id/content")
	require.False(t, ok)
	route, ok := MatchVideoGatewayRoute(http.MethodPost, "/v1/videos/text2video")
	require.True(t, ok)
	require.Equal(t, "kling", route.Protocol)
	require.True(t, smartRoutingGroupEndpointEligible(PlatformVideo, "/v1/videos/text2video"))
	require.False(t, smartRoutingGroupEndpointEligible(PlatformGrok, "/v1/videos/text2video"))
	require.True(t, smartRoutingGroupEndpointEligible(PlatformGrok, "/v1/videos/generations"))
}

func TestVideoCancelRequiresConfirmation(t *testing.T) {
	for _, f := range []struct {
		body string
		code int
		want string
	}{
		{"", 204, "cancelled"}, {"", 200, "cancelled"}, {`{"status":"processing"}`, 200, "processing"}, {`{"status":"completed"}`, 200, "completed"}, {`{"error":{"message":"rejected"}}`, 200, ""}, {`{"status":"cancelled"}`, 200, "cancelled"},
	} {
		t.Run(f.body, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointSeedance)
			httpFixture := &videoHTTPFixture{status: f.code, response: f.body}
			svc := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: httpFixture})
			result, err := svc.Cancel(context.Background(), account, VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointSeedance, BaseURL: "https://relay.example", AccountID: account.ID}, "task")
			require.NoError(t, err)
			require.Equal(t, f.want, result.Status)
			require.Equal(t, f.body, string(result.Body))
		})
	}
}

func TestVideoCredentialsValidation(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointSeedance)
	require.NoError(t, normalizeVideoCredentials(account))
	for _, mutate := range []func(*Account){
		func(a *Account) { a.Type = AccountTypeOAuth },
		func(a *Account) { a.Credentials["video_endpoints"] = []string{"seedance", "seedance"} },
		func(a *Account) { a.Credentials["video_endpoints"] = []string{"unknown"} },
		func(a *Account) { a.Credentials["video_model_bindings"] = map[string]string{"m": "minimax"} },
		func(a *Account) { a.Credentials["video_max_pending_tasks"] = 1001 },
		func(a *Account) { a.Credentials["video_max_duration_seconds"] = 0 },
		func(a *Account) { a.Credentials["base_url"] = "https://user:pass@relay.example" },
	} {
		a := videoFixtureAccount(VideoEndpointSeedance)
		mutate(a)
		require.Error(t, normalizeVideoCredentials(a))
	}
	for _, body := range []string{`{"model":"m","model":"other"}`, `{"Model":"m"}`, `{"model":"m","Model":"other"}`} {
		_, err := ParseVideoRequestMetadata([]byte(body), "")
		require.Error(t, err)
	}
}
