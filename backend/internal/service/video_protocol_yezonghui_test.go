//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 仅使用内存传输，验证供应商合同而不创建真实收费任务。
func yezonghuiVideoFixture() VideoUpstreamTarget {
	return VideoUpstreamTarget{Version: 2, Endpoint: VideoEndpointOpenAIVideos, ProtocolAdapter: VideoProtocolAdapterYezonghui,
		Model: "provider-video", BaseURL: "https://provider.example", CreatePath: "/v1/videos", AccountID: 11}
}

func TestVideoYezonghuiCanonicalRequestAndCanvasAliases(t *testing.T) {
	for _, fields := range []string{
		`"seconds":"15","size":"16:9","image_urls":["https://cdn.example/a.png?signature=opaque","https://cdn.example/a.png?signature=opaque"],"audio":false`,
		`"duration":15,"ratio":"16:9","images":["https://cdn.example/a.png?signature=opaque","https://cdn.example/a.png?signature=opaque"],"generate_audio":false`,
		`"seconds":15,"duration":15,"size":"16:9","aspect_ratio":"16:9","ratio":"16:9","image_urls":["https://cdn.example/a.png?signature=opaque","https://cdn.example/a.png?signature=opaque"],"images":["https://cdn.example/a.png?signature=opaque","https://cdn.example/a.png?signature=opaque"],"audio":false,"generate_audio":false`,
	} {
		body, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"client-model","prompt":"@图片2 running","resolution":" 1080P ",`+fields+`,"video_urls":["https://cdn.example/motion.mp4"],"audio_urls":["https://cdn.example/voice.mp3"],"seed":9223372036854775807,"fps":24,"n":1,"camera_fixed":true,"negative_prompt":"blur","metadata":{"vendor_counter":9007199254740993,"resolution":"1080p"}}`), "", yezonghuiVideoFixture())
		require.NoError(t, err)
		require.Equal(t, "provider-video", gjson.GetBytes(body, "model").String())
		require.Equal(t, "15", gjson.GetBytes(body, "seconds").String())
		require.Equal(t, "16:9", gjson.GetBytes(body, "size").String())
		require.Equal(t, "1080p", metadata.Resolution)
		require.Equal(t, float64(15), metadata.DurationSeconds)
		require.Empty(t, metadata.Size, "比例不能成为像素计费档位")
		require.True(t, metadata.HasReferenceVideo)
		require.Equal(t, 2, *metadata.ReferenceImageCount, "素材顺序和重复引用必须原样保留")
		require.Equal(t, "9007199254740993", gjson.GetBytes(body, "metadata.vendor_counter").Raw)
		require.Equal(t, "9223372036854775807", gjson.GetBytes(body, "seed").Raw)
		require.Equal(t, gjson.False, gjson.GetBytes(body, "audio").Type)
		for _, field := range []string{"duration", "ratio", "aspect_ratio", "images", "generate_audio", "input_reference"} {
			require.False(t, gjson.GetBytes(body, field).Exists(), field)
		}
	}
}

func TestVideoYezonghuiRequiredPricingAndNoInventedRatio(t *testing.T) {
	body, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"client","prompt":"p","seconds":"3600","metadata":{"resolution":"4K"}}`), "", yezonghuiVideoFixture())
	require.NoError(t, err)
	require.Equal(t, "4k", metadata.Resolution)
	require.Equal(t, float64(3600), metadata.DurationSeconds)
	require.Equal(t, 0, *metadata.ReferenceImageCount)
	require.False(t, metadata.HasReferenceVideo)
	require.False(t, gjson.GetBytes(body, "size").Exists())
	require.Equal(t, "4k", gjson.GetBytes(body, "resolution").String())
	_, _, err = PrepareVideoProtocolRequest([]byte(`{"model":"client","prompt":"p","seconds":"5","resolution":"1536p","size":"21:9"}`), "", yezonghuiVideoFixture())
	require.NoError(t, err, "模型允许档位不应被文档中的示例写死")
}

