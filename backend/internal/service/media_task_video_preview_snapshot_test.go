//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 成功结果只保留产物属性；处理中、认证代理与不可信 URL 都不能生成播放快照。
func TestVideoTaskPreviewSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		endpoint  GrokMediaEndpoint
		body      string
		available bool
	}{
		{"grok complete", GrokMediaEndpointVideoStatus, `{"status":"done","video":{"url":"https://vidgen.x.ai/result.mp4?signature=test","width":864,"height":496,"duration":10.1,"size_bytes":1024,"mime_type":"video/mp4"},"prompt":"must not retain"}`, true},
		{"grok pending", GrokMediaEndpointVideoStatus, `{"status":"pending","video":{"url":"https://vidgen.x.ai/result.mp4"}}`, false},
		{"grok relay", GrokMediaEndpointVideoStatus, `{"status":"done","video":{"url":"https://relay.example/v1/videos/task/content"}}`, false},
		{"grok other host", GrokMediaEndpointVideoStatus, `{"status":"done","video":{"url":"https://other.example/result.mp4"}}`, false},
		{"image endpoint", GrokMediaEndpointImagesGenerations, `{"status":"done","video":{"url":"https://vidgen.x.ai/result.mp4"}}`, false},
		{"seedance complete", SeedanceEndpointStatus, `{"status":"succeeded","content":{"video_url":"https://media.example/result.mp4","width":864,"height":496,"duration":10.1}}`, true},
		{"seedance failed", SeedanceEndpointStatus, `{"status":"failed","content":{"video_url":"https://media.example/result.mp4"}}`, false},
		{"seedance credentials", SeedanceEndpointStatus, `{"status":"succeeded","content":{"video_url":"https://user:secret@media.example/result.mp4"}}`, false},
		{"seedance relative", SeedanceEndpointStatus, `{"status":"succeeded","content":{"video_url":"/protected/video.mp4"}}`, false},
		{"malformed", SeedanceEndpointStatus, `{"status":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := videoTaskPreviewSnapshot(tc.endpoint, []byte(tc.body), "task")
			if !tc.available {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, 864, got.Width)
			require.Equal(t, 496, got.Height)
			require.Equal(t, 10.1, got.DurationSeconds)
		})
	}
}

func TestVideoTaskPreviewSnapshotDoesNotGuessMetadata(t *testing.T) {
	got := videoTaskPreviewSnapshot(SeedanceEndpointStatus, []byte(`{"status":"succeeded","duration":8,"resolution":"1080p","content":{"video_url":"https://media.example/result.mp4","width":"1920","height":-1,"duration":1e100}}`), "task")
	require.NotNil(t, got)
	require.Zero(t, got.Width)
	require.Zero(t, got.Height)
	require.Zero(t, got.DurationSeconds)
	require.Empty(t, got.MimeType)
}
