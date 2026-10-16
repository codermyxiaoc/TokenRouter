//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 帧数优先级只体现在计费事实，必须保留发送正文，且省略分辨率不能绕过帧数校验。
func TestVideoSeedanceFrameRequestContract(t *testing.T) {
	target := officialProtocolTarget(VideoEndpointSeedance, VideoProtocolAdapterSeedance)
	target.Model = "doubao-seedance-1-0-pro-250528"
	for _, frames := range []int{29, 57, 241, 289} {
		for _, duration := range []string{"", `,"duration":2`, `,"duration":-1`} {
			t.Run(fmt.Sprintf("%d/%s", frames, duration), func(t *testing.T) {
				body, metadata, err := PrepareVideoProtocolRequest([]byte(fmt.Sprintf(`{"model":"alias","prompt":"p","frames":%d%s}`, frames, duration)), "", target)
				require.NoError(t, err)
				require.InDelta(t, float64(frames)/24, metadata.DurationSeconds, 1e-12)
				require.Equal(t, int64(frames), gjson.GetBytes(body, "frames").Int())
				require.Equal(t, duration != "", gjson.GetBytes(body, "duration").Exists())
				require.False(t, gjson.GetBytes(body, "resolution").Exists())
			})
		}
	}
	for _, fields := range []string{
		`"frames":0`, `"frames":28`, `"frames":30`, `"frames":290`, `"frames":29.1`,
		`"frames":null`, `"frames":true`, `"frames":"289"`, `"frames":[]`, `"frames":{}`, `"frames":1e309`,
		`"Frames":289`, `"frames":29,"frames":289`, `"frames":29,"Frames":289`,
		`"frames":289,"framespersecond":30`, `"frames":289,"FramesPerSecond":24`,
	} {
		t.Run(fields, func(t *testing.T) {
			_, _, err := PrepareVideoProtocolRequest([]byte(`{"model":"alias","prompt":"p",`+fields+`}`), "", target)
			require.Error(t, err)
		})
	}
	for _, model := range []string{"doubao-seedance-2-0", "doubao-seedance-2-0-mini-260615", "doubao-seedance-2-0-fast-260128", "doubao-seedance-2-5-260628", "ep-alias", "private-model"} {
		t.Run(model, func(t *testing.T) {
			target.Model = model
			_, metadata, err := PrepareVideoProtocolRequest([]byte(`{"model":"alias","prompt":"p","frames":57}`), "", target)
			if model == "ep-alias" || model == "private-model" {
				require.NoError(t, err)
				require.Equal(t, 2.375, metadata.DurationSeconds)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// 官方帧数与时长互斥，缺少帧率或任何非法事实都不能沿用低时长结算。
func TestVideoSeedanceFrameResponseContract(t *testing.T) {
	target := officialProtocolTarget(VideoEndpointSeedance, VideoProtocolAdapterSeedance)
	for _, frames := range []int{29, 57, 241, 289} {
		response, err := ExtractVideoProtocolResponse(target, 200, []byte(fmt.Sprintf(`{"id":"task","status":"succeeded","frames":%d,"framespersecond":24}`, frames)), "task")
		require.NoError(t, err)
		require.Empty(t, response.MetadataError)
		require.InDelta(t, float64(frames)/24, response.Metadata.DurationSeconds, 1e-12)
	}
	for _, fields := range []string{
		`"frames":289`, `"frames":289,"framespersecond":null`, `"frames":289,"framespersecond":0`,
		`"frames":289,"framespersecond":30`, `"frames":289,"framespersecond":"24"`, `"frames":289,"framespersecond":true`,
		`"frames":289,"framespersecond":24.1`, `"frames":289,"framespersecond":1e309`,
		`"frames":289,"framespersecond":24,"duration":12`, `"frames":289,"framespersecond":24,"duration":2`,
		`"frames":null,"framespersecond":24`, `"frames":30,"framespersecond":24`, `"frames":290,"framespersecond":24`,
		`"Frames":289,"framespersecond":24`, `"frames":289,"FramesPerSecond":24`, `"frames":29,"frames":289,"framespersecond":24`,
	} {
		t.Run(fields, func(t *testing.T) {
			response, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"task","status":"succeeded",`+fields+`}`), "task")
			require.NoError(t, err)
			require.NotEmpty(t, response.MetadataError)
			require.Zero(t, response.Metadata.DurationSeconds)
		})
	}
	for _, status := range []string{"queued", "running", "failed", "cancelled"} {
		response, err := ExtractVideoProtocolResponse(target, 200, []byte(fmt.Sprintf(`{"id":"task","status":%q,"frames":0,"framespersecond":0}`, status)), "task")
		require.NoError(t, err)
		require.Empty(t, response.MetadataError, "未完成状态的零占位不应影响等待或退款")
	}
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterKemei} {
		target.ProtocolAdapter = adapter
		response, err := ExtractVideoProtocolResponse(target, 200, []byte(`{"id":"task","status":"succeeded","frames":289,"framespersecond":24,"duration":2}`), "task")
		require.NoError(t, err)
		require.Empty(t, response.MetadataError)
		require.Equal(t, float64(2), response.Metadata.DurationSeconds)
	}
}

// 非法尾串不得被忽略后作为成功扣费或失败退款依据，同任务完整重查后才可推进状态。
func TestVideoSeedanceRejectsTrailingResponseData(t *testing.T) {
	target := officialProtocolTarget(VideoEndpointSeedance, VideoProtocolAdapterSeedance)
	for _, status := range []string{"succeeded", "failed", "cancelled"} {
		for _, suffix := range []string{`{"frames":289}`, `broken`, `[]`} {
			t.Run(status+"/"+suffix, func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"id":"task","status":%q,"frames":57,"framespersecond":24,"content":{"video_url":"https://example.test/video.mp4"}}%s`, status, suffix))
				response, err := ExtractVideoProtocolResponse(target, 200, body, "task")
				require.NoError(t, err)
				require.Equal(t, "task", response.TaskID)
				require.Empty(t, response.Status)
				require.Empty(t, response.VideoURL)
				require.Zero(t, response.Metadata.DurationSeconds)
				require.NotEmpty(t, response.MetadataError)
			})
		}
	}
}

// 只对最终官方 2.5 型号的首尾帧模式限制比例，参考图模式、旧协议和未知接入点保持原行为。
func TestVideoSeedanceBoundaryImageRatioContract(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterSeedance, VideoProtocolAdapterLegacy, VideoProtocolAdapterKemei} {
		for _, model := range []string{"doubao-seedance-2-5-260628", "doubao-seedance-2-0-mini", "ep-private"} {
			for _, role := range []string{"first_frame", "last_frame", "reference_image"} {
				for _, ratio := range []string{"", "adaptive", "16:9"} {
					t.Run(adapter+"/"+model+"/"+role+"/"+ratio, func(t *testing.T) {
						target := officialProtocolTarget(VideoEndpointSeedance, adapter)
						target.Model = model
						fields := ""
						if ratio != "" {
							fields = fmt.Sprintf(`,"ratio":%q`, ratio)
						}
						raw := []byte(fmt.Sprintf(`{"model":"alias","duration":5,"content":[{"type":"text","text":"p"},{"type":"image_url","role":%q,"image_url":{"url":"https://example.test/image.png"}}]%s}`, role, fields))
						body, _, err := PrepareVideoProtocolRequest(raw, "", target)
						if adapter == VideoProtocolAdapterSeedance && model == "doubao-seedance-2-5-260628" && role != "reference_image" && ratio == "16:9" {
							require.Error(t, err)
						} else {
							require.NoError(t, err)
							require.Equal(t, ratio, gjson.GetBytes(body, "ratio").String())
						}
					})
				}
			}
		}
	}
}

