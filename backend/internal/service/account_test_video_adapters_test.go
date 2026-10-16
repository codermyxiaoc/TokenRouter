//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 管理员试跑和正式流量必须调用同一版本化协议，不因测试入口而丢图或改变时长。
func TestVideoAccountOfficialAdapterUsesMappedOverride(t *testing.T) {
	for _, withImage := range []bool{false, true} {
		account := videoFixtureAccount(VideoEndpointOpenAIVideos)
		account.Credentials["model_mapping"] = map[string]any{"public": "sora-2"}
		account.Credentials["model_whitelist"] = []string{"sora-2"}
		account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": "legacy_v1"}
		account.Credentials["video_model_adapters"] = map[string]any{"sora-2": map[string]string{"openai_videos": "openai_v1"}}
		require.Equal(t, "openai_v1", VideoAccountTestAdapters(account, "public")[VideoEndpointOpenAIVideos])
		image := ""
		if withImage {
			image = grokModesImageDataURL(t, 8)
		}
		svc, transport := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
			if r.Method == http.MethodPost {
				require.Equal(t, "sora-2", gjson.GetBytes(body, "model").String())
				require.Equal(t, gjson.String, gjson.GetBytes(body, "seconds").Type)
				require.Equal(t, "4", gjson.GetBytes(body, "seconds").String())
				require.False(t, gjson.GetBytes(body, "duration").Exists())
				require.False(t, gjson.GetBytes(body, "images").Exists())
				require.Equal(t, withImage, gjson.GetBytes(body, "input_reference.image_url").Exists())
				return videoAccountTestJSON(202, `{"id":"official-test","status":"queued"}`), nil
			}
			require.Equal(t, "/prefix/v1/videos/official-test", r.URL.Path)
			return videoAccountTestJSON(200, `{"id":"official-test","status":"completed","seconds":"4","size":"1280x720","video":{"url":"https://cdn.example/test.mp4"}}`), nil
		})
		c, recorder := grokModesContext(context.Background())
		err := svc.TestAccountConnectionWithOptions(c, account.ID, "public", "A ball", "video", "", "", AccountTestOptions{
			VideoEndpoint: "openai_videos", Resolution: "1280x720", Ratio: "16:9", ImageDataURL: image,
		})
		require.NoError(t, err)
		require.Len(t, transport.requests, 2)
		require.Contains(t, recorder.Body.String(), `"success":true`)
	}
}

// 官方协议不支持的参数在发出付费请求前停止，不能为了让测试成功静默裁剪秒数或参考图。
func TestVideoAccountOfficialAdapterRejectsUnsupportedDuration(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": "openai_v1"}
	svc, transport := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
		t.Fatal("无效参数不能触发上游请求")
		return nil, nil
	})
	c, _ := grokModesContext(context.Background())
	err := svc.TestAccountConnectionWithOptions(c, account.ID, "sora-2", "A ball", "video", "", "", AccountTestOptions{
		VideoEndpoint: "openai_videos", Duration: videoAccountTestInt(5),
	})
	require.Error(t, err)
	require.Empty(t, transport.requests)
}
