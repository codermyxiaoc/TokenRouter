//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 所有素材仅为本地协议夹具，回归不会创建真实收费视频。
func kemeiProtocolTarget(endpoint VideoEndpoint) VideoUpstreamTarget {
	paths := map[VideoEndpoint]string{
		VideoEndpointCompat: "/v1/video/generations", VideoEndpointMiniMax: "/v2/video_generation",
		VideoEndpointSeedance: "/api/v3/contents/generations/tasks", VideoEndpointWan: "/api/v1/services/aigc/video-generation/video-synthesis",
		VideoEndpointKling: "/image-to-video/model", VideoEndpointVidu: "/ent/v2/img2video",
	}
	return VideoUpstreamTarget{Version: 2, Endpoint: endpoint, Model: "model", ProtocolAdapter: VideoProtocolAdapterKemei, CreatePath: paths[endpoint]}
}

func TestVideoKemeiRequestDocumentExamples(t *testing.T) {
	cases := []struct {
		name     string
		endpoint VideoEndpoint
		body     string
		images   int
		duration float64
		video    bool
	}{
		{"compat minimax roles", VideoEndpointCompat, `{"model":"incoming","prompt":"p","images":[{"url":"a","role":"first_frame"},{"url":"b","type":"last_frame"}],"resolution":"768P","duration":6}`, 2, 6, false},
		{"compat seedance audio only", VideoEndpointCompat, `{"model":"incoming","prompt":"p","audios":["audio"],"duration":-1,"ratio":"adaptive"}`, 0, -1, false},
		{"compat vidu reference", VideoEndpointCompat, `{"model":"incoming","images":["a"],"audios":["audio"],"duration":5,"resolution":"1080p"}`, 1, 5, false},
		{"minimax text", VideoEndpointMiniMax, `{"model":"incoming","prompt":"p","resolution":"2K","duration":5,"ratio":"16:9"}`, 0, 5, false},
		{"minimax two frames", VideoEndpointMiniMax, `{"model":"incoming","prompt":"p","images":["a","b"],"resolution":"768p","duration":5}`, 2, 5, false},
		{"minimax multimodal", VideoEndpointMiniMax, `{"model":"incoming","prompt":"p","images":[{"url":"a","role":"reference_image"}],"videos":["v"],"audios":["audio"],"resolution":"2K","duration":5}`, 1, 5, true},
		{"seedance native", VideoEndpointSeedance, `{"model":"incoming","content":[{"type":"text","text":"p"},{"type":"image_url","image_url":{"url":"a"},"role":"first_frame"}],"duration":8,"resolution":"720p"}`, 1, 8, false},
		{"seedance edit", VideoEndpointSeedance, `{"model":"incoming","prompt":"p","videos":["v"],"duration":-1,"ratio":"adaptive","omni_reference_task_type":"edit"}`, 0, -1, true},
		{"wan reference", VideoEndpointWan, `{"model":"incoming","prompt":"p","images":[{"url":"a","role":"reference_image"}],"videos":["v"],"audios":["audio"],"duration":5,"resolution":"1080P"}`, 1, 5, true},
		{"wan file", VideoEndpointWan, `{"model":"incoming","prompt":"p","files":["https://example.com/file.pdf"],"duration":5}`, 0, 5, false},
		{"wan native", VideoEndpointWan, `{"model":"incoming","input":{"prompt":"p","media":[{"type":"reference_image","url":"a"}]},"parameters":{"duration":5,"resolution":"720P"}}`, 1, 5, false},
		{"kling two frames", VideoEndpointKling, `{"model":"incoming","prompt":"p","images":["a","b"],"duration":5,"resolution":"1080p"}`, 2, 5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, metadata, err := PrepareVideoProtocolRequest([]byte(tc.body), "", kemeiProtocolTarget(tc.endpoint))
			require.NoError(t, err)
			require.NotEmpty(t, body)
			require.Equal(t, "model", metadata.Model)
			require.NotNil(t, metadata.ReferenceImageCount)
			require.Equal(t, tc.images, *metadata.ReferenceImageCount)
			require.Equal(t, tc.duration, metadata.DurationSeconds)
			require.Equal(t, tc.video, metadata.HasReferenceVideo)
		})
	}
}

