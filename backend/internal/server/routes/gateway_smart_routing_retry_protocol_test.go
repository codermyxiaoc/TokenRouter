package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	servermiddleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestSmartRoutingRetryProtocolOnCopiedRoute 验证第二候选使用自己的协议权限，
// 并覆盖 Gin Copy 不保留完整路由模板时的真实 URL 与通配参数。
func TestSmartRoutingRetryProtocolOnCopiedRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registered := make(map[string]bool)
	for _, route := range newGatewayRoutesTestRouter().Routes() {
		if route.Method == http.MethodPost {
			registered[route.Path] = true
		}
	}
	allProtocols := []service.GroupClientProtocol{
		service.GroupClientProtocolAnthropicMessages,
		service.GroupClientProtocolOpenAIResponses,
		service.GroupClientProtocolOpenAIChatCompletions,
		service.GroupClientProtocolGeminiGenerateContent,
	}
	tests := []struct {
		path     string
		pattern  string
		platform string
		protocol service.GroupClientProtocol
		format   groupClientProtocolErrorFormat
	}{
		{"/v1/responses", "/v1/responses", service.PlatformOpenAI, service.GroupClientProtocolOpenAIResponses, groupClientProtocolErrorOpenAI},
		{"/responses", "/responses", service.PlatformOpenAI, service.GroupClientProtocolOpenAIResponses, groupClientProtocolErrorOpenAI},
		{"/backend-api/codex/responses", "/backend-api/codex/responses", service.PlatformOpenAI, service.GroupClientProtocolOpenAIResponses, groupClientProtocolErrorOpenAI},
		{"/v1/responses/compact", "/v1/responses/*subpath", service.PlatformOpenAI, service.GroupClientProtocolOpenAIResponses, groupClientProtocolErrorOpenAI},
		{"/responses/compact", "/responses/*subpath", service.PlatformOpenAI, service.GroupClientProtocolOpenAIResponses, groupClientProtocolErrorOpenAI},
		{"/backend-api/codex/responses/compact", "/backend-api/codex/responses/*subpath", service.PlatformOpenAI, service.GroupClientProtocolOpenAIResponses, groupClientProtocolErrorOpenAI},
		{"/v1/responses/input_tokens", "/v1/responses/*subpath", service.PlatformOpenAI, service.GroupClientProtocolOpenAIResponses, groupClientProtocolErrorOpenAI},
		{"/responses/input_tokens", "/responses/*subpath", service.PlatformOpenAI, service.GroupClientProtocolOpenAIResponses, groupClientProtocolErrorOpenAI},
		{"/backend-api/codex/responses/input_tokens", "/backend-api/codex/responses/*subpath", service.PlatformOpenAI, service.GroupClientProtocolOpenAIResponses, groupClientProtocolErrorOpenAI},
		{"/v1/messages", "/v1/messages", service.PlatformAnthropic, service.GroupClientProtocolAnthropicMessages, groupClientProtocolErrorAnthropic},
		{"/antigravity/v1/messages", "/antigravity/v1/messages", service.PlatformAntigravity, service.GroupClientProtocolAnthropicMessages, groupClientProtocolErrorAnthropic},
		{"/v1/messages/count_tokens", "/v1/messages/count_tokens", service.PlatformAnthropic, service.GroupClientProtocolAnthropicMessages, groupClientProtocolErrorAnthropic},
		{"/messages/count_tokens", "/messages/count_tokens", service.PlatformAnthropic, service.GroupClientProtocolAnthropicMessages, groupClientProtocolErrorAnthropic},
		{"/v1/chat/completions", "/v1/chat/completions", service.PlatformOpenAI, service.GroupClientProtocolOpenAIChatCompletions, groupClientProtocolErrorOpenAI},
		{"/chat/completions", "/chat/completions", service.PlatformOpenAI, service.GroupClientProtocolOpenAIChatCompletions, groupClientProtocolErrorOpenAI},
		{"/v1beta/models/gemini-2.5-pro:generateContent", "/v1beta/models/*modelAction", service.PlatformGemini, service.GroupClientProtocolGeminiGenerateContent, groupClientProtocolErrorGoogle},
		{"/v1beta/models/gemini-2.5-pro:streamGenerateContent", "/v1beta/models/*modelAction", service.PlatformGemini, service.GroupClientProtocolGeminiGenerateContent, groupClientProtocolErrorGoogle},
		{"/v1beta/models/gemini-2.5-pro:countTokens", "/v1beta/models/*modelAction", service.PlatformGemini, service.GroupClientProtocolGeminiGenerateContent, groupClientProtocolErrorGoogle},
		{"/antigravity/v1beta/models/gemini-2.5-pro:generateContent", "/antigravity/v1beta/models/*modelAction", service.PlatformAntigravity, service.GroupClientProtocolGeminiGenerateContent, groupClientProtocolErrorGoogle},
		{"/antigravity/v1beta/models/gemini-2.5-pro:streamGenerateContent", "/antigravity/v1beta/models/*modelAction", service.PlatformAntigravity, service.GroupClientProtocolGeminiGenerateContent, groupClientProtocolErrorGoogle},
		{"/antigravity/v1beta/models/gemini-2.5-pro:countTokens", "/antigravity/v1beta/models/*modelAction", service.PlatformAntigravity, service.GroupClientProtocolGeminiGenerateContent, groupClientProtocolErrorGoogle},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			// 路由模板必须来自生产注册表，防止测试覆盖项目实际上不存在的别名。
			require.True(t, registered[tt.pattern], "route is not registered: %s", tt.pattern)
			for _, allowed := range []bool{false, true} {
				name := "denied"
				if allowed {
					name = "allowed"
				}
				t.Run(name, func(t *testing.T) {
					secondProtocols := make([]service.GroupClientProtocol, 0, len(allProtocols))
					for _, protocol := range allProtocols {
						if (protocol == tt.protocol) == allowed {
							secondProtocols = append(secondProtocols, protocol)
						}
					}
					first := &service.Group{ID: 1, Platform: tt.platform, AllowedClientProtocols: allProtocols}
					second := &service.Group{ID: 2, Platform: tt.platform, AllowedClientProtocols: secondProtocols}
					firstCalls, secondCalls := 0, 0
					var retry *gin.Context
					router := gin.New()
					router.POST(tt.pattern, func(c *gin.Context) {
						c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{SmartRouting: true, GroupID: &first.ID, Group: first})
						terminal := c.Handler()
						c.Next()

						// 模拟真实跨组执行：先完成首组，再复制上下文，换入已鉴权的第二组。
						retry = c.Copy()
						retry.Writer = c.Writer
						retry.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{SmartRouting: true, GroupID: &second.ID, Group: second})
						if enforceSmartRoutingRetryProtocol(retry) {
							terminal(retry)
						}
					}, requireGroupClientProtocol(tt.protocol, tt.format), func(c *gin.Context) {
						key, ok := servermiddleware.GetAPIKeyFromContext(c)
						require.True(t, ok)
						if key.Group.ID == first.ID {
							firstCalls++
							return
						}
						require.Equal(t, second.ID, key.Group.ID)
						secondCalls++
						c.JSON(http.StatusOK, gin.H{"group_id": second.ID})
					})
					w := httptest.NewRecorder()
					router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tt.path, nil))

					require.Equal(t, 1, firstCalls)
					require.NotNil(t, retry)
					if allowed {
						require.Equal(t, 1, secondCalls)
						require.Equal(t, http.StatusOK, w.Code)
						require.JSONEq(t, `{"group_id":2}`, w.Body.String())
						require.False(t, service.HasOpsClientBusinessLimited(retry))
						return
					}
					require.Zero(t, secondCalls)
					require.Equal(t, http.StatusForbidden, w.Code)
					require.Contains(t, w.Body.String(), groupClientProtocolDeniedMessage(tt.protocol))
					require.Equal(t, service.OpsClientBusinessLimitedReasonLocalPolicyDenied, retry.GetString(service.OpsClientBusinessLimitedReasonKey))
					switch tt.format {
					case groupClientProtocolErrorGoogle:
						require.Contains(t, w.Body.String(), "PERMISSION_DENIED")
					case groupClientProtocolErrorOpenAI:
						require.Contains(t, w.Body.String(), "protocol_not_allowed")
					default:
						require.Contains(t, w.Body.String(), "permission_error")
					}
				})
			}
		})
	}
}