// 公网 URL 校验不执行 DNS；绝对域名末尾点、内网后缀和单标签主机也不能作为素材出站。
func TestVideoYezonghuiPublicURLHostBoundaries(t *testing.T) {
	for _, raw := range []string{
		"http://localhost./a", "http://LOCALHOST./a", "http://api.localhost./a", "http://localhost../a",
		"http://127.0.0.1./a", "http://127.0.0.1../a", "http://192.168.1.1./a",
		"http://worker.local/a", "http://worker.LOCAL./a", "http://worker.internal/a", "http://worker.INTERNAL./a",
		"http://worker/a", "http://worker./a", "http://[::1]/a", "http://[fe80::1%25eth0]/a",
	} {
		require.False(t, validVideoYezonghuiURL(raw), raw)
		body, err := json.Marshal(map[string]any{"model": "m", "prompt": "p", "seconds": 5, "resolution": "720p", "image_urls": []string{raw}})
		require.NoError(t, err)
		_, _, err = PrepareVideoProtocolRequest(body, "", yezonghuiVideoFixture())
		require.Error(t, err, raw)
	}
	for _, raw := range []string{
		"https://cdn.example/image.png?token=opaque", "https://CDN.EXAMPLE./image.png",
		"https://8.8.8.8/image.png", "https://[2606:4700:4700::1111]/image.png",
	} {
		require.True(t, validVideoYezonghuiURL(raw), raw)
	}
}

func TestVideoYezonghuiRejectsInvalidAndAmbiguousInput(t *testing.T) {
	for _, fragment := range []string{
		`"seconds":0`, `"seconds":-1`, `"seconds":"3601"`, `"seconds":5.5`, `"seconds":5.0`, `"seconds":1e1`, `"seconds":"05"`, `"seconds":"+5"`, `"seconds":"5s"`, `"seconds":" 5"`, `"seconds":null`,
		`"duration":"5"`, `"duration":6`, `"Seconds":"5"`,
		`"resolution":"auto"`, `"resolution":"16:9"`, `"resolution":"1280x720"`, `"resolution":null`,
		`"metadata":{"resolution":"4k"}`, `"metadata":null`, `"metadata":"opaque"`,
		`"size":"1280x720"`, `"size":"720p"`, `"size":"adaptive"`, `"size":"0:1"`, `"size":"01:1"`, `"size":"1:65537"`, `"size":"1:1","ratio":"16:9"`,
		`"image_urls":"https://cdn.example/image.png"`, `"image_urls":[1]`, `"image_urls":["data:image/png;base64,AAAA"]`,
		`"images":["https://cdn.example/a"],"image_urls":["https://cdn.example/b"]`,
		`"image_urls":["http://127.0.0.1/a"]`, `"image_urls":["http://192.168.1.1/a"]`, `"image_urls":["http://[::1]/a"]`, `"image_urls":["https://user:password@cdn.example/a"]`,
		`"video_urls":["file:///tmp/a"]`, `"audio_urls":["data:audio/wav;base64,AAAA"]`, `"image_urls":["https://cdn.example/a#fragment"]`,
		`"camera_fixed":"true"`, `"audio":1`, `"audio":true,"generate_audio":false`, `"n":2`, `"n":"1"`, `"seed":9223372036854775808`, `"seed":5.5`, `"fps":0`, `"negative_prompt":false`,
		`"watermark":true`, `"image":"https://cdn.example/a"`, `"input_reference":{"image_url":"https://cdn.example/a"}`, `"unknown_extension":true`,
	} {
		t.Run(fragment, func(t *testing.T) {
			// 片段覆盖基础字段；重复规范字段测试单独提供，避免只因重复字段而通过用例。
			values := map[string]json.RawMessage{}
			require.NoError(t, json.Unmarshal([]byte(`{"model":"client","prompt":"p","seconds":"5","resolution":"720p"}`), &values))
			extra := map[string]json.RawMessage{}
			require.NoError(t, json.Unmarshal([]byte(`{`+fragment+`}`), &extra))
			for key, value := range extra {
				values[key] = value
			}
			input, err := json.Marshal(values)
			require.NoError(t, err)
			_, _, err = PrepareVideoProtocolRequest(input, "", yezonghuiVideoFixture())
			require.Error(t, err)
		})
	}
	for _, body := range []string{
		`{"model":"m","prompt":"p","resolution":"720p"}`,
		`{"model":"m","prompt":"p","seconds":"5"}`,
		`{"model":"m","prompt":" ","seconds":"5","resolution":"720p"}`,
		`{"model":"m","prompt":"p","seconds":"5","seconds":"6","resolution":"720p"}`,
	} {
		_, _, err := PrepareVideoProtocolRequest([]byte(body), "", yezonghuiVideoFixture())
		require.Error(t, err)
	}
}