func TestVideoKemeiReferenceBytesRolesAndExtensions(t *testing.T) {
	target := kemeiProtocolTarget(VideoEndpointMiniMax)
	body, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"incoming","prompt":"p","image_urls":["data:image/jpeg;base64,AAAA","same"],"resolution":"768p","duration":5,"watermark":true,"seed":9007199254740993}`), "", target)
	require.NoError(t, err)
	require.Equal(t, 2, *metadata.ReferenceImageCount)
	require.Equal(t, "first_frame", gjson.GetBytes(body, "content.1.role").String())
	require.Equal(t, "last_frame", gjson.GetBytes(body, "content.2.role").String())
	require.Equal(t, "data:image/jpeg;base64,AAAA", gjson.GetBytes(body, "content.1.image_url.url").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(body, "seed").Raw)
	require.True(t, gjson.GetBytes(body, "aigc_watermark").Bool())
	require.False(t, gjson.GetBytes(body, "images").Exists())
	require.False(t, gjson.GetBytes(body, "image_urls").Exists())

	body, metadata, err = PrepareVideoProtocolRequest([]byte(`{"model":"incoming","prompt":"p","images":["same","same","same"],"duration":5,"resolution":"2K"}`), "", target)
	require.NoError(t, err)
	require.Equal(t, 3, *metadata.ReferenceImageCount)
	for i := 1; i < 4; i++ {
		require.Equal(t, "reference_image", gjson.GetBytes(body, "content").Array()[i].Get("role").String())
	}
}

func TestVideoKemeiRequestRejectsAmbiguousInputs(t *testing.T) {
	cases := []struct {
		endpoint VideoEndpoint
		body     string
	}{
		{VideoEndpointCompat, `{"model":"model","images":["a"],"image_urls":["b"]}`},
		{VideoEndpointCompat, `{"model":"model","images":[{"url":"a","role":"first_frame","type":"reference_image"}]}`},
		{VideoEndpointCompat, `{"model":"model","images":[{"role":"first_frame"}]}`},
		{VideoEndpointCompat, `{"model":"model","Images":["a"]}`},
		{VideoEndpointCompat, `{"model":"model","Videos":["v"]}`},
		{VideoEndpointCompat, `{"model":"model","duration":5,"seconds":"6"}`},
		{VideoEndpointCompat, `{"model":"model","duration":5.2}`},
		{VideoEndpointCompat, `{"model":"model","duration":5,"duration":8}`},
		{VideoEndpointCompat, `{"model":"model","images":["a"],"content":[{"type":"text","text":"p"}]}`},
		{VideoEndpointMiniMax, `{"model":"model","prompt":"p","resolution":"768P","duration":5,"images":["a"],"videos":["v"]}`},
		{VideoEndpointMiniMax, `{"model":"model","prompt":"p","resolution":"768P","duration":-1}`},
		{VideoEndpointMiniMax, `{"model":"model","prompt":"p","resolution":"768P","duration":5}`},
		{VideoEndpointMiniMax, `{"model":"model","content":[{"type":"mystery","url":"a"}],"resolution":"768P","duration":5}`},
		{VideoEndpointWan, `{"model":"model","images":["a"],"input":{"img_url":"b"}}`},
		{VideoEndpointWan, `{"model":"model","duration":5,"parameters":{"duration":8}}`},
		{VideoEndpointKling, `{"model":"model","images":["a"],"contents":[{"type":"first_frame","url":"b"}]}`},
	}
	for _, tc := range cases {
		t.Run(string(tc.endpoint)+tc.body, func(t *testing.T) {
			_, _, err := PrepareVideoProtocolRequest([]byte(tc.body), "", kemeiProtocolTarget(tc.endpoint))
			require.Error(t, err)
		})
	}
}

func TestVideoKemeiAdapterBoundaries(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointMiniMax, VideoEndpointSeedance, VideoEndpointWan, VideoEndpointKling, VideoEndpointVidu} {
		require.True(t, ValidVideoProtocolAdapter(endpoint, VideoProtocolAdapterKemei))
	}
	require.False(t, ValidVideoProtocolAdapter(VideoEndpointOpenAIVideos, VideoProtocolAdapterKemei))
	require.False(t, ValidVideoProtocolAdapter(VideoEndpoint("future"), VideoProtocolAdapterKemei))
	target := kemeiProtocolTarget(VideoEndpointCompat)
	target.CreatePath = "/v1/videos"
	require.Error(t, ValidateVideoProtocolTarget(target))
	target = kemeiProtocolTarget(VideoEndpointKling)
	target.CreatePath = "/omni-video/model"
	require.NoError(t, ValidateVideoProtocolTarget(target))
	body, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"model","prompt":"p","images":[{"url":"a","role":"reference_image"}],"videos":["v"],"duration":5}`), "", target)
	require.NoError(t, err)
	require.Equal(t, 1, *metadata.ReferenceImageCount)
	require.True(t, metadata.HasReferenceVideo)
	require.Equal(t, "refer_image", gjson.GetBytes(body, "contents.1.type").String())
	require.Equal(t, "feature_video", gjson.GetBytes(body, "contents.2.type").String())
}

