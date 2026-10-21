//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 发布替身只记录内部入口，实际 Redis 票据的并发与失效由独立安全回归验证。
type internalVideoReferenceStub struct {
	calls  int
	userID int64
	inputs []string
	err    error
}

func (s *internalVideoReferenceStub) PrepareDataURLs(_ context.Context, userID int64, inputs []string) ([]string, error) {
	s.calls++
	s.userID, s.inputs = userID, inputs
	if s.err != nil {
		return nil, s.err
	}
	urls := make([]string, len(inputs))
	for i := range urls {
		urls[i] = "https://canvas.example.com/api/v1/video-references/opaque"
	}
	return urls, nil
}

func TestCreativeVideoYezonghuiReferencesAndAdaptiveScope(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterYezonghui, VideoProtocolAdapterLegacy, VideoProtocolAdapterMetasoH3} {
		t.Run(adapter, func(t *testing.T) {
			publisher := &internalVideoReferenceStub{}
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": adapter}
			selector := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil)})
			key := &APIKey{GroupID: &account.GroupIDs[0], Group: &Group{ID: account.GroupIDs[0], Platform: PlatformVideo, Status: StatusActive}}
			raw := []byte(`{"model":"video-model","prompt":"fixture","duration":5,"resolution":"768p","ratio":"adaptive","images":["data:image/png;base64,trusted"]}`)
			req := VideoTaskSubmitRequest{Body: raw, InboundProtocol: "openai_videos", canvasAdaptiveRatio: "9:16", canvasPrepareReferences: newCreativeVideoReferencePreparer(publisher, 7)}
			hash := videoPayloadHash(req)
			for i := 0; i < 2; i++ {
				selected, err := selector.SelectAccount(context.Background(), key, req)
				require.NoError(t, err)
				selected.Release()
				require.Equal(t, hash, videoPayloadHash(req))
				require.Equal(t, string(raw), string(req.Body))
				if adapter == VideoProtocolAdapterYezonghui {
					require.Equal(t, 1, publisher.calls)
					require.Equal(t, int64(7), publisher.userID)
					require.Equal(t, "data:image/png;base64,trusted", publisher.inputs[0])
					require.False(t, gjson.GetBytes(selected.Body, "size").Exists())
					require.NotContains(t, string(selected.Body), "base64")
					require.Contains(t, string(selected.Body), "image_urls")
					require.Equal(t, "768p", selected.Metadata.Resolution)
					require.Equal(t, 1, *selected.Metadata.ReferenceImageCount)
				} else {
					require.Zero(t, publisher.calls)
					require.Contains(t, string(selected.Body), "base64,trusted")
				}
			}
		})
	}
}

func TestCreativeVideoYezonghuiReferenceFailureBeforePrechargeAndRecover(t *testing.T) {
	for _, failure := range []error{nil, ErrVideoReferencePublicURL, ErrVideoReferenceUnavailable, ErrVideoReferenceQuota} {
		s, e, upstream, billing, _ := creativeVideoTestFixture(t)
		e.Params.Endpoint, e.Params.AspectRatio = "openai_videos", "adaptive"
		account := videoFixtureAccount(VideoEndpointOpenAIVideos)
		account.GroupIDs = []int64{e.Params.GroupID}
		account.Credentials["model_mapping"] = map[string]any{e.Params.Model: e.Params.Model}
		account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": VideoProtocolAdapterYezonghui}
		s.public.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{e.Params.GroupID: {*account}}}
		s.public.Settings.(*creativeFakeSettingReader).models[0].VideoEndpoint = VideoEndpointOpenAIVideos
		transport := &creativeAdaptiveVideoTransport{videoLifecycleUpstream: upstream, selector: NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil)})}
		tasks := s.tasks.(*VideoTaskService)
		tasks.upstream, tasks.accounts = transport, videoLifecycleAccounts{account: account}
		publisher := &internalVideoReferenceStub{err: failure}
		s.references = publisher
		e.Params.References = []CreativeCanvasReference{{AssetID: "portrait"}}
		assets := []CreativeCanvasInputAsset{{Asset: &CreativeCanvasAsset{ID: "portrait", UserID: e.UserID, MediaType: "image", MIMEType: "image/png"}, Content: makeTestPNG(t, 16, 24)}}
		id, err := s.Submit(context.Background(), e, assets)
		if failure != nil {
			require.ErrorIs(t, err, failure)
			require.Empty(t, id)
			require.Zero(t, upstream.submitted)
			require.Zero(t, billing.reserves)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, 1, billing.reserves)
		require.Equal(t, 1, upstream.submitted)
		require.Equal(t, 1, publisher.calls)
		// 原图、公开地址或目录随后不可用，恢复原任务也不再发布或创建。
		s.references = nil
		s.public.Settings.(*creativeFakeSettingReader).models = nil
		recovered, err := s.Submit(context.Background(), e, nil)
		require.NoError(t, err)
		require.Equal(t, id, recovered)
		require.Equal(t, 1, upstream.submitted)
		require.Equal(t, 1, publisher.calls)
	}
}