func TestVideoYezonghuiResponseKeepsRatioSeparateFromPrice(t *testing.T) {
	for _, urlField := range []string{``, `,"url":null`, `,"url":""`, `,"url":"https://cdn.example/result.mp4"`} {
		out, err := ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusOK, []byte(`{"id":"owned","task_id":"owned","object":"video","model":"provider-video","status":"completed","seconds":"15","resolution":"1080P","size":"16:9","usage":{"output_tokens":0}`+urlField+`}`), "owned")
		require.NoError(t, err)
		require.Equal(t, "completed", out.Status)
		require.Empty(t, out.MetadataError)
		require.Equal(t, float64(15), out.Metadata.DurationSeconds)
		require.Equal(t, "1080p", out.Metadata.Resolution)
		require.Empty(t, out.Metadata.Size)
		require.Nil(t, out.Metadata.Tokens)
	}
	out, err := ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusOK, []byte(`{"id":"owned","status":"completed"}`), "owned")
	require.NoError(t, err)
	require.Empty(t, out.MetadataError, "文档允许完成包省略产物链接和计费回显")
	require.Empty(t, out.VideoURL)
	require.Zero(t, out.Metadata.DurationSeconds)
	for _, status := range []string{"unknown", "future_status", "SUCCESS", "FAILURE", "cancelled"} {
		out, err := ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusOK, []byte(`{"id":"owned","status":"`+status+`","url":"https://cdn.example/result.mp4"}`), "owned")
		require.NoError(t, err)
		require.Empty(t, out.Status, "未知状态不能生成终态或释放预扣")
		require.Empty(t, out.VideoURL)
	}
}

func TestVideoYezonghuiResponseRejectsForeignOrAmbiguousTask(t *testing.T) {
	for _, body := range []string{
		`{"id":"foreign","status":"completed"}`, `{"id":"owned","task_id":"foreign","status":"failed"}`,
		`{"id":"owned","id":"foreign","status":"failed"}`, `{"id":"owned","ID":"owned","status":"failed"}`,
		`{"id":123,"status":"failed"}`, `{"id":"owned","task_id":null,"status":"failed"}`,
		`{"id":"owned","status":"completed"} {"id":"foreign"}`,
	} {
		require.False(t, VideoProtocolResponseMatchesTask(yezonghuiVideoFixture(), []byte(body), "owned"), body)
		_, err := ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusOK, []byte(body), "owned")
		require.ErrorIs(t, err, errVideoTaskResponseMismatch)
	}
	for _, body := range []string{`{"id":123,"status":"failed"}`, `{"id":"one","task_id":"two","status":"failed"}`} {
		out, err := ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusBadRequest, []byte(body), "")
		require.NoError(t, err)
		require.True(t, VideoProtocolResponseHasConflictingTaskIDs(yezonghuiVideoFixture(), out.Body))
		task := &VideoTaskRecord{Target: yezonghuiVideoFixture(), Status: "submitting"}
		new(VideoTaskService).applySubmissionResponse(task, out)
		require.Equal(t, "submission_unknown", task.Status, "异常 ID 不能误当无任务拒绝释放费用")
		require.Equal(t, "reconciliation", task.BillingStatus)
	}
	// metadata 的 id 是厂商扩展，不能覆盖顶层的任务归属或状态。
	out, err := ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusOK, []byte(`{"id":"owned","status":"queued","metadata":{"id":"foreign","status":"completed"}}`), "owned")
	require.NoError(t, err)
	require.Equal(t, "queued", out.Status)
}

// 成功状态与业务错误并存、旧路径外壳、非对象和重复字段均不能形成可结算成功包。
func TestVideoYezonghuiContradictoryOrUnknownResponseCannotComplete(t *testing.T) {
	for _, body := range []string{
		`{"id":"owned","status":"completed","error":{"message":"generation failed"}}`,
		`{"id":"owned","status":"completed","Error":{"message":"generation failed"}}`,
		`{"id":"owned","status":"completed","code":"task_not_exist","message":"query rejected"}`,
		`{"id":"owned","status":"completed","code":"success","data":{"status":"FAILURE"}}`,
		`{"id":"owned","status":"completed","data":{"status":"failed"}}`,
		`{"id":"owned","status":"completed","task":{"status":"failed"}}`,
		`{"id":"owned","status":"completed","output":{"error":"failed"}}`,
		`{"id":"owned","status":"completed","status":"failed"}`,
		`{"id":"owned","status":"completed","seconds":"5","seconds":"8"}`,
		`{"id":"owned","status":true}`,
		`{"code":"success","data":{"id":"owned","status":"SUCCESS"}}`,
		`[{"id":"owned","status":"completed"}]`, `null`, `"completed"`, `{}`,
	} {
		out, err := ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusOK, []byte(body), "owned")
		require.NoError(t, err, body)
		require.True(t, out.Status != "completed" || out.MetadataError != "", body)
		require.Empty(t, out.VideoURL, body)
	}
	// 文档明确的失败状态仍正常释放；空错误占位不影响有效成功状态。
	out, err := ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusOK, []byte(`{"id":"owned","status":"failed","error":{"message":"generation failed"}}`), "owned")
	require.NoError(t, err)
	require.Equal(t, "failed", out.Status)
	out, err = ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusOK, []byte(`{"id":"owned","status":"completed","error":null}`), "owned")
	require.NoError(t, err)
	require.Equal(t, "completed", out.Status)
	require.Empty(t, out.MetadataError)
}

