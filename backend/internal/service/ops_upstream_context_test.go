package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 错误产生后换组、改协议和改模型，已经记录的失败尝试仍应保持原始归属。
func TestAppendOpsUpstreamErrorSnapshotsAttemptAttribution(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	group := &Group{ID: 3, Name: "test1", Platform: PlatformOpenAI}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
	SetActualOpenAIUpstreamEndpoint(c, "/v1/chat/completions")
	SetOpsUpstreamModel(c, "upstream-model-1")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: PlatformOpenAI, AccountID: 22, UpstreamStatusCode: 503, Message: "retry"})

	group.ID, group.Name = 5, "test2"
	SetActualOpenAIUpstreamEndpoint(c, "/backend-api/codex/responses")
	SetOpsUpstreamModel(c, "upstream-model-2")
	value, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events := value.([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.Equal(t, int64(3), events[0].GroupID)
	require.Equal(t, "test1", events[0].GroupName)
	require.Equal(t, "/v1/chat/completions", events[0].UpstreamEndpoint)
	require.Equal(t, "upstream-model-1", events[0].UpstreamModel)
}

// 非 OpenAI 协议优先复用网关真实端点，缺少上下文时仅从实际 URL 取不含凭据的路径。
func TestAppendOpsUpstreamErrorCapturesCrossPlatformEndpoint(t *testing.T) {
	for _, test := range []struct {
		name, actual, rawURL, want string
	}{
		{"gateway_context", "/v1internal:streamGenerateContent", "https://upstream.example/ignored", "/v1internal:streamGenerateContent"},
		{"url_fallback", "", "https://user:password@upstream.example/v1/messages?key=secret#fragment", "/v1/messages"},
		{"unknown", "", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(OpsActualUpstreamEndpointKey, test.actual)
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: PlatformAnthropic, UpstreamURL: test.rawURL, UpstreamStatusCode: 503})
			value, _ := c.Get(OpsUpstreamErrorsKey)
			require.Equal(t, test.want, value.([]*OpsUpstreamErrorEvent)[0].UpstreamEndpoint)
		})
	}
}

func TestSafeUpstreamURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips query", "https://api.anthropic.com/v1/messages?beta=true", "https://api.anthropic.com/v1/messages"},
		{"strips fragment", "https://api.openai.com/v1/responses#frag", "https://api.openai.com/v1/responses"},
		{"strips both", "https://host/path?token=secret#x", "https://host/path"},
		{"no query or fragment", "https://host/path", "https://host/path"},
		{"empty string", "", ""},
		{"whitespace only", "  ", ""},
		{"query before fragment", "https://h/p?a=1#f", "https://h/p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, safeUpstreamURL(tt.input))
		})
	}
}
