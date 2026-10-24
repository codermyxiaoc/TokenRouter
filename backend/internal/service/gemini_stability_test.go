//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type geminiStabilityEndpoint struct {
	name, path, body string
	call             func(*GeminiMessagesCompatService, *gin.Context, *Account, []byte) (*ForwardResult, error)
}

// 同一负面输入同时经过四个协议入口，避免兼容桥漏掉权限或计量规则。
func geminiStabilityEndpoints() []geminiStabilityEndpoint {
	return []geminiStabilityEndpoint{
		{"native", "/v1beta/models/studio-alias:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, func(s *GeminiMessagesCompatService, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.ForwardNative(c.Request.Context(), c, a, "studio-alias", "streamGenerateContent", true, b)
		}},
		{"messages", "/v1/messages", `{"model":"studio-alias","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, func(s *GeminiMessagesCompatService, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.Forward(c.Request.Context(), c, a, b)
		}},
		{"chat", "/v1/chat/completions", `{"model":"studio-alias","stream":true,"messages":[{"role":"user","content":"hello"}]}`, func(s *GeminiMessagesCompatService, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.ForwardAsChatCompletions(c.Request.Context(), c, a, b)
		}},
		{"responses", "/v1/responses", `{"model":"studio-alias","stream":true,"input":"hello"}`, func(s *GeminiMessagesCompatService, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.ForwardAsResponses(c.Request.Context(), c, a, b, nil)
		}},
	}
}

func TestGeminiStability_EmptyMetadataCannotEraseCollectedUsage(t *testing.T) {
	for _, ep := range geminiStabilityEndpoints() {
		for _, tail := range []string{`{}`, `null`, `[]`, `{"totalTokenCount":15}`, `{"promptTokenCount":null}`, `{"cachedContentTokenCount":null}`} {
			t.Run(ep.name+tail, func(t *testing.T) {
				body := []byte(ep.body)
				stream := geminiAbortFinalChunk + `data: {"usageMetadata":` + tail + `}` + "\n\n"
				svc, c, _ := newGeminiAbortingStreamService(t, ep.path, body, stream, true)
				result, err := ep.call(svc, c, geminiVertexAPIKeyTestAccount(""), body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.ClientDisconnect)
				require.Equal(t, 10, result.Usage.InputTokens)
				require.Equal(t, 5, result.Usage.OutputTokens)
			})
		}
	}
}

// 多帧 usage 是累计快照而非增量；缓存超过 prompt 时只钳制非缓存输入。
func TestGeminiStability_CumulativeUsageIsNotSummed(t *testing.T) {
	for _, ep := range geminiStabilityEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			body := []byte(ep.body)
			stream := geminiAbortUsageChunk + `data: {"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"cachedContentTokenCount":8,"candidatesTokenCount":7}}` + "\n\n"
			svc, c, _ := newGeminiAbortingStreamService(t, ep.path, body, stream, true)
			result, err := ep.call(svc, c, geminiVertexAPIKeyTestAccount(""), body)
			require.NoError(t, err)
			require.Equal(t, 0, result.Usage.InputTokens)
			require.Equal(t, 8, result.Usage.CacheReadInputTokens)
			require.Equal(t, 7, result.Usage.OutputTokens)
		})
	}
}

func TestGeminiStability_AccountAliasesKeepGroupImagePermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{PlatformGemini, PlatformAntigravity, PlatformComposite} {
		for _, ep := range geminiStabilityEndpoints() {
			for _, tc := range []struct {
				name, target        string
				enabled, wantDenied bool
			}{
				{"disabled image", "gemini-3-pro-image", false, true},
				{"enabled image", "gemini-3-pro-image", true, false},
				{"disabled text", "gemini-3-flash-preview", false, false},
			} {
				t.Run(platform+"/"+ep.name+"/"+tc.name, func(t *testing.T) {
					body := []byte(ep.body)
					svc, c, rec := newGeminiAbortingStreamService(t, ep.path, body, geminiAbortFinalChunk, true)
					account := geminiVertexAPIKeyTestAccount("")
					account.Credentials["model_mapping"] = map[string]any{"studio-*": tc.target}
					withGeminiImageGateAPIKey(c, &Group{ID: 1, Platform: platform, AllowImageGeneration: tc.enabled})
					result, err := ep.call(svc, c, account, body)
					if tc.wantDenied {
						require.Error(t, err)
						require.Nil(t, result)
						require.Zero(t, svc.httpUpstream.(*geminiCompatHTTPUpstreamStub).calls)
						require.Equal(t, http.StatusBadRequest, rec.Code)
						require.Contains(t, rec.Body.String(), ImageGenerationPermissionMessage())
						return
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.NotContains(t, rec.Body.String(), ImageGenerationPermissionMessage())
				})
			}
		}
	}
}

// 同一工具定义可能被并发请求复用，清理不能修改原 schema 或共享可变子树。
func TestGeminiStability_ToolSchemaConcurrentIsolation(t *testing.T) {
	var source any
	require.NoError(t, json.Unmarshal([]byte(`{"type":"object","properties":{"const":{"const":"fixed","examples":["sample"]},"examples":{"type":"array","items":{"const":"x"}}},"default":{"const":"user data","examples":["data"]}}`), &source))
	before, err := json.Marshal(source)
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan map[string]any, 32)
	for range 32 {
		wg.Go(func() { results <- cleanToolSchema(source).(map[string]any) })
	}
	wg.Wait()
	close(results)
	for result := range results {
		props := result["properties"].(map[string]any)
		require.Contains(t, props, "const")
		require.Contains(t, props, "examples")
		props["const"].(map[string]any)["type"] = "NUMBER"
		result["default"].(map[string]any)["const"] = "changed"
	}
	after, err := json.Marshal(source)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

// 显式零值仍是可用的累计快照，不应被“忽略空 metadata”规则误伤。
func TestGeminiStability_ExplicitZeroUsageIsPreserved(t *testing.T) {
	usage := extractGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"cachedContentTokenCount":0}}`))
	require.NotNil(t, usage)
	require.Equal(t, ClaudeUsage{}, *usage)
}