func TestVideoYezonghuiResponseMalformedDimensionsRemainUnsettled(t *testing.T) {
	for _, fragment := range []string{`"seconds":"5.5"`, `"seconds":"5","duration":6`, `"resolution":"16:9"`, `"resolution":"1280x720"`, `"Resolution":"4k"`, `"size":"1280x720"`, `"url":"http://127.0.0.1/a"`, `"usage":{"Output_tokens":1}`, `"usage":{"output_tokens":-1}`, `"usage":{"output_tokens":3,"completion_tokens":4}`} {
		out, err := ExtractVideoProtocolResponse(yezonghuiVideoFixture(), http.StatusOK, []byte(`{"id":"owned","status":"completed",`+fragment+`}`), "owned")
		require.NoError(t, err)
		require.NotEmpty(t, out.MetadataError, fragment)
		task := &VideoTaskRecord{Target: yezonghuiVideoFixture(), UpstreamTaskID: "owned", Status: "queued"}
		new(VideoTaskService).applyObservation(task, out)
		require.True(t, task.Status != "completed" || task.PricingMismatch, fragment)
	}
}

func TestVideoYezonghuiExplicitAdapterFrozenAndOriginalAccountContent(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax, VideoEndpointVidu} {
		require.False(t, ValidVideoProtocolAdapter(endpoint, VideoProtocolAdapterYezonghui))
	}
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": VideoProtocolAdapterYezonghui}
	response := func(body, contentType string) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
	}
	fixture := &videoContentHTTPFixture{response: response(`{"id":"owned","status":"queued"}`, "application/json")}
	svc := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
	target := yezonghuiVideoFixture()
	_, err := svc.Submit(context.Background(), &VideoUpstreamSelection{Account: account, Target: target, Body: []byte(`{"model":"provider-video","seconds":"5","resolution":"720p"}`)})
	require.NoError(t, err)
	require.Equal(t, "/v1/videos", fixture.request.URL.Path)
	// 保存后切换账号方案不会改变旧任务的查询协议、路径或认证。
	account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": VideoProtocolAdapterOpenAI}
	fixture.response = response(`{"id":"owned","status":"completed","size":"16:9","resolution":"720p","seconds":"5"}`, "application/json")
	polled, err := svc.Poll(context.Background(), account, target, "owned")
	require.NoError(t, err)
	require.Empty(t, polled.MetadataError)
	require.Equal(t, "completed", polled.Status)
	require.Equal(t, "/v1/videos/owned", fixture.request.URL.Path)
	require.Equal(t, "Bearer fixture-secret", fixture.request.Header.Get("Authorization"))
	fixture.response = response("video-bytes", "video/mp4")
	content, err := svc.OpenContent(context.Background(), account, target, "owned", "")
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, content.Body)
	_ = content.Body.Close()
	require.Equal(t, "/v1/videos/owned/content", fixture.request.URL.Path)
	require.Equal(t, "Bearer fixture-secret", fixture.request.Header.Get("Authorization"))
	require.Equal(t, 3, fixture.calls)
	foreign := *account
	foreign.ID++
	_, err = svc.Poll(context.Background(), &foreign, target, "owned")
	require.Error(t, err)
	_, err = svc.OpenContent(context.Background(), &foreign, target, "owned", "")
	require.Error(t, err)
	require.Equal(t, 3, fixture.calls, "跨账号查询与下载必须在网络前拒绝")
}

