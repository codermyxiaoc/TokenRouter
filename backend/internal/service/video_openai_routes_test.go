package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 共享入口只识别约定的创建、单任务查询和内容读取，不覆盖旧 Grok 操作子路径。
func TestOpenAIVideoRoutesAndSmartRoutingKeepLegacyGrok(t *testing.T) {
	for _, root := range []string{"/v1/videos", "/videos"} {
		create, ok := MatchVideoGatewayRoute(http.MethodPost, root)
		require.True(t, ok)
		require.Equal(t, "openai_videos", create.Protocol)
		require.True(t, create.Create)
		require.False(t, create.Native)
		for _, suffix := range []string{"/vid_owned", "/vid_owned/content"} {
			route, ok := MatchVideoGatewayRoute(http.MethodGet, root+suffix)
			require.True(t, ok)
			require.Equal(t, "vid_owned", route.TaskID)
			require.Equal(t, suffix == "/vid_owned/content", route.Content)
		}
		for _, path := range []string{root, root + "/vid_owned/content/extra", root + "/generations/task/content"} {
			_, ok = MatchVideoGatewayRoute(http.MethodGet, path)
			require.False(t, ok, path)
		}
		_, ok = MatchVideoGatewayRoute(http.MethodDelete, root+"/vid_owned")
		require.False(t, ok, "未声明取消协议不能自动开放")
		require.True(t, smartRoutingGroupEndpointEligible(PlatformGrok, root))
		require.True(t, smartRoutingGroupEndpointEligible(PlatformVideo, root))
		require.False(t, smartRoutingGroupEndpointEligible(PlatformOpenAI, root))
	}
	legacy, ok := MatchVideoGatewayRoute(http.MethodPost, "/v1/video/generations")
	require.True(t, ok)
	require.Equal(t, "unified", legacy.Protocol)
	kling, ok := MatchVideoGatewayRoute(http.MethodPost, "/v1/videos/text2video")
	require.True(t, ok)
	require.Equal(t, "kling", kling.Protocol)
}
