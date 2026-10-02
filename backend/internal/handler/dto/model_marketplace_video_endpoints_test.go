package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 公开 DTO 只传递规范入站路径，不改变非视频模型的历史响应形状。
func TestModelMarketplaceVideoEndpointsProjection(t *testing.T) {
	groups := ModelMarketplaceGroupsFromService([]service.ModelMarketplaceGroup{{Models: []service.ModelMarketplaceModel{
		{ID: "video", VideoEndpoints: []service.ModelMarketplaceVideoEndpoint{{Method: "POST", Path: "/v1/videos", Protocol: "openai_videos"}}},
		{ID: "text"},
	}}})
	require.Equal(t, []ModelMarketplaceVideoEndpoint{{Method: "POST", Path: "/v1/videos", Protocol: "openai_videos"}}, groups[0].Models[0].VideoEndpoints)
	video, err := json.Marshal(groups[0].Models[0])
	require.NoError(t, err)
	require.Contains(t, string(video), `"video_endpoints":[{"method":"POST","path":"/v1/videos","protocol":"openai_videos"}]`)
	text, err := json.Marshal(groups[0].Models[1])
	require.NoError(t, err)
	require.NotContains(t, string(text), "video_endpoints")
}
