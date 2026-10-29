//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Vidu 回归只使用内存载荷与 HTTP 替身，不发送真实生成请求。
func viduProtocolFixture(path string) VideoUpstreamTarget {
	return VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointVidu, ProtocolAdapter: "kemei_v1", Model: "viduq3-pro", CreatePath: path, BaseURL: "https://vidu.example/ent/v2", AccountID: 11}
}

func TestVideoKemeiViduOperationRoutes(t *testing.T) {
	for _, path := range []string{"/ent/v2/img2video", "/ent/v2/reference2video", "/ent/v2/text2video", "/ent/v2/start-end2video"} {
		route, ok := MatchVideoGatewayRoute(http.MethodPost, path)
		require.True(t, ok)
		require.True(t, route.Native)
		require.True(t, route.Create)
		require.Equal(t, "vidu", route.Protocol)
		require.Equal(t, path, route.PathTemplate)
	}
	route, ok := MatchVideoGatewayRoute(http.MethodGet, "/ent/v2/tasks/owned/creations")
	require.True(t, ok)
	require.Equal(t, "owned", route.TaskID)
	require.False(t, route.Create)
	for _, path := range []string{"/ent/v2/tasks", "/ent/v2/tasks/creations", "/ent/v2/tasks/../creations", "/ent/v2/tasks/owned/creations/extra", "/ent/v2/tasks/owned", "/ent/v2/img2video/unknown"} {
		_, ok := MatchVideoGatewayRoute(http.MethodGet, path)
		require.False(t, ok, path)
	}
	_, ok = MatchVideoGatewayRoute(http.MethodDelete, "/ent/v2/tasks/owned/creations")
	require.False(t, ok)
}

func TestVideoKemeiViduRequestsAndBillingMetadata(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		count      int
	}{
		{"/ent/v2/text2video", `{"model":"client-model","prompt":"p"}`, 0},
		{"/ent/v2/img2video", `{"model":"client-model","image_urls":["image"],"ratio":"9:16","generate_audio":false}`, 1},
		{"/ent/v2/reference2video", `{"model":"client-model","images":["same","same"],"audios":["sound"],"duration":7,"resolution":"720P"}`, 2},
		{"/ent/v2/start-end2video", `{"model":"client-model","images":["first","last"],"duration":8}`, 2},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body, metadata, err := PrepareVideoProtocolRequest([]byte(tc.body), "", viduProtocolFixture(tc.path))
			require.NoError(t, err)
			require.Equal(t, "viduq3-pro", gjson.GetBytes(body, "model").String())
			require.Equal(t, tc.count, *metadata.ReferenceImageCount)
			require.Equal(t, gjson.GetBytes(body, "duration").Float(), metadata.DurationSeconds)
			require.Equal(t, gjson.GetBytes(body, "resolution").String(), metadata.Resolution)
			require.False(t, metadata.HasReferenceVideo)
			for _, alias := range []string{"image_urls", "audios", "ratio", "generate_audio"} {
				require.False(t, gjson.GetBytes(body, alias).Exists(), alias)
			}
			if tc.path == "/ent/v2/text2video" {
				require.Equal(t, float64(5), metadata.DurationSeconds)
				require.Equal(t, "540p", metadata.Resolution)
				require.Equal(t, "16:9", gjson.GetBytes(body, "aspect_ratio").String())
			}
		})
	}
}

