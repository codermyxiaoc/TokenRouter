package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestService_OpenAIImageOAuthHandlesOutputItemDoneFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_123\",\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"revised_prompt\":\"draw a cat\",\"output_format\":\"png\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000006,\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc := &AccountTestService{httpUpstream: upstream}
	account := &Account{
		ID:       53,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	err := svc.testOpenAIImageOAuth(c, withOpenAIImagesForceResponses(context.Background()), account, "gpt-image-2", "draw a cat")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Contains(t, rec.Body.String(), "Calling Codex /responses image tool")
	require.Contains(t, rec.Body.String(), "data:image/png;base64,aGVsbG8=")
	require.Contains(t, rec.Body.String(), "\"success\":true")
}

func TestAccountTestService_OpenAIImageOAuthForwardsTLSProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_456\",\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"output_format\":\"png\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc := &AccountTestService{
		httpUpstream:        upstream,
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
	account := &Account{
		ID:       55,
		Name:     "openai-oauth-tls",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
		Extra: map[string]any{
			"enable_tls_fingerprint": true,
		},
	}

	err := svc.testOpenAIImageOAuth(c, withOpenAIImagesForceResponses(context.Background()), account, "gpt-image-2", "draw a cat")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastTLSProfile)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
}

func TestAccountTestService_OpenAIImageAPIKeyUsesConfiguredV1BaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aGVsbG8=","revised_prompt":"draw a cat"}]}`)),
		},
	}
	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{},
	}
	account := &Account{
		ID:       54,
		Name:     "openai-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1",
		},
	}

	err := svc.testOpenAIImageAPIKey(c, context.Background(), account, "gpt-image-2", "draw a cat")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, "https://image-upstream.example/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer test-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, rec.Body.String(), "data:image/png;base64,aGVsbG8=")
	require.Contains(t, rec.Body.String(), "\"success\":true")
}

func TestAccountTestService_OpenAIExplicitImageTypeDoesNotInspectModelName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aGVsbG8="}]}`)),
		},
	}
	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{},
	}
	account := &Account{
		ID:       56,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1",
		},
	}

	err := svc.testOpenAIAccountConnection(c, account, "custom-model-alias", "draw a lighthouse", AccountTestModeDefault, AccountTestTypeImage)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://image-upstream.example/v1/images/generations", upstream.lastReq.URL.String())
	body, err := io.ReadAll(upstream.lastReq.Body)
	require.NoError(t, err)
	require.Equal(t, "custom-model-alias", gjson.GetBytes(body, "model").String())
	require.Equal(t, "draw a lighthouse", gjson.GetBytes(body, "prompt").String())
}

func TestAccountTestService_OpenAIExplicitTextTypeDoesNotInspectModelName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n")),
		},
	}
	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{},
	}
	account := &Account{
		ID:       57,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://text-upstream.example/v1",
		},
	}

	err := svc.testOpenAIAccountConnection(c, account, "gpt-image-2", "reply briefly", AccountTestModeDefault, AccountTestTypeText)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://text-upstream.example/v1/responses", upstream.lastReq.URL.String())
	body, err := io.ReadAll(upstream.lastReq.Body)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", gjson.GetBytes(body, "model").String())
	require.Equal(t, "reply briefly", gjson.GetBytes(body, "input.0.content.0.text").String())
}

// runOpenAIImageAPIKeyAccountTest 以给定模型和上游响应执行 APIKey 生图测试，返回上游请求体与 SSE 输出。
func runOpenAIImageAPIKeyAccountTest(t *testing.T, model, upstreamBody string) (map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		},
	}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{
		ID: 54, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-api-key"},
	}

	err := svc.testOpenAIImageAPIKey(c, context.Background(), account, model, "draw a cat")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://api.openai.com/v1/images/generations", upstream.lastReq.URL.String())
	var payload map[string]any
	require.NoError(t, json.Unmarshal(upstream.lastBody, &payload))
	require.Equal(t, model, payload["model"])
	require.Equal(t, "draw a cat", payload["prompt"])
	require.Equal(t, float64(1), payload["n"])
	return payload, rec.Body.String()
}

// TestAccountTestService_OpenAIImageAPIKeyOmitsUnsupportedResponseFormat 验证 GPT Image 模型测试不发送 response_format。
func TestAccountTestService_OpenAIImageAPIKeyOmitsUnsupportedResponseFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{
		"gpt-image-1",
		"gpt-image-1-mini",
		"gpt-image-1.5",
		"gpt-image-2",
		"gpt-image-2-2026-04-21",
		"gpt-image-2.5-flare",
	} {
		t.Run(model, func(t *testing.T) {
			payload, body := runOpenAIImageAPIKeyAccountTest(t, model, `{"data":[{"b64_json":"aGVsbG8="}]}`)
			// GPT Image 模型始终返回 base64，并拒绝该旧参数。
			require.NotContains(t, payload, "response_format")
			require.Contains(t, body, "data:image/png;base64,aGVsbG8=")
			require.Contains(t, body, `"success":true`)
		})
	}
}

// TestAccountTestService_OpenAIImageAPIKeyDallEKeepsResponseFormat 验证 dall-e 模型仍显式请求 b64_json。
func TestAccountTestService_OpenAIImageAPIKeyDallEKeepsResponseFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload, body := runOpenAIImageAPIKeyAccountTest(t, "dall-e-3", `{"data":[{"b64_json":"aGVsbG8="}]}`)
	require.Equal(t, "b64_json", payload["response_format"])
	require.Contains(t, body, "data:image/png;base64,aGVsbG8=")
}

// TestAccountTestService_OpenAIImageAPIKeyAcceptsURLResponse 验证上游返回 url 形态时测试仍展示图片并判定成功。
func TestAccountTestService_OpenAIImageAPIKeyAcceptsURLResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, body := runOpenAIImageAPIKeyAccountTest(t, "gpt-image-2", `{"data":[{"url":"https://cdn.example/cat.png"}]}`)
	require.Contains(t, body, "https://cdn.example/cat.png")
	require.Contains(t, body, `"success":true`)
}
