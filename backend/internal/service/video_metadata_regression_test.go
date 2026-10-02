package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 未知、非整数、负数和溢出的供应商用量都不能转成免费；可信原生零和兼容占位零保持区别。
func TestVideoMetadataRegressionUsageValidity(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax} {
		for _, value := range []string{`null`, `"123"`, `-1`, `1.5`, `1000000000000000`, `{}`, `[]`} {
			got := ExtractVideoUpstreamResponse(endpoint, []byte(`{"status":"completed","usage":{"completion_tokens":`+value+`}}`), "owned")
			require.Nil(t, got.Metadata.Tokens, "endpoint=%s value=%s", endpoint, value)
		}
		got := ExtractVideoUpstreamResponse(endpoint, []byte(`{"status":"completed","usage":{"completion_tokens":0}}`), "owned")
		if endpoint == VideoEndpointCompat {
			require.Nil(t, got.Metadata.Tokens)
		} else {
			require.NotNil(t, got.Metadata.Tokens)
			require.Zero(t, *got.Metadata.Tokens)
		}
		for _, body := range []string{
			`{"status":"completed","usage":{"completion_tokens":123}}`,
			`{"status":"completed","usage":{"output_tokens":123}}`,
			`{"output":{"task_status":"SUCCEEDED","usage":{"completion_tokens":123}}}`,
			`{"data":{"status":"success","usage":{"completion_tokens":123}}}`,
		} {
			got = ExtractVideoUpstreamResponse(endpoint, []byte(body), "owned")
			require.Equal(t, "completed", got.Status)
			require.NotNil(t, got.Metadata.Tokens)
			require.Equal(t, int64(123), *got.Metadata.Tokens)
		}
	}
}

// 万相的原生参数优先级必须保持；兼容入口存在真正冲突时拒绝，而大小写一致别名可以共存。
func TestVideoMetadataRegressionAliasesAndIgnoredFields(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointWan} {
		body := []byte(`{"model":"m","resolution":"720p","size":"720P","duration":5,"images":["same","same"],"image_urls":["same","same"]}`)
		got, err := ParseVideoEndpointRequestMetadata(body, "", endpoint)
		require.NoError(t, err)
		require.Equal(t, "720P", got.Resolution)
		require.NotNil(t, got.ReferenceImageCount)
		require.Equal(t, 2, *got.ReferenceImageCount)
		require.False(t, got.HasReferenceVideo)
	}
	wan := []byte(`{"model":"m","resolution":"480p","duration":1,"videos":["ignored"],"images":["ignored"],"parameters":{"size":"1080P","duration":12},"input":{"media":[{"type":"video_url","url":"video"},{"type":"image_url","url":"image"}]}}`)
	got, err := ParseVideoEndpointRequestMetadata(wan, "", VideoEndpointWan)
	require.NoError(t, err)
	require.Equal(t, "1080P", got.Resolution)
	require.Equal(t, 12., got.DurationSeconds)
	require.True(t, got.HasReferenceVideo)
	require.Equal(t, 1, *got.ReferenceImageCount)
	_, err = ParseVideoEndpointRequestMetadata(wan, "", VideoEndpointCompat)
	require.Error(t, err)
	// 输出、自报收费维度和未知扩展都不能改变输入计费条件。
	got, err = ParseVideoRequestMetadata([]byte(`{"model":"m","resolution":"540p","duration":5,"has_reference_video":true,"tokens":0,"reference_image_count":100,"output":{"video_url":"generated","images":["generated"]},"extension":{"videos":["not-an-input"]}}`), "")
	require.NoError(t, err)
	require.False(t, got.HasReferenceVideo)
	require.Nil(t, got.Tokens)
	require.Zero(t, *got.ReferenceImageCount)
}