func TestVideoKemeiViduRejectsAmbiguousParametersBeforeSubmit(t *testing.T) {
	for _, body := range []string{
		`{"images":["a"],"image_urls":["b"]}`, `{"images":["a"],"duration":"5"}`, `{"images":["a"],"duration":-1}`,
		`{"images":["a"],"duration":5.5}`, `{"images":["a"],"audio":"false"}`, `{"images":["a"],"off_peak":1}`,
		`{"images":["a"],"audios":["a"],"sounds":["b"]}`, `{"images":["a"],"ratio":"16:9","aspect_ratio":"9:16"}`,
		`{"images":["a"],"generate_audio":false,"audio":true}`, `{"images":["a"],"content":[]}`, `{"images":["a"],"Sounds":["b"]}`,
		`{"images":["a"],"images":["b"]}`, `{"images":[""]}`, `{"images":[{}]}`, `{"images":null}`, `{"images":["a"],"sounds":[1]}`,
	} {
		var values map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(body), &values))
		videoProtocolPut(values, "model", "viduq3-pro")
		encoded := []byte(`{"model":"viduq3-pro",` + body[1:])
		_, _, err := PrepareVideoProtocolRequest(encoded, "", viduProtocolFixture("/ent/v2/img2video"))
		require.Error(t, err, body)
	}
	for _, tc := range []struct{ path, body string }{
		{"/ent/v2/text2video", `{"model":"m","prompt":"p","images":["a"]}`},
		{"/ent/v2/text2video", `{"model":"m"}`},
		{"/ent/v2/img2video", `{"model":"m"}`},
		{"/ent/v2/reference2video", `{"model":"m","images":[]}`},
		{"/ent/v2/start-end2video", `{"model":"m","images":["a"]}`},
	} {
		_, _, err := PrepareVideoProtocolRequest([]byte(tc.body), "", viduProtocolFixture(tc.path))
		require.Error(t, err, tc.path)
	}
	q4 := viduProtocolFixture("/ent/v2/text2video")
	q4.Model = "viduq4-pro"
	_, _, err := PrepareVideoProtocolRequest([]byte(`{"model":"viduq4-pro","prompt":"p"}`), "", q4)
	require.Error(t, err)
}

func TestVideoKemeiViduSoundsOnlyForReferenceOperation(t *testing.T) {
	for _, tc := range []struct{ path, images string }{
		{"/ent/v2/text2video", `[]`},
		{"/ent/v2/img2video", `["first"]`},
		{"/ent/v2/start-end2video", `["first","last"]`},
		{"/ent/v2/reference2video", `["reference"]`},
	} {
		for _, alias := range []string{"sounds", "audios"} {
			body := []byte(`{"model":"viduq3-pro","prompt":"p","images":` + tc.images + `,"` + alias + `":["https://cdn.example/audio.wav"]}`)
			actual, _, err := PrepareVideoProtocolRequest(body, "", viduProtocolFixture(tc.path))
			if tc.path == "/ent/v2/reference2video" {
				require.NoError(t, err)
				require.Equal(t, "https://cdn.example/audio.wav", gjson.GetBytes(actual, "sounds.0").String())
				require.False(t, gjson.GetBytes(actual, "audios").Exists())
			} else {
				require.ErrorContains(t, err, "only supported by reference2video", tc.path+"/"+alias)
			}
			// 空数组没有参考音频，保留原有可接受的空素材表示。
			body = []byte(`{"model":"viduq3-pro","prompt":"p","images":` + tc.images + `,"` + alias + `":[]}`)
			_, _, err = PrepareVideoProtocolRequest(body, "", viduProtocolFixture(tc.path))
			require.NoError(t, err, tc.path+"/"+alias)
		}
	}
}

func TestVideoKemeiViduAcceptanceEvidencePreventsRetry(t *testing.T) {
	for _, fragment := range []string{
		`"state":"created"`, `"state":"queueing"`, `"state":"queued"`, `"state":"processing"`, `"state":"success"`,
		`"creations":[{"url":"https://cdn.example/video.mp4"}]`, `"creations":[{"id":"product-only"}]`, `"creations":[{}]`,
		`"state":"failed","state":"created"`, `"creations":[],"creations":[{"id":"product-only"}]`,
	} {
		body := []byte(`{"error":{"code":"service_unavailable","message":"service unavailable"},` + fragment + `}`)
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			require.False(t, VideoCreationDefinitelyRejected(status, body), string(body))
			task := &VideoTaskRecord{Status: "failed", BillingStatus: "reserved", CreateResponseStatus: status, CreateResponse: body}
			require.False(t, VideoTaskCanRetrySubmission(task), string(body))
		}
	}
	for _, body := range []string{
		`{"error":{"code":"service_unavailable","message":"service unavailable"},"creations":[]}`,
		`{"error":{"code":"service_unavailable","message":"service unavailable"},"input":{"state":"success","creations":[{"url":"echo"}]}}`,
	} {
		// 用户输入回显与空产物不影响既有的明确未受理规则。
		require.True(t, VideoCreationDefinitelyRejected(http.StatusServiceUnavailable, []byte(body)))
	}
}

