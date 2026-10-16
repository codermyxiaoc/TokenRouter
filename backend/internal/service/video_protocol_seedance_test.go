//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 型号限制按最终上游模型判断，公开别名、未知接入点和其他供应商方案不能被误匹配。
func TestVideoOfficialSeedanceResolution(t *testing.T) {
	for _, tc := range []struct {
		model, resolution string
		valid             bool
	}{
		{"doubao-seedance-2-0-mini", "768p", false},
		{"doubao-seedance-2-0-mini-260615", "480p", true},
		{"doubao-seedance-2-0-mini-260615", " 720P ", true},
		{"doubao-seedance-2-0-mini-260615", "1080p", false},
		{"doubao-seedance-2-0-mini-260615", "4k", false},
		{"doubao-seedance-2-0-fast-260128", "720p", true},
		{"doubao-seedance-2-0-fast-260128", "1080p", false},
		{"doubao-seedance-2-0-260128", "4K", true},
		{"doubao-seedance-2-5-260628", "1080p", true},
		{"doubao-seedance-2-5-260628", "4k", false},
		{"doubao-seedance-1-0-pro-250528", "1080p", true},
		{"doubao-seedance-1-0-pro-fast-250528", "4k", false},
		{"ep-model-deployment", "1080p", true},
		{"ep-model-deployment", "768p", false},
		{"custom-doubao-seedance-2-0-mini", "1080p", true},
		{"doubao-seedance-2-0-mini-custom", "1080p", true},
		{"doubao-seedance-2-0-mini", "2K", false},
		{"doubao-seedance-2-0-mini", "720x1280", false},
	} {
		t.Run(tc.model+"/"+tc.resolution, func(t *testing.T) {
			target := officialProtocolTarget(VideoEndpointSeedance, VideoProtocolAdapterSeedance)
			target.Model = tc.model
			raw, err := json.Marshal(map[string]any{"model": "public-alias", "prompt": "A ball", "resolution": tc.resolution, "duration": 5})
			require.NoError(t, err)
			body, metadata, err := PrepareVideoProtocolRequest(raw, "", target)
			if !tc.valid {
				require.ErrorContains(t, err, "Seedance")
				require.Empty(t, body)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.model, gjson.GetBytes(body, "model").String())
			require.Equal(t, strings.ToLower(strings.TrimSpace(tc.resolution)), gjson.GetBytes(body, "resolution").String())
			require.Equal(t, gjson.GetBytes(body, "resolution").String(), metadata.Resolution)
			require.Equal(t, float64(5), metadata.DurationSeconds)
		})
	}
}

// 不注入默认档位；原生首帧结构、图片数量、扩展数字精度以及自动时长继续保留。
func TestVideoOfficialSeedanceResolutionPreservesNativePayloadAndDefaults(t *testing.T) {
	target := officialProtocolTarget(VideoEndpointSeedance, VideoProtocolAdapterSeedance)
	target.Model = "doubao-seedance-2-0-mini-260615"
	body, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"alias","duration":-1,"ratio":"adaptive","content":[{"type":"text","text":"A ball"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"},"role":"first_frame"}],"extension":9007199254740993}`), "", target)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "resolution").Exists())
	require.Empty(t, metadata.Resolution)
	require.Equal(t, float64(-1), metadata.DurationSeconds)
	require.Equal(t, 1, *metadata.ReferenceImageCount)
	require.Equal(t, "data:image/png;base64,AAAA", gjson.GetBytes(body, "content.1.image_url.url").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(body, "extension").Raw)
	for _, value := range []string{`null`, `720`, `true`, `""`, `["720p"]`, `{"value":"720p"}`} {
		_, _, err = PrepareVideoProtocolRequest([]byte(`{"model":"alias","prompt":"p","resolution":`+value+`}`), "", target)
		require.Error(t, err, value)
	}
}

// 官方档位检查不能影响可美、历史 Seedance 透传或 MiniMax 的 768P 合同。
func TestVideoOfficialSeedanceResolutionIsAdapterScoped(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterKemei} {
		target := officialProtocolTarget(VideoEndpointSeedance, adapter)
		target.Model = "doubao-seedance-2-0-mini"
		raw := []byte(`{"model":"doubao-seedance-2-0-mini","prompt":"p","duration":5,"resolution":"768p","ratio":"16:9"}`)
		body, metadata, err := PrepareVideoProtocolRequest(raw, "", target)
		require.NoError(t, err, adapter)
		require.Equal(t, "768p", metadata.Resolution)
		if adapter == VideoProtocolAdapterLegacy {
			require.Equal(t, raw, body)
		}
	}
	_, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"model","prompt":"p","duration":5,"resolution":"768p"}`), "", officialProtocolTarget(VideoEndpointMiniMax, VideoProtocolAdapterMiniMax))
	require.NoError(t, err)
	require.Equal(t, "768P", metadata.Resolution)
}

// 完整账号测试在映射及模型协议覆盖后校验：非法档位零请求，合法或留空只创建一次并查询同一任务。
func TestVideoAccountOfficialSeedanceResolutionBeforeUpstream(t *testing.T) {
	for _, resolution := range []string{"768p", "1080p", "720P", ""} {
		t.Run(resolution, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointSeedance)
			model := "doubao-seedance-2-0-mini-260615"
			account.Credentials["model_mapping"] = map[string]any{"public": model}
			account.Credentials["model_whitelist"] = []string{model}
			account.Credentials["video_endpoint_adapters"] = map[string]string{"seedance": VideoProtocolAdapterLegacy}
			account.Credentials["video_model_adapters"] = map[string]any{model: map[string]string{"seedance": VideoProtocolAdapterSeedance}}
			invalid := resolution == "768p" || resolution == "1080p"
			svc, transport := videoAccountTestService(account, func(request *http.Request, body []byte) (*http.Response, error) {
				require.False(t, invalid, "无效档位不得发送上游请求")
				if request.Method == http.MethodPost {
					require.Equal(t, model, gjson.GetBytes(body, "model").String())
					require.Equal(t, strings.ToLower(resolution), gjson.GetBytes(body, "resolution").String())
					require.Equal(t, resolution != "", gjson.GetBytes(body, "resolution").Exists())
					require.Equal(t, "/prefix/api/v3/contents/generations/tasks", request.URL.Path)
					return videoAccountTestJSON(200, `{"id":"seedance-test","status":"queued"}`), nil
				}
				require.Equal(t, "/prefix/api/v3/contents/generations/tasks/seedance-test", request.URL.Path)
				return videoAccountTestJSON(200, `{"id":"seedance-test","status":"succeeded","duration":5,"resolution":"720p","content":{"video_url":"https://cdn.example/video.mp4"},"usage":{"completion_tokens":42}}`), nil
			})
			c, recorder := grokModesContext(context.Background())
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "public", "A ball", "video", "", "", AccountTestOptions{VideoEndpoint: "seedance", Duration: videoAccountTestInt(5), Resolution: resolution, Ratio: "16:9"})
			if invalid {
				require.Error(t, err)
				require.Empty(t, transport.requests)
				return
			}
			require.NoError(t, err)
			require.Len(t, transport.requests, 2)
			require.Contains(t, recorder.Body.String(), `"success":true`)
		})
	}
}
