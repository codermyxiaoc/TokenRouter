//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 存储替身只接收已校验字节，不访问真实对象存储或供应商。
type videoAccountTestReferenceStorage func(context.Context, string, string, []byte) (string, error)

func (f videoAccountTestReferenceStorage) Save(ctx context.Context, key, mime string, data []byte) (string, error) {
	return f(ctx, key, mime, data)
}

func TestVideoAccountTestMiniMaxHTTPSUploadPreservesOneCreationAndUniqueObjects(t *testing.T) {
	account := videoFixtureAccount(VideoEndpointMiniMax)
	image := grokModesImageDataURL(t, 8)
	decoded, _, err := decodeGrokTestDataURL(image, "image/", maxGrokTestImageBytes)
	require.NoError(t, err)
	storedURL := "https://cdn.example/admin-first-frame.png?signature=private-signature"
	keys := []string{}
	storage := videoAccountTestReferenceStorage(func(ctx context.Context, key, mime string, data []byte) (string, error) {
		require.NoError(t, ctx.Err())
		require.Equal(t, "image/png", mime)
		require.Equal(t, decoded, data)
		require.True(t, strings.HasPrefix(key, "images/account-test-reference-"))
		keys = append(keys, key)
		return storedURL, nil
	})
	uploader := NewImageResultUploader(storage, "images/", 0, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("Data URL conversion must not download any remote image")
		return nil, nil
	})})
	for i := 0; i < 2; i++ {
		svc, upstream := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
			if r.Method == http.MethodPost {
				require.Len(t, keys, i+1)
				require.Equal(t, storedURL, gjson.GetBytes(body, "content.1.image_url.url").String())
				require.Equal(t, "first_frame", gjson.GetBytes(body, "content.1.role").String())
				require.Equal(t, "2K", gjson.GetBytes(body, "resolution").String())
				require.Equal(t, int64(5), gjson.GetBytes(body, "duration").Int())
				require.NotContains(t, string(body), "data:image/")
				require.False(t, gjson.GetBytes(body, "reference_image_transport").Exists())
				metadata, parseErr := ParseVideoEndpointRequestMetadata(body, "", VideoEndpointMiniMax)
				require.NoError(t, parseErr)
				require.NotNil(t, metadata.ReferenceImageCount)
				require.Equal(t, 1, *metadata.ReferenceImageCount)
				return videoAccountTestJSON(200, `{"TaskId":"url-task"}`), nil
			}
			require.Equal(t, http.MethodGet, r.Method)
			return videoAccountTestJSON(200, `{"task":{"id":"url-task","status":"completed","content":{"url":"https://cdn.example/result.mp4"}}}`), nil
		})
		resolveCalls := 0
		svc.SetImageStorageResolver(func() (*ImageResultUploader, bool) {
			resolveCalls++
			return uploader, true
		})
		c, recorder := grokModesContext(context.Background())
		err := svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3-open", "test", "video", "", "", AccountTestOptions{
			VideoEndpoint: "minimax", Resolution: "2K", ImageDataURL: image, ReferenceImageTransport: "https_url",
		})
		require.NoError(t, err)
		require.Equal(t, 1, resolveCalls)
		require.Len(t, upstream.requests, 2)
		require.Contains(t, recorder.Body.String(), `"success":true`)
		require.NotContains(t, recorder.Body.String(), "private-signature")
		require.NotContains(t, recorder.Body.String(), "admin-first-frame")
	}
	require.Len(t, keys, 2)
	require.NotEqual(t, keys[0], keys[1], "independent tests must not overwrite the same reference image")
}