// 禁止重提与既定退款政策分开：完整错误响应没有任务 ID 时仍只释放一次原预扣。
func TestVideoKemeiViduAcceptanceEvidenceKeepsRefundPolicy(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, fragment := range []string{`"state":"created"`, `"state":"processing"`, `"state":"success"`, `"creations":[{"url":"https://cdn.example/video.mp4"}]`} {
			svc, key, upstream, billing, _, repo := newVideoRetryFixture()
			for _, selected := range upstream.selections {
				selected.Target.Version = 2
				selected.Target.Endpoint = VideoEndpointVidu
				selected.Target.ProtocolAdapter = "kemei_v1"
				selected.Target.CreatePath = "/ent/v2/img2video"
			}
			body := []byte(`{"error":{"code":"service_unavailable"},` + fragment + `}`)
			response, err := ExtractVideoProtocolResponse(upstream.selections[0].Target, status, body, "")
			require.NoError(t, err)
			upstream.responses = []*VideoUpstreamResponse{response}
			created, err := svc.Submit(context.Background(), key, VideoTaskSubmitRequest{Native: true, InboundProtocol: "vidu", NativePath: "/ent/v2/img2video", Body: []byte(`{"model":"video-model","images":["reference"]}`)})
			require.NoError(t, err)
			require.Equal(t, status, created.StatusCode)
			require.Equal(t, []int64{4}, upstream.attempted)
			require.Equal(t, 1, billing.reserves)
			require.Equal(t, 1, billing.releases)
			require.Zero(t, billing.captures)
			task, err := repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "released", task.BillingStatus)
			require.Empty(t, task.RejectedAttempts)
			svc.advance(context.Background(), task)
			require.Equal(t, 1, billing.releases)
			require.Equal(t, []int64{4}, upstream.attempted)
		}
	}
}

