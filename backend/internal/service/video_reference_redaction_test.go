package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 只遮罩本站新路由的完整能力票据，不改变已有上游错误类别、资源 ID 或原始响应存储。
func TestVideoReferenceUpstreamErrorRedactsTicket(t *testing.T) {
	token := strings.Repeat("abcdef01", 8)
	for _, suffix := range []string{"", ".", "?retry=1", "\"", ")", "\n"} {
		input := "Failed fetching https://public.example.com/api/v1/video-references/" + token + suffix
		want := "Failed fetching https://public.example.com/api/v1/video-references/[redacted]" + suffix
		require.Equal(t, want, sanitizeUpstreamErrorMessage(input))
	}
	require.Equal(t, "/api/v1/video-references/[redacted]", sanitizeUpstreamErrorMessage("/api/v1/video-references/"+strings.ToUpper(token)))
	require.Equal(t, "https://public.example.com/api/v1/video-references/[redacted]?key=***&retry=1", sanitizeUpstreamErrorMessage("https://public.example.com/api/v1/video-references/"+token+"?key=secret&retry=1"))
	for _, unchanged := range []string{"", "invalid size", "https://vendor.example/tasks/" + token, "/api/v1/video-references/not-a-ticket", "/api/v1/video-references/" + token[:63], "/api/v1/video-references/" + token + "a"} {
		require.Equal(t, unchanged, sanitizeUpstreamErrorMessage(unchanged))
	}
}
