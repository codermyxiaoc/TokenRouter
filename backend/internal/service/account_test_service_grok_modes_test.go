//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// grokModesLocalUpstream 始终由 httptest.Client 访问本地模拟上游，不使用真实账号或外网。
type grokModesLocalUpstream struct {
	client *http.Client
	calls  atomic.Int32
}

func (u *grokModesLocalUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls.Add(1)
	return u.client.Do(req)
}

func (u *grokModesLocalUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func grokModesService(server *httptest.Server) (*AccountTestService, *Account, *grokModesLocalUpstream) {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	account := &Account{ID: 1501, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{"api_key": "local-test-only", "base_url": server.URL + "/v1"}}
	upstream := &grokModesLocalUpstream{client: server.Client()}
	return &AccountTestService{cfg: cfg, httpUpstream: upstream}, account, upstream
}

func grokModesContext(ctx context.Context) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/accounts/1501/test", nil).WithContext(ctx)
	return c, recorder
}

func grokModesImageDataURL(t *testing.T, size int) string {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewRGBA(image.Rect(0, 0, size, size))))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestGrokModesLocalEndpointsAndOutputs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name, mode, path, response, contentType, event string
		options                                        AccountTestOptions
	}{
		{"text", "text", "/v1/responses", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n", "text/event-stream", "content", AccountTestOptions{}},
		{"image", "image", "/v1/images/generations", `{"data":[{"b64_json":"aGVsbG8=","mime_type":"image/png"}]}`, "application/json", "image", AccountTestOptions{}},
		{"image_edit", "image", "/v1/images/edits", `{"data":[{"url":"https://cdn.example/image.png"}]}`, "application/json", "image", AccountTestOptions{ImageDataURL: grokModesImageDataURL(t, 8)}},
		{"search", "search", "/v1/responses", `{"output":[{"type":"web_search_call","id":"search-1","action":{"sources":[{"url":"https://example.org"}]}},{"type":"message","content":[{"type":"output_text","text":"found"}]}]}`, "application/json", "content", AccountTestOptions{}},
		{"tts", "tts", "/v1/tts", "ID3sample-audio", "audio/mpeg", "audio", AccountTestOptions{}},
		{"stt_silent", "stt", "/v1/stt", `{"text":""}`, "application/json", "content", AccountTestOptions{}},
		{"stt_uploaded", "stt", "/v1/stt", `{"text":"hello"}`, "application/json", "content", AccountTestOptions{AudioDataURL: "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(minimalGrokTestWAV())}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, test.path, r.URL.Path)
				require.Equal(t, "Bearer local-test-only", r.Header.Get("Authorization"))
				require.Empty(t, r.Header.Get("X-Grok-Client-Version"))
				if test.mode == "stt" {
					require.NoError(t, r.ParseMultipartForm(8<<20))
					file, header, err := r.FormFile("file")
					require.NoError(t, err)
					defer file.Close()
					data, err := io.ReadAll(file)
					require.NoError(t, err)
					require.True(t, validGrokTestAudio(data, "audio/wav"))
					require.Contains(t, header.Filename, ".wav")
					require.Equal(t, "grok-stt", r.FormValue("model"))
				} else {
					data, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					if test.mode == "search" {
						require.Equal(t, "web_search", gjson.GetBytes(data, "tools.0.type").String())
						require.False(t, gjson.GetBytes(data, "stream").Bool())
					}
					if test.name == "image_edit" {
						require.Equal(t, test.options.ImageDataURL, gjson.GetBytes(data, "image.url").String())
					}
					if test.mode == "tts" {
						require.Equal(t, "en", gjson.GetBytes(data, "language").String())
						require.Equal(t, "hello", gjson.GetBytes(data, "text").String())
					}
				}
				w.Header().Set("Content-Type", test.contentType)
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			svc, account, upstream := grokModesService(server)
			c, recorder := grokModesContext(context.Background())
			require.NoError(t, svc.testGrokAccountConnectionWithOptions(c, account, "", "hello", test.mode, test.options))
			require.Equal(t, int32(1), upstream.calls.Load())
			require.Contains(t, recorder.Body.String(), `"type":"`+test.event+`"`)
			require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
			require.NotContains(t, recorder.Body.String(), `"type":"error"`)
		})
	}
}