// 保存时阻止新适配搭配旧路径，同时保留原账号的旧协议路径配置。
func TestVideoKemeiKlingConfigurationPaths(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterKemei, VideoProtocolAdapterLegacy} {
		for _, path := range []string{"/text-to-video/{model}", "/image-to-video/{model}", "/omni-video/{model}", "/v1/videos/text2video", "/v1/videos/omni-video"} {
			a := videoFixtureAccount(VideoEndpointKling)
			a.Credentials["video_endpoint_adapters"] = map[string]string{"kling": adapter}
			a.Credentials["video_model_paths"] = map[string]string{"model": path}
			err := normalizeVideoCredentials(a)
			if adapter == VideoProtocolAdapterKemei && (path == "/v1/videos/text2video" || path == "/v1/videos/omni-video") {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		}
	}
}

// 新原生素材名称和声音枚举来自官方 Omni 合同，不能沿用统一入口的角色名称。
func TestVideoKemeiKlingNativeSettingsAndMedia(t *testing.T) {
	target := kemeiProtocolTarget(VideoEndpointKling)
	target.CreatePath = "/omni-video/model"
	body, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"model","prompt":"p","generate_audio":true,"watermark":true,"callback_url":"https://example.com/callback"}`), "", target)
	require.NoError(t, err)
	require.Equal(t, 0, *metadata.ReferenceImageCount)
	require.Equal(t, float64(5), metadata.DurationSeconds)
	require.Equal(t, "720p", metadata.Resolution)
	require.Equal(t, "native", gjson.GetBytes(body, "settings.audio").String())
	require.True(t, gjson.GetBytes(body, "options.watermark_info.enabled").Bool())
	require.Equal(t, "https://example.com/callback", gjson.GetBytes(body, "options.callback_url").String())
	for _, field := range []string{"generate_audio", "watermark", "callback_url", "prompt"} {
		require.False(t, gjson.GetBytes(body, field).Exists())
	}
	body, metadata, err = PrepareVideoProtocolRequest([]byte(`{"model":"model","contents":[{"type":"prompt","text":"p"},{"type":"refer_image","url":"data:image/png;base64,AAAA","id":"ref"},{"type":"base_video","url":"https://example.com/v.mp4","id":"video"}],"settings":{"audio":"original","duration":8},"options":{"external_task_id":"external"}}`), "", target)
	require.NoError(t, err)
	require.Equal(t, 1, *metadata.ReferenceImageCount)
	require.True(t, metadata.HasReferenceVideo)
	require.Equal(t, float64(8), metadata.DurationSeconds)
	require.False(t, gjson.GetBytes(body, "settings.multi_shot").Bool())
	require.Equal(t, "data:image/png;base64,AAAA", gjson.GetBytes(body, "contents.1.url").String())
	require.Equal(t, "ref", gjson.GetBytes(body, "contents.1.id").String())
	_, metadata, err = PrepareVideoProtocolRequest([]byte(`{"model":"model","contents":[{"type":"element","element_id":"saved-element"}]}`), "", target)
	require.NoError(t, err)
	require.Nil(t, metadata.ReferenceImageCount)
	for _, input := range []string{
		`{"model":"model","prompt":"p","audios":["a"]}`,
		`{"model":"model","contents":[{"type":"reference_image","url":"a"}]}`,
		`{"model":"model","contents":[{"type":"feature_video"}]}`,
		`{"model":"model","contents":[{"type":"first_frame","url":"a"},{"type":"base_video","url":"v"}]}`,
		`{"model":"model","contents":[{"type":"base_video","url":"v"}],"generate_audio":true}`,
		`{"model":"model","contents":[{"type":"feature_video","url":"v"}],"generate_audio":true}`,
		`{"model":"model","prompt":"p","generate_audio":"false"}`,
		`{"model":"model","prompt":"p","generate_audio":true,"settings":{"audio":"off"}}`,
		`{"model":"model","prompt":"p","callback_url":"a","options":{"callback_url":"b"}}`,
		`{"model":"model","prompt":"p","watermark":true,"options":{"watermark_info":{"enabled":false}}}`,
		`{"model":"model","contents":[{"type":"unexpected","url":"a"}]}`,
	} {
		_, _, err = PrepareVideoProtocolRequest([]byte(input), "", target)
		require.Error(t, err, input)
	}
}

// 原生输入同样做媒体验证，不能绕过扁平入口验证后被上游忽略或错误计价。
func TestVideoKemeiWanRejectsMalformedNativeMedia(t *testing.T) {
	target := kemeiProtocolTarget(VideoEndpointWan)
	for _, input := range []string{
		`{"model":"model","input":{"media":[{"type":"reference_image"}]}}`,
		`{"model":"model","input":{"media":[{"type":"unexpected","url":"a"}]}}`,
		`{"model":"model","input":{"media":{"type":"reference_image","url":"a"}}}`,
		`{"model":"model","input":{"media":[{"type":"first_frame","url":"a"},{"type":"reference_image","url":"b"}]}}`,
		`{"model":"model","input":{"media":[{"type":"first_frame","url":"a"}],"img_url":"b"}}`,
	} {
		_, _, err := PrepareVideoProtocolRequest([]byte(input), "", target)
		require.Error(t, err, input)
	}
}

func TestVideoKemeiResponseDocumentExamples(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointMiniMax, VideoEndpointWan, VideoEndpointKling, VideoEndpointSeedance} {
		t.Run(string(endpoint), func(t *testing.T) {
			target := kemeiProtocolTarget(endpoint)
			created, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"task","task_id":"task","status":"pending"}`), "")
			require.NoError(t, err)
			require.Equal(t, "task", created.TaskID)
			require.Equal(t, "queued", created.Status)
			completed, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"task","task_id":"task","status":"completed","data":[{"id":"output-file","url":"https://example.com/video.mp4"}],"usage":{"completion_tokens":0,"prompt_tokens":0,"total_tokens":0}}`), "task")
			require.NoError(t, err)
			require.Equal(t, "completed", completed.Status)
			require.Equal(t, "https://example.com/video.mp4", completed.VideoURL)
			require.Nil(t, completed.Metadata.Tokens)
			require.Empty(t, completed.MetadataError)
			_, err = ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"other","task_id":"other","status":"failed"}`), "task")
			require.Error(t, err)
		})
	}
	mini := kemeiProtocolTarget(VideoEndpointMiniMax)
	created, err := ExtractVideoProtocolResponse(mini, 200, []byte(`{"TaskId":"task"}`), "")
	require.NoError(t, err)
	require.Equal(t, "task", created.TaskID)
	completed, err := ExtractVideoProtocolResponse(mini, 200, []byte(`{"task":{"id":"task","status":"completed","content":{"url":"https://example.com/video.mp4"},"usage":{"output_seconds":5,"completion_tokens":100}}}`), "task")
	require.NoError(t, err)
	require.Equal(t, int64(100), *completed.Metadata.Tokens)
	require.Equal(t, float64(5), completed.Metadata.DurationSeconds)
}