func TestVideoYezonghuiAdminTestSignedImageAndIdentity(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": VideoProtocolAdapterYezonghui}
	account.Credentials["model_mapping"] = map[string]any{"video-model": "video-model"}
	for _, failure := range []error{nil, ErrVideoReferencePublicURL} {
		svc, upstream := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
			if r.Method == http.MethodPost {
				require.Equal(t, "/prefix/v1/videos", r.URL.Path)
				require.Equal(t, "5", gjson.GetBytes(body, "seconds").String())
				require.Equal(t, "1080p", gjson.GetBytes(body, "resolution").String())
				require.False(t, gjson.GetBytes(body, "size").Exists())
				require.Contains(t, gjson.GetBytes(body, "image_urls.0").String(), "/video-references/")
				require.NotContains(t, string(body), "base64")
				return videoAccountTestJSON(200, `{"id":"task-one","status":"queued"}`), nil
			}
			return videoAccountTestJSON(200, `{"id":"task-one","status":"completed","url":"https://cdn.example/result.mp4"}`), nil
		})
		publisher := &internalVideoReferenceStub{err: failure}
		svc.videoReferences = publisher
		c, recorder := grokModesContext(context.Background())
		err := svc.TestAccountConnectionWithOptions(c, account.ID, "video-model", "test", "video", "", "", AccountTestOptions{AuthenticatedUserID: 9, VideoEndpoint: "openai_videos", Resolution: "1080p", ImageDataURL: grokModesImageDataURL(t, 8)})
		require.Equal(t, int64(9), publisher.userID)
		if failure != nil {
			require.Error(t, err)
			require.Empty(t, upstream.requests)
			continue
		}
		require.NoError(t, err)
		require.Len(t, upstream.requests, 2)
		require.Contains(t, recorder.Body.String(), `"success":true`)
		require.NotContains(t, recorder.Body.String(), "video-references")
		require.NotContains(t, recorder.Body.String(), "fixture-secret")
	}
	// 身份只由鉴权处理器注入，客户端同名字段不能占用其他用户配额。
	var options AccountTestOptions
	require.NoError(t, json.Unmarshal([]byte(`{"AuthenticatedUserID":999,"authenticated_user_id":999}`), &options))
	require.Zero(t, options.AuthenticatedUserID)
}

func TestVideoYezonghuiInternalValidationBeforePublishing(t *testing.T) {
	pub := &internalVideoReferenceStub{}
	prepare := newCreativeVideoReferencePreparer(pub, 3)
	target := yezonghuiVideoFixture()
	_, err := prepare(context.Background(), target, []byte(`{"model":"m","prompt":"p","duration":0,"resolution":"720p","images":["data:image/png;base64,a"]}`))
	require.Error(t, err)
	require.Zero(t, pub.calls)
	// 没有图片时不要求站点公网地址或参考图存储服务。
	_, err = newCreativeVideoReferencePreparer(nil, 3)(context.Background(), target, []byte(`{"model":"m","prompt":"p","duration":5,"resolution":"720p"}`))
	require.NoError(t, err)
	for _, code := range []string{"VIDEO_REFERENCE_PUBLIC_URL_REQUIRED", "VIDEO_REFERENCE_INVALID", "VIDEO_REFERENCE_UNAVAILABLE", "VIDEO_REFERENCE_QUOTA"} {
		require.NotEmpty(t, creativeCanvasExecutionFailureMessage("video", code))
	}
}

// 查询失败只重查原任务；供应商明确的失败终态必须立即结束测试，而非一直等到超时。
func TestVideoYezonghuiAdminPollOutcome(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		status           int
	}{
		{"failed", `{"id":"task-one","status":"failed","error":{"message":"rejected","code":"failed"}}`, "ended with status failed", 200},
		{"temporary", `{"code":"busy","message":"retry later"}`, "查询暂不可用", 502},
		{"unknown", `{"id":"task-one","status":"unknown"}`, "等待超时", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.Credentials["model_mapping"] = map[string]any{"video-model": "video-model"}
			account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": VideoProtocolAdapterYezonghui}
			svc, upstream := videoAccountTestService(account, func(r *http.Request, _ []byte) (*http.Response, error) {
				if r.Method == http.MethodPost {
					return videoAccountTestJSON(200, `{"id":"task-one","status":"queued"}`), nil
				}
				return videoAccountTestJSON(test.status, test.body), nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			c, recorder := grokModesContext(ctx)
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "video-model", "test", "video", "", "", AccountTestOptions{VideoEndpoint: "openai_videos", Resolution: "720p"})
			require.Error(t, err)
			require.Len(t, upstream.requests, 2)
			require.Contains(t, recorder.Body.String(), test.want)
			require.NotContains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestVideoYezonghuiPollDelayPreservesOtherModels(t *testing.T) {
	target := yezonghuiVideoFixture()
	require.Equal(t, time.Minute, videoTaskPollDelay(target, 3*time.Second))
	require.Equal(t, 5*time.Minute, videoTaskPollDelay(target, 5*time.Minute))
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterOpenAI, VideoProtocolAdapterMetasoH3} {
		target.ProtocolAdapter = adapter
		require.Equal(t, 3*time.Second, videoTaskPollDelay(target, 3*time.Second))
	}
}

// 只写 SSE 注释保持连接，不伪装任务成功事件；取消立即结束等待。
func TestVideoYezonghuiAdminWaitKeepsStreamAlive(t *testing.T) {
	c, recorder := grokModesContext(context.Background())
	require.NoError(t, waitVideoAccountTestPoll(c, context.Background(), 40*time.Millisecond, 5*time.Millisecond))
	require.Contains(t, recorder.Body.String(), ": keep-alive\n\n")
	require.NotContains(t, recorder.Body.String(), "data:")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, waitVideoAccountTestPoll(c, ctx, time.Minute, time.Second), context.Canceled)
}