func TestGrokModesVideoCreatesOnceAndPollsSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, contentFallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "signed_url", true: "content_fallback"}[contentFallback], func(t *testing.T) {
			var creates, polls, downloads atomic.Int32
			imageURL := grokModesImageDataURL(t, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer local-test-only", r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/videos/generations":
					creates.Add(1)
					require.Equal(t, http.MethodPost, r.Method)
					body, _ := io.ReadAll(r.Body)
					require.Equal(t, imageURL, gjson.GetBytes(body, "image.url").String())
					require.Equal(t, "grok-imagine-video", gjson.GetBytes(body, "model").String())
					w.WriteHeader(http.StatusAccepted)
					_, _ = io.WriteString(w, `{"data":{"task_id":"video-test-1"}}`)
				case "/v1/videos/video-test-1":
					polls.Add(1)
					if contentFallback {
						_, _ = io.WriteString(w, `{"status":"done"}`)
					} else {
						_, _ = io.WriteString(w, `{"status":"done","video":{"url":"https://cdn.example/video.mp4"}}`)
					}
				case "/v1/videos/video-test-1/content":
					downloads.Add(1)
					w.Header().Set("Content-Type", "video/mp4")
					_, _ = io.WriteString(w, "local-video")
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			svc, account, _ := grokModesService(server)
			c, recorder := grokModesContext(context.Background())
			require.NoError(t, svc.testGrokAccountConnectionWithOptions(c, account, "", "hello", "video", AccountTestOptions{ImageDataURL: imageURL}))
			require.Equal(t, int32(1), creates.Load())
			require.Equal(t, int32(1), polls.Load())
			require.Equal(t, map[bool]int32{false: 0, true: 1}[contentFallback], downloads.Load())
			require.Contains(t, recorder.Body.String(), `"type":"video"`)
			require.Contains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestGrokModesRejectFalseSuccessAndNeverRetryGeneration(t *testing.T) {
	cases := []struct {
		mode, body, contentType string
		status                  int
	}{
		{"image", `{"data":[]}`, "application/json", 200},
		{"image", `{"data":[{}]}`, "application/json", 200},
		{"image", `{"data":[{"url":"javascript:alert(1)"}]}`, "application/json", 200},
		{"image", `{"error":{"message":"denied"}}`, "application/json", 200},
		{"image", `{"error":"busy"}`, "application/json", 503},
		{"video", `{}`, "application/json", 202},
		{"search", `{"output":[{"type":"message"}]}`, "application/json", 200},
		{"search", `{"status":"failed","output":[{"type":"web_search_call","id":"failed-search"}]}`, "application/json", 200},
		{"search", `{"output":[{"type":"web_search_call","id":"failed-search","status":"failed"}]}`, "application/json", 200},
		{"search", `<html>login</html>`, "text/html", 200},
		{"tts", `<html>login</html>`, "text/html", 200},
		{"tts", ``, "audio/mpeg", 200},
		{"tts", `{"error":"busy"}`, "application/json", 429},
		{"stt", `<html>login</html>`, "text/html", 200},
		{"stt", `{}`, "application/json", 200},
		{"stt", `{"text":null}`, "application/json", 200},
		{"stt", `{"text":42}`, "application/json", 200},
	}
	for index, test := range cases {
		t.Run(test.mode+"_"+string(rune('a'+index)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			svc, account, upstream := grokModesService(server)
			c, recorder := grokModesContext(context.Background())
			require.Error(t, svc.testGrokAccountConnectionWithOptions(c, account, "", "", test.mode, AccountTestOptions{}))
			require.Equal(t, int32(1), upstream.calls.Load())
			require.Contains(t, recorder.Body.String(), `"type":"error"`)
			require.NotContains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestGrokModesRejectUnsafeInputsBeforeUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid media reached upstream") }))
	defer server.Close()
	cases := []struct {
		mode string
		opts AccountTestOptions
	}{
		{"image", AccountTestOptions{ImageDataURL: "https://private.example/secret"}},
		{"image", AccountTestOptions{ImageDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("<script>alert(1)</script>"))}},
		{"video", AccountTestOptions{ImageDataURL: grokModesImageDataURL(t, 1)}},
		{"stt", AccountTestOptions{AudioDataURL: "https://private.example/secret"}},
		{"stt", AccountTestOptions{AudioDataURL: "data:audio/wav;base64," + base64.StdEncoding.EncodeToString([]byte("<?php echo 1;?>"))}},
		{"stt", AccountTestOptions{AudioDataURL: "data:audio/wav;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(maxGrokTestMediaBytes+1))}},
		{"invalid", AccountTestOptions{}},
	}
	for _, test := range cases {
		svc, account, upstream := grokModesService(server)
		c, recorder := grokModesContext(context.Background())
		require.Error(t, svc.testGrokAccountConnectionWithOptions(c, account, "", "", test.mode, test.opts))
		require.Zero(t, upstream.calls.Load())
		require.NotContains(t, recorder.Body.String(), `"success":true`)
	}
}

func TestGrokModesVideoCancellationDoesNotResubmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = io.WriteString(w, `{"request_id":"video-pending"}`)
		} else {
			_, _ = io.WriteString(w, `{"status":"pending"}`)
			cancel()
		}
	}))
	defer server.Close()
	svc, account, upstream := grokModesService(server)
	c, recorder := grokModesContext(ctx)
	started := time.Now()
	require.Error(t, svc.testGrokAccountConnectionWithOptions(c, account, "", "", "video", AccountTestOptions{}))
	require.Less(t, time.Since(started), 2*time.Second)
	require.Equal(t, int32(2), upstream.calls.Load())
	require.NotContains(t, recorder.Body.String(), `"success":true`)
}

func TestGrokModesRealtimeLocalHandshake(t *testing.T) {
	for _, errorEvent := range []bool{false, true} {
		t.Run(map[bool]string{false: "session", true: "error"}[errorEvent], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/realtime", r.URL.Path)
				require.Equal(t, "grok-voice-latest", r.URL.Query().Get("model"))
				require.Equal(t, "Bearer local-test-only", r.Header.Get("Authorization"))
				conn, err := coderws.Accept(w, r, nil)
				require.NoError(t, err)
				defer conn.CloseNow()
				event := map[string]any{"type": "session.created"}
				if errorEvent {
					event = map[string]any{"type": "error", "error": map[string]any{"message": "permission denied"}}
				}
				encoded, _ := json.Marshal(event)
				require.NoError(t, conn.Write(r.Context(), coderws.MessageText, encoded))
				// 客户端只能关闭连接，不能发送音频或请求生成。
				_, _, _ = conn.Read(r.Context())
			}))
			defer server.Close()
			svc, account, upstream := grokModesService(server)
			c, recorder := grokModesContext(context.Background())
			err := svc.testGrokAccountConnectionWithOptions(c, account, "", "", "realtime", AccountTestOptions{})
			if errorEvent {
				require.Error(t, err)
				require.NotContains(t, recorder.Body.String(), `"success":true`)
			} else {
				require.NoError(t, err)
				require.Contains(t, recorder.Body.String(), "session.created")
				require.Contains(t, recorder.Body.String(), `"success":true`)
			}
			require.Zero(t, upstream.calls.Load())
		})
	}
}

