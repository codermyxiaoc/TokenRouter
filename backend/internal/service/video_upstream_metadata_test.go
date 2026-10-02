package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 计费维度必须跟随供应商实际采用的参数结构，不能被被忽略的廉价字段覆盖。
func TestVideoMetadataUsesSelectedProtocolPrecedence(t *testing.T) {
	body := []byte(`{"model":"m","resolution":"480p","duration":1,"videos":["flat-video"],"parameters":{"resolution":"1080p","duration":10},"input":{"media":[{"type":"image_url","url":"image"}]}}`)
	got, err := ParseVideoEndpointRequestMetadata(body, "", VideoEndpointWan)
	require.NoError(t, err)
	require.Equal(t, "1080p", got.Resolution)
	require.Equal(t, float64(10), got.DurationSeconds)
	require.False(t, got.HasReferenceVideo)
	_, err = ParseVideoEndpointRequestMetadata(body, "", VideoEndpointCompat)
	require.Error(t, err)
	for _, endpoint := range []VideoEndpoint{VideoEndpointMiniMax, VideoEndpointSeedance} {
		got, err = ParseVideoEndpointRequestMetadata([]byte(`{"model":"m","videos":["ignored"],"content":[{"type":"image_url","image_url":{"url":"image"}}]}`), "", endpoint)
		require.NoError(t, err)
		require.False(t, got.HasReferenceVideo)
		got, err = ParseVideoEndpointRequestMetadata([]byte(`{"model":"m","images":["ignored"],"content":[{"type":"video_url","video_url":{"url":"video"}}]}`), "", endpoint)
		require.NoError(t, err)
		require.True(t, got.HasReferenceVideo)
	}
	for _, body := range []string{
		`{"model":"m","duration":1,"parameters":{"duration":10}}`,
		`{"model":"m","videos":["ignored"],"content":[{"type":"image_url","image_url":{"url":"image"}}]}`,
		`{"model":"m","images":["ignored"],"input":{"media":[{"type":"video","url":"video"}]}}`,
	} {
		_, err = ParseVideoRequestMetadata([]byte(body), "")
		require.Error(t, err)
	}
}

func TestVideoMetadataRejectsDuplicateJSONKeysAtEveryDepth(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","resolution":"480p","resolution":"1080p"}`,
		`{"model":"m","parameters":{"duration":1,"duration":10}}`,
		`{"model":"m","content":[{"type":"image_url","type":"video_url","url":"video"}]}`,
		`{"model":"m","content":[],"Content":[{"type":"video","url":"video"}]}`,
		`{"model":"m","Duration":5}`,
		`{"model":"m","ext":` + strings.Repeat("[", 65) + `0` + strings.Repeat("]", 65) + `}`,
	} {
		_, err := ParseVideoRequestModel([]byte(body), "")
		require.Error(t, err, body)
	}
	_, err := ParseVideoRequestMetadata([]byte(`{"model":"m","ext":[{"key":1},{"key":2}]}`), "")
	require.NoError(t, err)
}

func TestVideoCompatPlaceholderZeroUsageRemainsUnknown(t *testing.T) {
	body := []byte(`{"status":"completed","usage":{"completion_tokens":0}}`)
	require.Nil(t, ExtractVideoUpstreamResponse(VideoEndpointCompat, body, "task").Metadata.Tokens)
	require.NotNil(t, ExtractVideoUpstreamResponse(VideoEndpointSeedance, body, "task").Metadata.Tokens)
	got := ExtractVideoUpstreamResponse(VideoEndpointCompat, []byte(`{"status":"completed","usage":{"completion_tokens":123}}`), "task")
	require.NotNil(t, got.Metadata.Tokens)
	require.Equal(t, int64(123), *got.Metadata.Tokens)
}