// 未选择转换、仅手动 URL 或没有上传图时，不能读取存储配置或改变原来的参数。
func TestVideoAccountTestMiniMaxReferenceTransportDoesNotChangeDefaultOrNoImage(t *testing.T) {
	for _, tc := range []struct {
		name, transport  string
		image, manualURL bool
	}{
		{name: "default-upload", image: true},
		{name: "explicit-data-url", transport: "data_url", image: true},
		{name: "no-image"},
		{name: "https-without-image", transport: "https_url"},
		{name: "data-without-image", transport: "data_url"},
		{name: "manual-https", manualURL: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointMiniMax)
			options := AccountTestOptions{VideoEndpoint: "minimax", ReferenceImageTransport: tc.transport}
			expected := ""
			if tc.image {
				expected = grokModesImageDataURL(t, 8)
				options.ImageDataURL = expected
			}
			if tc.manualURL {
				expected = "https://cdn.example/manual.png"
				options.ReferenceImageURL = expected
			}
			svc, upstream := videoAccountTestService(account, func(r *http.Request, body []byte) (*http.Response, error) {
				if r.Method == http.MethodPost {
					require.Equal(t, expected, gjson.GetBytes(body, "content.1.image_url.url").String())
					return videoAccountTestJSON(200, `{"TaskId":"unchanged-task"}`), nil
				}
				return videoAccountTestJSON(200, `{"task":{"id":"unchanged-task","status":"completed","content":{"url":"https://cdn.example/result.mp4"}}}`), nil
			})
			svc.SetImageStorageResolver(func() (*ImageResultUploader, bool) {
				t.Fatal("unselected or unused conversion must not access object storage")
				return nil, false
			})
			c, _ := grokModesContext(context.Background())
			require.NoError(t, svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3-open", "", "video", "", "", options))
			require.Len(t, upstream.requests, 2)
		})
	}
}

// 存储故障及不安全地址必须在生成前停止；错误文本不得暴露存储凭据或素材地址。
func TestVideoAccountTestMiniMaxHTTPSUploadFailureNeverCreatesVideo(t *testing.T) {
	for _, tc := range []struct {
		name, url                                                         string
		missing, disabled, nilUploader, nilStorage, canceled, uploadError bool
	}{
		{name: "missing-resolver", missing: true},
		{name: "disabled", disabled: true},
		{name: "nil-uploader", nilUploader: true},
		{name: "nil-storage", nilStorage: true},
		{name: "upload-failure", uploadError: true},
		{name: "canceled", canceled: true},
		{name: "http", url: "http://cdn.example/private-signature"},
		{name: "private-ip", url: "https://127.0.0.1/private-signature"},
		{name: "localhost", url: "https://localhost/private-signature"},
		{name: "credentials", url: "https://private-secret@cdn.example/private-signature"},
		{name: "fragment", url: "https://cdn.example/private-signature#fragment"},
		{name: "empty-url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpointMiniMax)
			svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
				t.Fatal("failed reference preparation must not create a video")
				return nil, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			saveCalls := 0
			storage := videoAccountTestReferenceStorage(func(context.Context, string, string, []byte) (string, error) {
				saveCalls++
				if tc.uploadError {
					return "", errors.New("private-secret https://bucket.example/private-signature")
				}
				return tc.url, nil
			})
			if !tc.missing {
				svc.SetImageStorageResolver(func() (*ImageResultUploader, bool) {
					if tc.nilUploader {
						return nil, true
					}
					if tc.nilStorage {
						return NewImageResultUploader(nil, "images/", 0, nil), true
					}
					return NewImageResultUploader(storage, "images/", 0, nil), !tc.disabled
				})
			}
			c, recorder := grokModesContext(ctx)
			err := svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3-open", "", "video", "", "", AccountTestOptions{
				VideoEndpoint: "minimax", ImageDataURL: grokModesImageDataURL(t, 8), ReferenceImageTransport: "https_url",
			})
			require.Error(t, err)
			require.Empty(t, upstream.requests)
			expectedSaves := 1
			if tc.missing || tc.disabled || tc.nilUploader || tc.nilStorage || tc.canceled {
				expectedSaves = 0
			}
			require.Equal(t, expectedSaves, saveCalls)
			require.NotContains(t, recorder.Body.String(), "private-secret")
			require.NotContains(t, recorder.Body.String(), "private-signature")
			require.NotContains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestVideoAccountTestMiniMaxReferenceTransportValidatesBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name, platform, endpoint string
		options                  AccountTestOptions
	}{
		{name: "unknown-transport", endpoint: "minimax", options: AccountTestOptions{ReferenceImageTransport: "auto"}},
		{name: "case-sensitive-transport", endpoint: "minimax", options: AccountTestOptions{ReferenceImageTransport: "HTTPS_URL"}},
		{name: "manual-url-and-https", endpoint: "minimax", options: AccountTestOptions{ReferenceImageTransport: "https_url", ReferenceImageURL: "https://cdn.example/input.png"}},
		{name: "manual-url-and-data", endpoint: "minimax", options: AccountTestOptions{ReferenceImageTransport: "data_url", ReferenceImageURL: "https://cdn.example/input.png"}},
		{name: "invalid-image", endpoint: "minimax", options: AccountTestOptions{ReferenceImageTransport: "https_url", ImageDataURL: "data:image/png;base64,AA=="}},
		{name: "invalid-duration", endpoint: "minimax", options: AccountTestOptions{ReferenceImageTransport: "https_url", Duration: new(0)}},
		{name: "other-video-protocol", endpoint: "compat", options: AccountTestOptions{ReferenceImageTransport: "data_url"}},
		{name: "openai-account", platform: PlatformOpenAI, options: AccountTestOptions{ReferenceImageTransport: "https_url"}},
		{name: "grok-account", platform: PlatformGrok, options: AccountTestOptions{ReferenceImageTransport: "https_url"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := videoFixtureAccount(VideoEndpoint(firstNonEmpty(tc.endpoint, "compat")))
			if tc.platform != "" {
				account.Platform = tc.platform
			}
			tc.options.VideoEndpoint = tc.endpoint
			svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
				t.Fatal("invalid options must not create a video")
				return nil, nil
			})
			svc.SetImageStorageResolver(func() (*ImageResultUploader, bool) {
				t.Fatal("invalid options must not access object storage")
				return nil, false
			})
			c, _ := grokModesContext(context.Background())
			require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "m", "", "video", "", "", tc.options))
			require.Empty(t, upstream.requests)
		})
	}
}