// 选号时才根据最终模型冻结协议；未选择新方案的旧账号保持原协议。
func TestVideoYezonghuiSelectionFreezesExplicitModelAdapter(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.Credentials["model_mapping"] = map[string]any{"public-video": "provider-video"}
	account.Credentials["model_whitelist"] = []string{"provider-video"}
	account.Credentials["video_model_bindings"] = map[string]string{"provider-video": "openai_videos"}
	account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": VideoProtocolAdapterOpenAI}
	account.Credentials["video_model_adapters"] = map[string]any{"provider-video": map[string]string{"openai_videos": VideoProtocolAdapterYezonghui}}
	svc := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil)})
	key := &APIKey{GroupID: &account.GroupIDs[0], Group: &Group{ID: account.GroupIDs[0], Platform: PlatformVideo, Status: StatusActive}}
	selected, err := svc.SelectAccount(context.Background(), key, VideoTaskSubmitRequest{InboundProtocol: "openai_videos", Body: []byte(`{"model":"public-video","prompt":"测试","duration":5,"resolution":"1080p","ratio":"16:9"}`)})
	require.NoError(t, err)
	if selected.Release != nil {
		defer selected.Release()
	}
	require.Equal(t, 2, selected.Target.Version)
	require.Equal(t, VideoProtocolAdapterYezonghui, selected.Target.ProtocolAdapter)
	require.Equal(t, "provider-video", selected.Target.Model)
	require.Equal(t, account.ID, selected.Target.AccountID)
	require.Equal(t, "5", gjson.GetBytes(selected.Body, "seconds").String())
	require.Equal(t, "16:9", gjson.GetBytes(selected.Body, "size").String())
	require.Equal(t, "1080p", selected.Metadata.Resolution)
	encoded, err := json.Marshal(selected.Target)
	require.NoError(t, err)
	var frozen VideoUpstreamTarget
	require.NoError(t, json.Unmarshal(encoded, &frozen))
	adapter, err := VideoTargetProtocolAdapter(frozen)
	require.NoError(t, err)
	require.Equal(t, VideoProtocolAdapterYezonghui, adapter)
}

// 新合同不能将零 Token 占位或变化后的价档按请求预估直接结算。
func TestVideoYezonghuiUntrustedBillingDoesNotSettle(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		mode       BillingMode
	}{
		{"zero_tokens_unknown", `{"id":"owned","status":"completed","usage":{"output_tokens":0}}`, BillingModeVideoToken},
		{"resolution_mismatch", `{"id":"owned","status":"completed","resolution":"4k"}`, BillingModeVideo},
		{"completed_with_error", `{"id":"owned","status":"completed","error":{"code":"task_not_exist"}}`, BillingModeVideo},
		{"completed_with_error_code", `{"id":"owned","status":"completed","code":"invalid_resolution"}`, BillingModeVideo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, key, upstream, billing, logs := newVideoLifecycleFixture(tc.mode)
			target := yezonghuiVideoFixture()
			target.Model = "video-model"
			target.AccountID = upstream.selection.Account.ID
			body := []byte(`{"model":"video-model","prompt":"测试","seconds":"8","resolution":"720p","size":"16:9"}`)
			prepared, metadata, err := PrepareVideoProtocolRequest(body, "", target)
			require.NoError(t, err)
			upstream.selection.Target, upstream.selection.Body, upstream.selection.Metadata = target, prepared, metadata
			upstream.submit, err = ExtractVideoProtocolResponse(target, http.StatusOK, []byte(`{"id":"owned","status":"queued"}`), "")
			require.NoError(t, err)
			req := VideoTaskSubmitRequest{Body: body, InboundProtocol: "openai_videos", IdempotencyKey: "yezonghui-once"}
			created, err := svc.Submit(context.Background(), key, req)
			require.NoError(t, err)
			task, err := svc.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			upstream.poll, err = ExtractVideoProtocolResponse(target, http.StatusOK, []byte(tc.body), "owned")
			require.NoError(t, err)
			svc.advance(context.Background(), task)
			fresh, err := svc.repo.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "reconciliation", fresh.BillingStatus)
			_, err = svc.Submit(context.Background(), key, req)
			require.NoError(t, err)
			svc.advance(context.Background(), fresh)
			require.Equal(t, 1, billing.reserves)
			require.Equal(t, 1, upstream.submitted)
			require.Zero(t, billing.captures+billing.releases)
			require.Empty(t, logs.logs)
		})
	}
}