// 只计最终协议生效的输入图片；同一数组和首尾帧重复 URL 都保留传入张数。
func TestVideoMetadataCountsEffectiveReferenceImages(t *testing.T) {
	fixtures := []struct {
		name     string
		endpoint VideoEndpoint
		body     string
		count    int
	}{
		{"compat_flat_duplicates", VideoEndpointCompat, `{"model":"m","images":["same","same",{"url":"same","role":"first_frame"},{"url":"tail","type":"last_frame"}]}`, 4},
		{"compat_alias_same_count_once", VideoEndpointCompat, `{"model":"m","images":["a","b"],"image_urls":["a","b"]}`, 2},
		{"compat_alias_only", VideoEndpointCompat, `{"model":"m","image_urls":[{"image_url":{"url":"a"},"role":"reference_image"}]}`, 1},
		{"compat_typed", VideoEndpointCompat, `{"model":"m","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"a"}},{"type":"video_url","video_url":{"url":"v"}},{"type":"audio_url","audio_url":{"url":"s"}}]}`, 1},
		{"seedance_content_overrides_flat", VideoEndpointSeedance, `{"model":"m","images":["ignored","ignored"],"image_urls":["ambiguous-but-ignored"],"content":[{"type":"image_url","image_url":{"url":"frame"},"role":"first_frame"}]}`, 1},
		{"seedance_content_empty_overrides_flat", VideoEndpointSeedance, `{"model":"m","images":["ignored"],"content":[]}`, 0},
		{"seedance_content_ignores_media", VideoEndpointSeedance, `{"model":"m","content":[{"type":"text","text":"x"}],"media":[{"type":"image_url","url":"extension"}]}`, 0},
		{"seedance_flat_bridge", VideoEndpointSeedance, `{"model":"m","images":[{"url":"a","role":"reference_image"}]}`, 1},
		{"minimax_content_overrides_flat", VideoEndpointMiniMax, `{"model":"m","images":["ignored"],"content":[{"type":"image_url","image_url":{"url":"same"},"role":"first_frame"},{"type":"image_url","image_url":{"url":"same"},"role":"last_frame"}]}`, 2},
		{"minimax_content_video_not_image", VideoEndpointMiniMax, `{"model":"m","images":["ignored"],"content":[{"type":"video_url","video_url":{"url":"v"}},{"type":"audio_url","audio_url":{"url":"a"}}]}`, 0},
		{"minimax_flat_alias", VideoEndpointMiniMax, `{"model":"m","image_urls":["a","a"]}`, 2},
		{"wan_input_media_overrides_root", VideoEndpointWan, `{"model":"m","images":["ignored","ignored"],"input":{"media":[{"type":"image_url","url":"a"},{"type":"image","url":"b"},{"type":"audio","url":"s"}]}}`, 2},
		{"wan_native_image", VideoEndpointWan, `{"model":"m","input":{"img_url":"a"}}`, 1},
		{"wan_native_first_last", VideoEndpointWan, `{"model":"m","input":{"first_frame_url":"same","last_frame_url":"same"}}`, 2},
		{"wan_empty_input_overrides_root", VideoEndpointWan, `{"model":"m","images":["ignored"],"input":{"prompt":"x"}}`, 0},
		{"kling_native_first_last", VideoEndpointKling, `{"model":"m","image":"data:image/png;base64,fixture","image_tail":"data:image/png;base64,fixture"}`, 2},
		{"kling_native_list", VideoEndpointKling, `{"model":"m","image_list":[{"image_url":"a","type":"first_frame"},{"image_url":"b","type":"last_frame"},{"image_url":"a","type":"reference_image"}]}`, 3},
		{"kling_flat_bridge", VideoEndpointKling, `{"model":"m","images":["a"]}`, 1},
		{"unknown_extension_not_scanned", VideoEndpointCompat, `{"model":"m","extension":{"images":["x"]},"output":{"images":["generated"]},"data":[{"image_url":"result"}],"return_last_frame":true,"files":["file"],"links":["page"]}`, 0},
		{"video_audio_not_images", VideoEndpointCompat, `{"model":"m","videos":["v"],"audios":["a"]}`, 0},
		{"unknown_content_item_not_scanned", VideoEndpointCompat, `{"model":"m","content":[{"type":"extension","image_url":{"url":"not-an-input"}}]}`, 0},
		{"caller_count_cannot_override", VideoEndpointCompat, `{"model":"m","images":["a"],"reference_image_count":0}`, 1},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			body := []byte(fixture.body)
			original := append([]byte(nil), body...)
			metadata, err := ParseVideoEndpointRequestMetadata(body, "", fixture.endpoint)
			require.NoError(t, err)
			require.NotNil(t, metadata.ReferenceImageCount)
			require.Equal(t, fixture.count, *metadata.ReferenceImageCount)
			require.Equal(t, original, body, "图片计数不得重写转发正文")
		})
	}
}