// 真实拒绝信息只映射为固定提示；HTTP 200 业务失败同样不重提、不显示任意错误正文。
func TestVideoAccountTestMiniMaxBase64RejectionHasSafeActionableReason(t *testing.T) {
	const upstreamMessage = "refusing to download from disallowed scheme 'data': base64数据 (2003)"
	const expected = "上游不接受 Base64 参考图，请选择 HTTPS 图片链接或上传转 HTTPS 方式"
	for _, status := range []int{200, 400} {
		for _, path := range []string{"error", "task.error"} {
			for _, extra := range []string{"", " private-secret"} {
				root := map[string]any{"task": map[string]any{"id": "accepted-task", "status": "failed"}, "debug": "private-secret"}
				errorValue := map[string]any{"message": upstreamMessage + extra, "http_code": 400}
				if path == "error" {
					root["error"] = errorValue
				} else {
					root["task"].(map[string]any)["error"] = errorValue
				}
				body, err := json.Marshal(root)
				require.NoError(t, err)
				account := videoFixtureAccount(VideoEndpointMiniMax)
				svc, upstream := videoAccountTestService(account, func(*http.Request, []byte) (*http.Response, error) {
					return videoAccountTestJSON(status, string(body)), nil
				})
				c, recorder := grokModesContext(context.Background())
				require.Error(t, svc.TestAccountConnectionWithOptions(c, account.ID, "MiniMax-H3-open", "", "video", "", "", AccountTestOptions{VideoEndpoint: "minimax"}))
				require.Len(t, upstream.requests, 1)
				require.Equal(t, extra == "", strings.Contains(recorder.Body.String(), expected))
				require.NotContains(t, recorder.Body.String(), "private-secret")
				require.NotContains(t, recorder.Body.String(), upstreamMessage)
				require.NotContains(t, recorder.Body.String(), `"success":true`)
				response := &VideoUpstreamResponse{StatusCode: status, Body: body, Status: "failed", TaskID: "accepted-task"}
				_ = videoAccountTestResponseError(VideoEndpointMiniMax, response)
				require.Equal(t, "failed", response.Status)
				require.Equal(t, "accepted-task", response.TaskID)
				if otherErr := videoAccountTestResponseError(VideoEndpointCompat, response); otherErr != nil {
					require.NotContains(t, otherErr.Error(), expected)
				}
			}
		}
	}
}
