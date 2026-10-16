//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 按原生示例核对实际出站正文；上传、URL、无图与水印三态均不能改变单次创建语义。
func TestVideoAccountTestMiniMaxNativeRequestOptions(t *testing.T) {
	for _, imageKind := range []string{"none", "upload", "url"} {
		for _, resolution := range []string{"", "2K", "768P"} {
			for _, watermark := range []*bool{nil, new(true), new(false)} {
				watermarkName := "omitted"
				if watermark != nil {
					watermarkName = "false"
					if *watermark {
						watermarkName = "true"
					}
				}
				t.Run(imageKind+"/"+resolution+"/"+watermarkName, func(t *testing.T) {
					account := videoFixtureAccount(VideoEndpointMiniMax)
					before, err := json.Marshal(account)
					require.NoError(t, err)
					options := AccountTestOptions{VideoEndpoint: "minimax", Resolution: resolution, AIGCWatermark: watermark}
					imageURL := ""
					switch imageKind {
					case "upload":
						imageURL = grokModesImageDataURL(t, 8)
						options.ImageDataURL = imageURL
					case "url":
						imageURL = "https://cdn.example/first_frame.png?signature=fixture"
						options.ReferenceImageURL = " " + imageURL + " "
					}
					svc, upstream := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
						if r.Method == http.MethodPost {
							require.Equal(t, "/prefix/v2/video_generation", r.URL.Path)
							expected := map[string]any{"model": "MiniMax-H3", "duration": 5, "content": []map[string]any{{"type": "text", "text": "城市霓虹雨夜"}}}
							if imageURL != "" {
								expected["content"] = append(expected["content"].([]map[string]any), map[string]any{"type": "image_url", "image_url": map[string]string{"url": imageURL}, "role": "first_frame"})
							}
							if resolution != "" {
								expected["resolution"] = resolution
							}
							if watermark != nil {
								expected["aigc_watermark"] = *watermark
							}
							encoded, err := json.Marshal(expected)
							require.NoError(t, err)
							require.JSONEq(t, string(encoded), string(body))
							metadata, err := ParseVideoEndpointRequestMetadata(body, "", VideoEndpointMiniMax)
							require.NoError(t, err)
							require.Equal(t, float64(5), metadata.DurationSeconds)
							require.Equal(t, resolution, metadata.Resolution)
							require.NotNil(t, metadata.ReferenceImageCount)
							wantCount := 0
							if imageURL != "" {
								wantCount = 1
							}
							require.Equal(t, wantCount, *metadata.ReferenceImageCount)
							return videoAccountTestJSON(200, `{"TaskId":"native-task"}`), nil
						}
						require.Equal(t, http.MethodGet, r.Method)
						require.Equal(t, "/prefix/v2/query/video_generation/native-task", r.URL.Path)
						return videoAccountTestJSON(200, `{"task":{"id":"native-task","status":"completed","content":{"url":"https://cdn.example/video.mp4"}}}`), nil
					})
					c, recorder := grokModesContext(context.Background())
					require.NoError(t, svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3", "城市霓虹雨夜", "video", "", "", options))
					require.Len(t, upstream.requests, 2)
					require.Contains(t, recorder.Body.String(), `"success":true`)
					require.NotContains(t, recorder.Body.String(), "signature=fixture")
					after, err := json.Marshal(account)
					require.NoError(t, err)
					require.JSONEq(t, string(before), string(after))
				})
			}
		}
	}
}

// 错误素材或跨端点选项在提交前拒绝，避免静默丢弃导致与管理员所选参数不一致。
func TestVideoAccountTestMiniMaxRejectsInvalidOptionsBeforeSubmit(t *testing.T) {
	for _, raw := range []string{"http://cdn.example/a.png", "https://127.0.0.1/a.png", "https://localhost/a.png", "https://[::1]/a.png", "https://user:pass@cdn.example/a.png", "https://cdn.example/a.png#fragment", "data:image/png;base64,AA==", "https://cdn.example/" + strings.Repeat("x", 16384), "://invalid"} {
		t.Run(raw[:min(len(raw), 60)], func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointMiniMax)
			svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
				t.Fatal("invalid image URL must not reach upstream")
				return nil, nil
			})
			c, _ := grokModesContext(context.Background())
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", AccountTestOptions{VideoEndpoint: "minimax", ReferenceImageURL: raw}))
			require.Empty(t, upstream.requests)
		})
	}
	for _, platform := range []string{PlatformVideo, PlatformGrok, PlatformOpenAI} {
		for _, option := range []AccountTestOptions{{ReferenceImageURL: "https://cdn.example/a.png"}, {AIGCWatermark: new(false)}, {ReferenceImageURL: "https://cdn.example/a.png", ImageDataURL: "data:image/png;base64,AA=="}} {
			account := videoFixtureAccount(VideoEndpointCompat)
			account.Platform = platform
			if platform == PlatformVideo {
				option.VideoEndpoint = "compat"
				if option.ImageDataURL != "" {
					account = videoFixtureAccount(VideoEndpointMiniMax)
					option.VideoEndpoint = "minimax"
				}
			}
			svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
				t.Fatal("incompatible options must not reach upstream")
				return nil, nil
			})
			c, _ := grokModesContext(context.Background())
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", option))
			require.Empty(t, upstream.requests)
		}
	}
}

// 已确认的分辨率拒绝仅显示固定原因；任意正文及其它平台不能借该诊断泄露信息或重提。
func TestVideoAccountTestMiniMaxResolutionErrorIsSafeAndNeverRetries(t *testing.T) {
	for _, message := range []string{"当前分辨率 768p 不支持", "当前分辨率 768p 不支持\nsecret-override", "secret-override"} {
		account := videoFixtureAccount(VideoEndpointMiniMax)
		body, err := json.Marshal(map[string]any{"error": map[string]string{"message": message}, "debug": "fixture-secret"})
		require.NoError(t, err)
		svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
			return videoAccountTestJSON(400, string(body)), nil
		})
		c, recorder := grokModesContext(context.Background())
		require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", AccountTestOptions{VideoEndpoint: "minimax"}))
		require.Len(t, upstream.requests, 1)
		require.Contains(t, recorder.Body.String(), "HTTP 400")
		require.Equal(t, message == "当前分辨率 768p 不支持", strings.Contains(recorder.Body.String(), "上游不支持所填分辨率"))
		require.NotContains(t, recorder.Body.String(), "secret")
		require.NotContains(t, recorder.Body.String(), `"success":true`)
		result := &VideoUpstreamResponse{StatusCode: 400, Body: body}
		require.EqualError(t, videoAccountTestResponseError(VideoEndpointCompat, result), "Video upstream returned HTTP 400")
	}
}