// 图片计数歧义保持未知，不能为未配置图片附加费的旧请求新增解析错误。
func TestVideoMetadataAmbiguousImageCountPreservesRequestCompatibility(t *testing.T) {
	fixtures := []struct {
		endpoint VideoEndpoint
		body     string
	}{
		{VideoEndpointCompat, `{"model":"m","images":["a"],"image_urls":["a","b"]}`},
		{VideoEndpointMiniMax, `{"model":"m","images":[],"image_urls":["a"]}`},
		{VideoEndpointSeedance, `{"model":"m","content":null,"images":["a"]}`},
		{VideoEndpointCompat, `{"model":"m","images":["a"],"content":[{"type":"image_url","url":"a"}]}`},
		{VideoEndpointCompat, `{"model":"m","images":["a"],"input":{"images":["a","b"]}}`},
		{VideoEndpointWan, `{"model":"m","input":{"img_url":"a","media":[{"type":"image_url","url":"b"}]}}`},
		{VideoEndpointKling, `{"model":"m","image":"a","image_list":[{"image_url":"b"}]}`},
		{VideoEndpointCompat, `{"model":"m","Images":["a"]}`},
		{VideoEndpointCompat, `{"model":"m","images":[{"Image_url":"a"}]}`},
		{VideoEndpointMiniMax, `{"model":"m","images":{"url":"a"}}`},
		{VideoEndpointMiniMax, `{"model":"m","content":[{"type":"image_url","image_url":{"url":["a","b"]}}]}`},
		{VideoEndpointKling, `{"model":"m","image_list":[{"image_url":"a","role":"unknown"}]}`},
	}
	for _, fixture := range fixtures {
		metadata, err := ParseVideoEndpointRequestMetadata([]byte(fixture.body), "", fixture.endpoint)
		require.NoError(t, err, fixture.body)
		require.Nil(t, metadata.ReferenceImageCount, fixture.body)
	}
}

func TestVideoMetadataPersistsKnownZeroAndUnknownImageCount(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax} {
		metadata, err := ParseVideoEndpointRequestMetadata([]byte(`{"model":"m","prompt":"x"}`), "", endpoint)
		require.NoError(t, err)
		require.NotNil(t, metadata.ReferenceImageCount)
		require.Zero(t, *metadata.ReferenceImageCount)
		raw, err := json.Marshal(metadata)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"reference_image_count":0`)
		var restored VideoRequestMetadata
		require.NoError(t, json.Unmarshal(raw, &restored))
		require.NotNil(t, restored.ReferenceImageCount)
		require.Zero(t, *restored.ReferenceImageCount)
	}
	metadata, err := ParseVideoRequestMetadata([]byte(`{"model":"m","images":["a"],"image_urls":[]}`), "")
	require.NoError(t, err)
	raw, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"reference_image_count"`)
	var restored VideoRequestMetadata
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Nil(t, restored.ReferenceImageCount)
	// 生成结果中的图片、预览与任意同名计数字段不能覆盖创建请求冻结的数量。
	response := ExtractVideoUpstreamResponse(VideoEndpointMiniMax, []byte(`{"status":"completed","reference_image_count":99,"data":[{"image_url":"result"}]}`), "task")
	require.Nil(t, response.Metadata.ReferenceImageCount)
}
