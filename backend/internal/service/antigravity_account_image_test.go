//go:build unit

package service

import (
	"context"
	"encoding/json"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 管理员图片测试覆盖别名、正常大图、正文限额和明确的文字模型拒绝，不访问真实上游。
func TestAntigravityAccountImageTestMappedModelAndBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		limit int64
		fail  bool
	}{{"image_larger_than_error_summary", 2 << 20, false}, {"response_limit", 100, true}} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Repeat("A", 128<<10)
			encoded, _ := json.Marshal(map[string]any{"response": map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": data}}}}}}}})
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + string(encoded) + "\n\n"))}}}
			svc := &AntigravityGatewayService{tokenProvider: &AntigravityTokenProvider{}, httpUpstream: upstream, settingService: &SettingService{cfg: &config.Config{Gateway: config.GatewayConfig{UpstreamResponseReadMaxBytes: tc.limit}}}}
			account := &Account{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-only", "project_id": "test-project", "model_mapping": map[string]any{"art": "gemini-3.1-flash-image"}}}
			result, err := svc.TestConnection(context.Background(), account, "art", "draw a blue circle")
			if tc.fail {
				require.ErrorContains(t, err, "response body too large")
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Images, 1)
			require.Equal(t, "data:image/png;base64,"+data, result.Images[0].ImageURL)
			require.Equal(t, "gemini-3.1-flash-image", result.MappedModel)
			require.Len(t, upstream.requestBodies, 1)
			require.Contains(t, gjson.GetBytes(upstream.requestBodies[0], "request.generationConfig.responseModalities").String(), "IMAGE")
			require.Contains(t, string(upstream.requestBodies[0]), "draw a blue circle")
		})
	}
}

func TestAntigravityAccountImageTestRejectsTextModel(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	svc := &AccountTestService{}
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"art": "claude-sonnet-4-6"}}}
	require.ErrorContains(t, svc.routeAntigravityTest(c, account, "art", "", AccountTestTypeImage), "image generation model")
}
