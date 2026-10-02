package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 两个统一入口必须分别启用对应端点，任务查询继续使用创建时冻结的路径。
func TestVideoOpenAIEndpointSelectionAndFrozenPaths(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.Credentials["video_endpoints"] = []string{"compat", "seedance", "openai_videos"}
	account.Credentials["video_model_bindings"] = map[string]any{"m": []string{"seedance", "openai_videos", "compat"}}
	account.Credentials["video_base_urls"] = map[string]string{"openai_videos": "https://openai-video.example/proxy/v1"}
	require.NoError(t, normalizeVideoCredentials(account))
	svc := NewVideoUpstreamService(&OpenAIGatewayService{})
	target, err := svc.resolveTarget(account, "m", VideoTaskSubmitRequest{InboundProtocol: "openai_videos"})
	require.NoError(t, err)
	require.Equal(t, VideoEndpointOpenAIVideos, target.Endpoint)
	require.Equal(t, "/v1/videos", target.CreatePath)
	require.Equal(t, "https://openai-video.example/proxy/v1/videos", videoEndpointURL(target.BaseURL, target.CreatePath))
	path, err := videoTaskPath(target, "upstream-id")
	require.NoError(t, err)
	require.Equal(t, "/v1/videos/upstream-id", path)
	_, err = videoTaskPath(target, "../other")
	require.Error(t, err)
	legacy, err := svc.resolveTarget(account, "m", VideoTaskSubmitRequest{InboundProtocol: "unified"})
	require.NoError(t, err)
	require.Equal(t, VideoEndpointCompat, legacy.Endpoint)
	for _, tc := range []struct {
		endpoints []VideoEndpoint
		want      VideoEndpoint
		fail      bool
	}{
		{[]VideoEndpoint{VideoEndpointOpenAIVideos}, VideoEndpointOpenAIVideos, false},
		{[]VideoEndpoint{VideoEndpointCompat, VideoEndpointSeedance}, "", true},
		{[]VideoEndpoint{VideoEndpointSeedance}, "", true},
		{[]VideoEndpoint{VideoEndpointSeedance, VideoEndpointMiniMax}, "", true},
	} {
		cfg := VideoAccountConfiguration{Endpoints: tc.endpoints}
		got, err := cfg.SelectEndpoint("m", "openai_videos", false)
		if tc.fail {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		}
	}
}

// JSON 扩展参数原样转发，查询只访问原协议，未声明的取消不能发出 DELETE。
func TestVideoOpenAIUpstreamPreservesJSONAndStatuses(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	fixture := &videoHTTPFixture{status: http.StatusAccepted, response: `{"id":"upstream-id","object":"video","status":"queued"}`}
	svc := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
	target, err := svc.resolveTarget(account, "m", VideoTaskSubmitRequest{InboundProtocol: "openai_videos"})
	require.NoError(t, err)
	body := []byte(`{"model":"m","prompt":"test","duration":8,"resolution":"720p","ratio":"16:9","images":["https://image.example/a"],"future":{"flag":false}}`)
	metadata, err := ParseVideoEndpointRequestMetadata(body, "", target.Endpoint)
	require.NoError(t, err)
	require.Equal(t, 8.0, metadata.DurationSeconds)
	require.Equal(t, "720p", metadata.Resolution)
	require.NotNil(t, metadata.ReferenceImageCount)
	require.Equal(t, 1, *metadata.ReferenceImageCount)
	require.False(t, metadata.HasReferenceVideo)
	created, err := svc.Submit(context.Background(), &VideoUpstreamSelection{Account: account, Target: target, Body: body})
	require.NoError(t, err)
	require.Equal(t, "upstream-id", created.TaskID)
	require.Equal(t, body, fixture.requestBody)
	require.Equal(t, "/prefix/v1/videos", fixture.request.URL.Path)
	fixture.response = `{"id":"upstream-id","status":"in_progress"}`
	polled, err := svc.Poll(context.Background(), account, target, created.TaskID)
	require.NoError(t, err)
	require.Equal(t, "processing", polled.Status)
	require.Equal(t, http.MethodGet, fixture.request.Method)
	require.Equal(t, "/prefix/v1/videos/upstream-id", fixture.request.URL.Path)
	_, err = svc.Cancel(context.Background(), account, target, created.TaskID)
	require.Error(t, err)
	require.Equal(t, 2, fixture.calls)
	for _, status := range []string{"queued", "in_progress", "completed", "failed"} {
		result := ExtractVideoUpstreamResponse(VideoEndpointOpenAIVideos, []byte(`{"id":"upstream-id","status":"`+status+`"}`), "")
		require.NotEmpty(t, result.Status)
		require.Nil(t, result.Metadata.Tokens)
	}
}

