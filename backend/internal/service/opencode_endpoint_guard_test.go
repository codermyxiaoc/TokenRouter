//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 默认自适应模式须在三个标准入口都拒绝专用协议模型，不能向上游试错。
func TestOpenCodeDedicatedModelsRejectBeforeForward(t *testing.T) {
	for _, endpoint := range []string{"responses", "chat", "messages"} {
		for _, model := range []string{"gemini-3-pro", "jev-1.13"} {
			t.Run(endpoint+"/"+model, func(t *testing.T) {
				upstream := &httpUpstreamRecorder{}
				svc := newOpenAIImageGenerationControlTestService(upstream)
				c, recorder := newOpenAIImageGenerationControlTestContext(true, "test-client")
				account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "test-key", "api_protocol": APIProtocolAdaptive, "account_mode": AccountModeZen}}
				body := []byte(fmt.Sprintf(`{"model":%q,"input":"hi","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, model))
				var err error
				switch endpoint {
				case "responses":
					_, err = svc.Forward(context.Background(), c, account, body)
				case "chat":
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				case "messages":
					_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
				}
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				require.Nil(t, upstream.lastReq)
				require.Contains(t, recorder.Body.String(), "dedicated OpenCode endpoint")
			})
		}
	}
}

// 管理员指定协议和自定义模型规则继续生效；平台专用 System One 仍不能混用文本入口。
func TestOpenCodeDedicatedModelGuardPreservesExplicitPolicy(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_protocol": APIProtocolAdaptive}}
	require.True(t, rejectOpenCodeStandardModel(account, "opencode/gemini-3-pro"))
	account.Credentials["api_protocol"] = APIProtocolChatCompletions
	require.False(t, rejectOpenCodeStandardModel(account, "gemini-3-pro"))
	require.True(t, rejectOpenCodeStandardModel(account, "jev-1.13"))
	account.Credentials["api_protocol"] = APIProtocolAdaptive
	account.Credentials[openCodeGoProtocolRulesKey] = []any{map[string]any{"pattern": "gemini-*", "protocol": APIProtocolChatCompletions}}
	require.False(t, rejectOpenCodeStandardModel(account, "gemini-3-pro"))
	account.Credentials[openCodeGoProtocolRulesKey] = []any{}
	require.False(t, rejectOpenCodeStandardModel(account, "gemini-3-pro"), "显式空规则仍表示管理员指定 Chat 兜底")
	delete(account.Credentials, openCodeGoProtocolRulesKey)
	account.Credentials["model_mapping"] = map[string]any{"public-model": "gemini-3-pro", "gemini-alias": "gpt-5.6-sol"}
	require.True(t, rejectOpenCodeStandardModel(account, resolveOpenCodeGoMappedModel(account, []byte(`{"model":"public-model"}`), "")))
	require.False(t, rejectOpenCodeStandardModel(account, resolveOpenCodeGoMappedModel(account, []byte(`{"model":"gemini-alias"}`), "")))
	account.Platform = PlatformOpenAI
	require.False(t, rejectOpenCodeStandardModel(account, "gemini-3-pro"))
}