// 实际输出秒数必须进入最终计费元数据，不能只校验后仍使用请求或响应的默认时长。
func TestVideoKemeiResponseActualOutputSeconds(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointMiniMax, VideoEndpointSeedance, VideoEndpointWan, VideoEndpointKling} {
		target := kemeiProtocolTarget(endpoint)
		result, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"task","task_id":"task","status":"completed","duration":5,"usage":{"output_seconds":8},"data":[{"url":"https://example.com/video.mp4"}]}`), "task")
		require.NoError(t, err)
		require.Empty(t, result.MetadataError)
		require.Equal(t, float64(8), result.Metadata.DurationSeconds, endpoint)
	}
	result, err := ExtractVideoProtocolResponse(kemeiProtocolTarget(VideoEndpointWan), 200, []byte(`{"output":{"task_id":"task","task_status":"SUCCEEDED","video_url":"https://example.com/video.mp4"},"duration":5,"usage":{"output_seconds":8,"output_video_duration":10}}`), "task")
	require.NoError(t, err)
	require.Empty(t, result.MetadataError)
	require.Equal(t, float64(10), result.Metadata.DurationSeconds)
}

func TestVideoKemeiResponseRejectsUntrustedFacts(t *testing.T) {
	target := kemeiProtocolTarget(VideoEndpointCompat)
	for _, body := range []string{
		`{"id":"task","status":"completed","usage":{"completion_tokens":-1}}`,
		`{"id":"task","status":"completed","usage":{"completion_tokens":1.2}}`,
		`{"id":"task","status":"completed","usage":{"completion_tokens":"100"}}`,
		`{"id":"task","status":"completed","usage":{"completion_tokens":100,"output_tokens":200}}`,
		`{"id":"task","status":"completed","duration":-1}`,
		`{"id":"task","status":"completed","data":[{"url":"a"},{"url":"b"}]}`,
	} {
		out, err := ExtractVideoProtocolResponse(target, 200, []byte(body), "task")
		require.NoError(t, err)
		require.NotEmpty(t, out.MetadataError)
	}
	out, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"task","task_id":"another","status":"failed"}`), "")
	require.NoError(t, err)
	require.Empty(t, out.TaskID)
	require.Empty(t, out.Status)
	require.NotEmpty(t, out.MetadataError)
}