func TestVideoKemeiViduTaskIdentityAndResponses(t *testing.T) {
	target := viduProtocolFixture("/ent/v2/img2video")
	created := []byte(`{"task_id":"task-1","type":"img2video","state":"created","model":"viduq3-pro","duration":5}`)
	result, err := ExtractVideoProtocolResponse(target, 200, created, "")
	require.NoError(t, err)
	require.Equal(t, "task-1", result.TaskID)
	require.Equal(t, "queued", result.Status)
	require.Equal(t, float64(5), result.Metadata.DurationSeconds)
	completed := []byte(`{"id":"task-1","type":"img2video","state":"success","err_code":"","err_msg":"","creations":[{"id":"different-product-id","url":"https://cdn.example/movie.mp4","cover_url":"https://cdn.example/cover.png"}]}`)
	result, err = ExtractVideoProtocolResponse(target, 200, completed, "task-1")
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, "https://cdn.example/movie.mp4", result.VideoURL)
	require.Nil(t, result.Metadata.Tokens)
	require.Empty(t, result.MetadataError)
	for _, body := range []string{`{"id":"foreign","state":"success"}`, `{"task_id":"task-1","id":"foreign","state":"failed"}`, `{"id":"task-1","id":"foreign","state":"failed"}`} {
		_, err := ExtractVideoProtocolResponse(target, 200, []byte(body), "task-1")
		require.Error(t, err)
	}
	result, err = ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"task-1","state":"failed","err_code":"invalid_image"}`), "task-1")
	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	for _, body := range []string{
		`{"id":"task-1","state":"success","err_code":"failed","creations":[{"url":"https://cdn.example/v"}]}`,
		`{"id":"task-1","state":"success","creations":[]}`,
		`{"id":"task-1","state":"success","duration":"bad","creations":[{"url":"https://cdn.example/v"}]}`,
	} {
		result, err = ExtractVideoProtocolResponse(target, 200, []byte(body), "task-1")
		require.NoError(t, err)
		require.NotEmpty(t, result.MetadataError)
	}
}

func TestVideoKemeiViduUpstreamPathsAndExplicitConfiguration(t *testing.T) {
	a := videoFixtureAccount(VideoEndpointVidu)
	require.Error(t, normalizeVideoCredentials(a))
	a.Credentials["video_endpoint_adapters"] = map[string]string{"vidu": "kemei_v1"}
	a.Credentials["video_vidu_model_paths"] = map[string]string{"viduq3-pro": "/ent/v2/img2video"}
	a.Credentials["video_model_paths"] = map[string]string{"viduq3-pro": "/image-to-video/{model}"}
	require.NoError(t, normalizeVideoCredentials(a))
	svc := NewVideoUpstreamService(&OpenAIGatewayService{})
	// Vidu 操作路径来自模型协议绑定，客户端不能再通过原生路径改写。
	target, err := svc.resolveTarget(context.Background(), a, "viduq3-pro")
	require.NoError(t, err)
	require.Equal(t, "/ent/v2/img2video", target.CreatePath)
	// 可美 Vidu 方案保存时改绑为对应操作的预置模板。
	require.Equal(t, videoTargetVersionProfile, target.Version)
	require.Equal(t, "profile:kemei-vidu-img2video", VideoAccountTestProtocol(a, "viduq3-pro"))
	target = viduProtocolFixture("/ent/v2/img2video")
	path, err := VideoProtocolTaskPath(target, "task-1")
	require.NoError(t, err)
	require.Equal(t, "/ent/v2/tasks/task-1/creations", path)
	require.Equal(t, "https://vidu.example/ent/v2/tasks/task-1/creations", videoEndpointURL(target.BaseURL, path))
	fixture := &videoAccountTestHTTP{handle: func(_ *http.Request, _ []byte) (*http.Response, error) {
		return videoAccountTestJSON(http.StatusOK, `{"id":"task-1","state":"processing"}`), nil
	}}
	svc = NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
	_, err = svc.Poll(context.Background(), a, target, "task-1")
	require.NoError(t, err)
	require.Equal(t, "/ent/v2/tasks/task-1/creations", fixture.requests[0].URL.Path)
	require.Equal(t, "Bearer fixture-secret", fixture.requests[0].Header.Get("Authorization"))
}

// 真实协议解析后的状态接入既有生命周期，模拟重启和重复查询仍只能捕获或释放一次。
func TestVideoKemeiViduLifecyclePreservesSettlementAndRefund(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoPerRequest, BillingModeVideoToken} {
		for _, success := range []bool{false, true} {
			t.Run(string(mode)+map[bool]string{true: "/success", false: "/failed"}[success], func(t *testing.T) {
				svc, key, upstream, billing, logs := newVideoLifecycleFixture(mode)
				target := viduProtocolFixture("/ent/v2/img2video")
				target.Model = "video-model"
				target.AccountID = upstream.selection.Account.ID
				upstream.selection.Target = target
				createdBody := []byte(`{"task_id":"upstream-task","state":"created","duration":8,"resolution":"720p"}`)
				var err error
				upstream.submit, err = ExtractVideoProtocolResponse(target, 200, createdBody, "")
				require.NoError(t, err)
				req := VideoTaskSubmitRequest{Native: true, NativePath: target.CreatePath, InboundProtocol: "vidu", Body: []byte(`{"model":"video-model","images":["a"],"duration":8,"resolution":"720p"}`), IdempotencyKey: "vidu-one"}
				created, err := svc.Submit(context.Background(), key, req)
				require.NoError(t, err)
				require.Equal(t, 1, billing.reserves)
				body := []byte(`{"id":"upstream-task","state":"failed","err_code":"invalid_image"}`)
				if success {
					body = []byte(`{"id":"upstream-task","state":"success","creations":[{"id":"asset","url":"https://cdn.example/video.mp4"}]}`)
				}
				upstream.poll, err = ExtractVideoProtocolResponse(target, 200, body, "upstream-task")
				require.NoError(t, err)
				restarted := *svc
				for range 3 {
					task, err := svc.repo.Get(context.Background(), created.LocalTaskID)
					require.NoError(t, err)
					restarted.advance(context.Background(), task)
				}
				task, err := svc.repo.Get(context.Background(), created.LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, target, task.Target)
				if !success {
					require.Equal(t, "released", task.BillingStatus)
					require.Equal(t, 1, billing.releases)
					require.Zero(t, billing.captures)
				} else if mode == BillingModeVideoToken {
					// 文档没有 Token 用量；不能把缺失用量误认为免费成功。
					require.Equal(t, "reconciliation", task.BillingStatus)
					require.Zero(t, billing.captures+billing.releases)
				} else {
					require.Equal(t, "settled", task.BillingStatus)
					require.Equal(t, 1, billing.captures)
					require.Zero(t, billing.releases)
					require.Len(t, logs.logs, 1)
					want := 16.0
					if mode == BillingModeVideoPerRequest {
						want = 2.0
					}
					require.Equal(t, want, task.BillingResult.ActualAmountUSD)
				}
				_, err = svc.Submit(context.Background(), key, req)
				require.NoError(t, err)
				require.Equal(t, 1, upstream.submitted)
				require.Equal(t, 1, billing.reserves)
			})
		}
	}
}