// 从账号测试入口验证上传首帧的真实发送模板；非法比例在任何 POST 前拒绝，空值不补 16:9。
func TestVideoSeedance25AccountReferenceImageDefaults(t *testing.T) {
	for _, ratio := range []string{"", "adaptive", "16:9"} {
		t.Run(ratio, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointSeedance)
			account.Credentials["model_mapping"] = map[string]any{"public": "doubao-seedance-2-5-260628"}
			account.Credentials["video_endpoint_adapters"] = map[string]string{"seedance": VideoProtocolAdapterSeedance}
			invalid := ratio == "16:9"
			svc, transport := videoAccountTestService(account, func(request *http.Request, body []byte) (*http.Response, error) {
				require.False(t, invalid)
				if request.Method == http.MethodPost {
					require.Equal(t, ratio, gjson.GetBytes(body, "ratio").String())
					require.Equal(t, ratio != "", gjson.GetBytes(body, "ratio").Exists())
					require.Equal(t, "first_frame", gjson.GetBytes(body, "content.1.role").String())
					return videoAccountTestJSON(200, `{"id":"task","status":"queued"}`), nil
				}
				return videoAccountTestJSON(200, `{"id":"task","status":"succeeded","duration":5,"resolution":"720p","content":{"video_url":"https://example.test/video.mp4"}}`), nil
			})
			c, recorder := grokModesContext(context.Background())
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "public", "p", "video", "", "", AccountTestOptions{VideoEndpoint: "seedance", Ratio: ratio, ImageDataURL: grokModesImageDataURL(t, 8)})
			if invalid {
				require.Error(t, err)
				require.Empty(t, transport.requests)
			} else {
				require.NoError(t, err)
				require.Len(t, transport.requests, 2)
				require.Contains(t, recorder.Body.String(), `"success":true`)
			}
		})
	}
}