func TestGrokModesResponseSizeLimitIsErrorNotTruncatedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, strings.Repeat("a", maxGrokTestMediaBytes+1))
	}))
	defer server.Close()
	svc, account, upstream := grokModesService(server)
	c, recorder := grokModesContext(context.Background())
	require.Error(t, svc.testGrokAccountConnectionWithOptions(c, account, "", "", "tts", AccountTestOptions{}))
	require.Equal(t, int32(1), upstream.calls.Load())
	require.Contains(t, recorder.Body.String(), "preview limit")
	require.NotContains(t, recorder.Body.String(), `"type":"audio"`)
}

func TestGrokModesRealtimeImmediateCloseIsNotSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		_ = conn.Close(coderws.StatusPolicyViolation, "not entitled")
	}))
	defer server.Close()
	svc, account, _ := grokModesService(server)
	c, recorder := grokModesContext(context.Background())
	require.Error(t, svc.testGrokAccountConnectionWithOptions(c, account, "", "", "realtime", AccountTestOptions{}))
	require.Contains(t, recorder.Body.String(), "closed before a server event")
	require.NotContains(t, recorder.Body.String(), `"success":true`)
}

func TestGrokModesRealtimeNoInitialEventOnlyProvesHandshake(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer conn.CloseNow()
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()
	svc, account, _ := grokModesService(server)
	c, recorder := grokModesContext(context.Background())
	require.NoError(t, svc.testGrokAccountConnectionWithOptions(c, account, "", "", "realtime", AccountTestOptions{}))
	require.Contains(t, recorder.Body.String(), "仅确认连接可达")
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

func TestGrokModesRespectsURLAllowlistBeforeSendingCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted host reached") }))
	defer server.Close()
	for _, mode := range []string{"image", "video", "search", "tts", "stt", "realtime"} {
		svc, account, upstream := grokModesService(server)
		svc.cfg.Security.URLAllowlist.Enabled = true
		svc.cfg.Security.URLAllowlist.UpstreamHosts = []string{"api.x.ai"}
		c, recorder := grokModesContext(context.Background())
		require.Error(t, svc.testGrokAccountConnectionWithOptions(c, account, "", "", mode, AccountTestOptions{}))
		require.Zero(t, upstream.calls.Load())
		require.NotContains(t, recorder.Body.String(), "local-test-only")
	}
}
