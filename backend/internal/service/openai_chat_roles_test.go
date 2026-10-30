//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// chatRoleContractUpstream 只替换上游传输：strict 时像 DeepSeek 一样拒绝 developer 角色。
type chatRoleContractUpstream struct {
	httpUpstreamRecorder
	strict bool
}

func (u *chatRoleContractUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	status, contentType := http.StatusOK, "application/json"
	result := `{"id":"chatcmpl_roles","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10}}`
	if u.strict {
		for _, message := range gjson.GetBytes(body, "messages").Array() {
			if message.Get("role").String() == "developer" {
				status = http.StatusUnprocessableEntity
				result = "{\"error\":{\"message\":\"unknown variant `developer`\",\"type\":\"invalid_request_error\"}}"
			}
		}
	}
	if status == http.StatusOK && gjson.GetBytes(body, "stream").Bool() {
		contentType = "text/event-stream"
		result = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1,\"total_tokens\":10}}\n\ndata: [DONE]\n\n"
	}
	u.resp = &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(result))}
	return u.httpUpstreamRecorder.Do(req, proxyURL, accountID, concurrency)
}

// DoWithTLS 覆盖 OpenCode 等使用 TLS 指纹的发送路径，与 Do 共用同一判定。
func (u *chatRoleContractUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func forwardRawChatForRoleTest(t *testing.T, platform, model, base string, stream bool, strict bool) (*chatRoleContractUpstream, []byte, []byte) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"model":"example-alias","messages":[{"role":"developer","content":"Keep these instructions."},{"role":"user","content":"ping"}],"stream":%t,"max_tokens":10}`, stream))
	original := bytes.Clone(body)
	upstream := &chatRoleContractUpstream{strict: strict}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := rawChatCompletionsTestAccount()
	account.Platform = platform
	account.Credentials["base_url"] = base
	account.Credentials["api_protocol"] = APIProtocolChatCompletions
	account.Credentials["model_mapping"] = map[string]any{"example-alias": model}
	account.Extra = map[string]any{openai_compat.ExtraKeyResponsesProbeStatus: string(openai_compat.ResponsesProbeStatusUnsupported)}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "pong")
	require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
	return upstream, original, body
}

// OpenCode 转发映射后的 DeepSeek 模型时把 developer 改为 system；流式与非流式、OpenCode 平台与指向 OpenCode 的兼容账号都覆盖。
func TestForwardAsChatCompletions_OpenCodeDeepSeekDeveloperRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, target := range []struct{ platform, model, base string }{
			{PlatformOpenCodeGo, "deepseek-v4.1-flash", DefaultOpenCodeGoBaseURL},
			{PlatformOpenCodeGo, "opencode-go/deepseek-v4-flash", DefaultOpenCodeGoBaseURL},
			{PlatformOpenAI, "deepseek-v4.1-flash", DefaultOpenCodeGoBaseURL},
			{PlatformOpenAI, "deepseek-v4.1-flash", DefaultOpenCodeZenBaseURL},
		} {
			t.Run(fmt.Sprintf("%s/%s/%s/stream=%t", target.platform, target.model, target.base, stream), func(t *testing.T) {
				upstream, original, body := forwardRawChatForRoleTest(t, target.platform, target.model, target.base, stream, true)
				require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
				require.Equal(t, "Keep these instructions.", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
				require.Equal(t, original, body, "按账号适配不能修改换号重试使用的原始正文")
			})
		}
	}
}

// 本 fork 只规范化 OpenCode→DeepSeek：OpenCode 的其它模型、DeepSeek/Kimi/智谱平台及其它兼容上游的原生 Chat 正文保持 developer 不变。
func TestForwardAsChatCompletions_DeveloperRoleUnchangedElsewhere(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, target := range []struct{ platform, model, base string }{
		{PlatformOpenCodeGo, "kimi-k3", DefaultOpenCodeGoBaseURL},
		{PlatformOpenAI, "glm-5.3", DefaultOpenCodeZenBaseURL},
		{PlatformDeepseek, "deepseek-flash", "https://api.deepseek.com"},
		{PlatformKimi, "k3", "https://api.kimi.com/coding"},
		{PlatformOpenAI, "deepseek-flash", "https://api.deepseek.com"},
		{PlatformOpenAI, "deepseek-v4.1-flash", "https://compatible.example.test/v1"},
	} {
		t.Run(target.platform+"/"+target.model+"/"+target.base, func(t *testing.T) {
			upstream, _, _ := forwardRawChatForRoleTest(t, target.platform, target.model, target.base, false, false)
			require.Equal(t, "developer", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
		})
	}
}

// 只改写消息角色，保留顺序、多模态内容、工具调用参数、未知字段和大整数精度；无 developer 时正文字节不变。
func TestNormalizeOpenCodeDeepSeekDeveloperRoles_PreservesConversation(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"system","content":"first"},{"role":"developer","content":[{"type":"text","text":"developer is just text"}],"name":"rules"},{"role":"user","content":"ping"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"role\":\"developer\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"},{"role":"developer","content":"last"}],"metadata":{"large":9007199254740993,"role":"developer"},"stream":true}`)
	got, err := normalizeOpenCodeDeepSeekDeveloperRoles(account, DefaultOpenCodeGoBaseURL+"/chat/completions", body)
	require.NoError(t, err)
	require.JSONEq(t, strings.ReplaceAll(string(body), `"role":"developer","content"`, `"role":"system","content"`), string(got))
	require.Contains(t, string(got), "9007199254740993")
	require.Equal(t, "developer", gjson.GetBytes(got, "metadata.role").String())

	plain := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"ping"}]}`)
	unchanged, err := normalizeOpenCodeDeepSeekDeveloperRoles(account, DefaultOpenCodeGoBaseURL, plain)
	require.NoError(t, err)
	require.Equal(t, plain, unchanged)

	// 只认官方 HTTPS 主机，主机名伪装不命中；OAuth 账号不在范围内。
	compat := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, host := range []string{"https://opencode.ai.evil.test/zen/v1", "http://opencode.ai/zen/v1", "https://user@relay.test/opencode.ai"} {
		require.False(t, requiresOpenCodeDeepSeekSystemRole(compat, host, "deepseek-v4-flash"), host)
	}
	require.False(t, requiresOpenCodeDeepSeekSystemRole(&Account{Platform: PlatformOpenCodeGo, Type: AccountTypeOAuth}, DefaultOpenCodeGoBaseURL, "deepseek-v4-flash"))

	_, err = normalizeOpenCodeDeepSeekDeveloperRoles(account, DefaultOpenCodeGoBaseURL, []byte(`{"model":"deepseek-v4-flash","messages":"bad"}`))
	require.Error(t, err)
}