// 同一付费创建只发一次；新旧统一入口分别保持自己的响应形状和本地任务 ID。
func TestVideoOpenAIUnifiedLifecycleShapeAndIdempotency(t *testing.T) {
	svc, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	request := VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","duration":8,"resolution":"720p"}`), InboundProtocol: "openai_videos", IdempotencyKey: "once"}
	created, err := svc.Submit(context.Background(), key, request)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(created.LocalTaskID, "vid_"))
	require.Equal(t, created.LocalTaskID, gjson.GetBytes(created.Body, "id").String())
	require.Equal(t, "video", gjson.GetBytes(created.Body, "object").String())
	require.Equal(t, "queued", gjson.GetBytes(created.Body, "status").String())
	replayed, err := svc.Submit(context.Background(), key, request)
	require.NoError(t, err)
	require.Equal(t, created.LocalTaskID, replayed.LocalTaskID)
	require.Equal(t, 1, upstream.submitted)
	require.Equal(t, 1, billing.reserves)
	repo := svc.repo.(*videoLifecycleRepo)
	task := repo.tasks[created.LocalTaskID]
	require.Equal(t, "/v1/videos", task.InboundPath)
	for state, expected := range map[string]string{"queued": "queued", "processing": "in_progress", "submission_unknown": "in_progress", "completed": "completed", "failed": "failed", "cancelled": "failed"} {
		task.Status = state
		task.NextPollAt = time.Now().Add(time.Hour)
		got, err := svc.Query(context.Background(), key, task.ID, "openai_videos")
		require.NoError(t, err)
		require.Equal(t, "video", gjson.GetBytes(got.Body, "object").String())
		require.Equal(t, expected, gjson.GetBytes(got.Body, "status").String())
		if expected == "failed" {
			require.NotEmpty(t, gjson.GetBytes(got.Body, "error.message").String())
		}
		if state == "submission_unknown" {
			require.Equal(t, state, gjson.GetBytes(got.Body, "task_status").String())
		}
	}
	legacy, err := svc.Query(context.Background(), key, task.ID, "unified")
	require.NoError(t, err)
	require.Equal(t, "video.generation", gjson.GetBytes(legacy.Body, "object").String())
	require.Zero(t, upstream.polls)
	require.Zero(t, billing.captures)
}

// 终态缺失 Token 仍待对账，不允许新协议被误判成免费产物。
func TestVideoOpenAIMissingUsageRemainsUnsettled(t *testing.T) {
	svc, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	upstream.submit = ExtractVideoUpstreamResponse(VideoEndpointOpenAIVideos, []byte(`{"id":"upstream-id","status":"completed"}`), "")
	upstream.submit.StatusCode = http.StatusOK
	result, err := svc.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model"}`), InboundProtocol: "openai_videos"})
	require.NoError(t, err)
	require.Equal(t, "reconciliation", gjson.GetBytes(result.Body, "billing_status").String())
	require.Zero(t, billing.captures)
	_, err = svc.OpenContent(context.Background(), key, result.LocalTaskID, "openai_videos", "")
	require.ErrorIs(t, err, ErrVideoTaskConflict)
}

// 新中继端点必须沿用缺失用量占位和混合 JSON 结构的保守规则，防止漏算视频或图片费用。
func TestVideoOpenAIMetadataKeepsCompatBillingGuards(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","resolution":"720p","parameters":{"resolution":"1080p"}}`,
		`{"model":"m","duration":5,"parameters":{"duration":8}}`,
		`{"model":"m","video_url":"https://video.example/a","content":[{"type":"image_url","image_url":{"url":"https://image.example/a"}}]}`,
	} {
		_, err := ParseVideoEndpointRequestMetadata([]byte(body), "", VideoEndpointOpenAIVideos)
		require.Error(t, err)
	}
	metadata, err := ParseVideoEndpointRequestMetadata([]byte(`{"model":"m","images":["a"],"input":{"images":["b"]}}`), "", VideoEndpointOpenAIVideos)
	require.NoError(t, err)
	require.Nil(t, metadata.ReferenceImageCount)
	for _, body := range []string{`{"status":"completed"}`, `{"status":"completed","usage":{"completion_tokens":0}}`} {
		got := ExtractVideoUpstreamResponse(VideoEndpointOpenAIVideos, []byte(body), "id")
		require.Nil(t, got.Metadata.Tokens)
	}
	got := ExtractVideoUpstreamResponse(VideoEndpointOpenAIVideos, []byte(`{"status":"completed","usage":{"completion_tokens":100}}`), "id")
	require.NotNil(t, got.Metadata.Tokens)
	require.Equal(t, int64(100), *got.Metadata.Tokens)
}
