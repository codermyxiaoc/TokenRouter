//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 本地模拟上游只接收预期的版本化路径，确保真实 HTTP 请求而非仅字符串断言。
func TestGrokVoiceRootURLAcrossAccountTestGatewayAndCanvas(t *testing.T) {
	for _, prefix := range []string{"", "/v1", "/relay/xai/v1", "/custom"} {
		t.Run("base"+strings.ReplaceAll(prefix, "/", "_"), func(t *testing.T) {
			path := prefix
			if path == "" {
				path = "/v1"
			}
			path += "/tts"
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				require.Equal(t, path, r.URL.Path)
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "Bearer local-test-only", r.Header.Get("Authorization"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.True(t, gjson.GetBytes(body, "text").Exists())
				require.False(t, gjson.GetBytes(body, "model").Exists(), "官方TTS参数没有model，不应偷偷改成另一协议")
				w.Header().Set("Content-Type", "audio/wav")
				_, _ = w.Write(minimalGrokTestWAV())
			}))
			defer server.Close()
			accountTest, account, upstream := grokModesService(server)
			account.Credentials["base_url"] = server.URL + prefix
			c, recorder := grokModesContext(context.Background())
			require.NoError(t, accountTest.testGrokTTS(c, context.Background(), account, "local-test-only", "hello"))
			require.Contains(t, recorder.Body.String(), `"success":true`)
			require.Equal(t, int32(1), requests.Load())
			gateway := &OpenAIGatewayService{cfg: accountTest.cfg, httpUpstream: upstream}
			c, recorder = grokModesContext(context.Background())
			result, err := gateway.ForwardGrokVoice(context.Background(), c, account, "tts", []byte(`{"text":"hello","voice_id":"eve","language":"auto","output_format":{"codec":"wav"}}`), "application/json")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, minimalGrokTestWAV(), recorder.Body.Bytes())
			require.Equal(t, "tts", result.AudioUsage.Mode)
			require.InDelta(t, 5.0/1e6, result.AudioUsage.DurationOrUnits, 1e-12)
			require.Equal(t, int32(2), requests.Load())
			run, payload, _ := creativeAudioTestExecution(t)
			executor := &CreativeExecutor{cfg: accountTest.cfg, gateway: gateway}
			output, err := executor.Execute(context.Background(), run, payload, &CreativeExecution{Account: account, UpstreamModel: CreativeGrokTTSModel})
			require.NoError(t, err)
			require.Len(t, output.Outputs, 1)
			require.Equal(t, "audio/wav", output.Outputs[0].Mime)
			require.Equal(t, int32(3), requests.Load(), "每个入口只请求既定地址一次")
		})
	}
}

func TestGrokVoiceRoot404NeverFallsBackToAnotherGenerationEndpoint(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/v1/tts", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	accountTest, account, upstream := grokModesService(server)
	account.Credentials["base_url"] = server.URL
	run, payload, _ := creativeAudioTestExecution(t)
	executor := &CreativeExecutor{cfg: accountTest.cfg, gateway: &OpenAIGatewayService{cfg: accountTest.cfg, httpUpstream: upstream}}
	_, err := executor.Execute(context.Background(), run, payload, &CreativeExecution{Account: account, UpstreamModel: CreativeGrokTTSModel})
	require.Error(t, err)
	require.False(t, IsRetryableCreativeError(err))
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "语音服务端点不存在，请检查账号服务地址", creativeCanvasExecutionFailureMessage("audio", "UPSTREAM_STATUS_404"))
	require.NotEqual(t, creativeCanvasExecutionFailureMessage("audio", "UPSTREAM_STATUS_404"), creativeCanvasExecutionFailureMessage("image", "UPSTREAM_STATUS_404"))
}

func TestGrokVoiceURLKeepsOperatorSecurityForRootBase(t *testing.T) {
	account := &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "http://relay.example:8317"}}
	cfg := &config.Config{}
	_, err := buildGrokVoiceURL(account, cfg, "tts")
	require.Error(t, err)
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	got, err := buildGrokVoiceURL(account, cfg, "tts")
	require.NoError(t, err)
	require.Equal(t, "http://relay.example:8317/v1/tts", got)
	for _, base := range []string{"http://relay.example:8317?token=secret", "http://user:secret@relay.example:8317", "http://relay.example:8317#secret"} {
		account.Credentials["base_url"] = base
		_, err = buildGrokVoiceURL(account, cfg, "tts")
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}
