//go:build unit

package middleware

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// replayPolicyMappedFailure 保留真实上游状态，复现兼容网关把上游 503 映射为客户端 502。
func replayPolicyMappedFailure(c *gin.Context) {
	service.SetOpsUpstreamError(c, http.StatusServiceUnavailable, "upstream temporarily unavailable", "")
	c.Header("Retry-After", "999")
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "mapped gateway failure"}})
}

// replayPolicyKey 使用同协议候选聚焦协调器行为，不依赖真实账号目录或外部服务。
func replayPolicyKey() *service.APIKey {
	key := smartRoutingTestKey()
	key.SmartRoutingCooldownSeconds = 60
	for index := range key.CompositeGroups {
		key.CompositeGroups[index].Group.Platform = service.PlatformOpenAI
	}
	return key
}

// 加密 reasoning 属于客户端历史；工具参数的同名字段也不能被当成服务端执行副作用。
func TestSmartRoutingReplayPolicyEncryptedReasoningAndSchemaPreservesBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "encrypted_reasoning",
			body: `{"model":"gpt-6-astra","input":[{"type":"reasoning","id":"rs_history","encrypted_content":"opaque-history","summary":[{"type":"summary_text","text":"keep summary"}]},{"type":"message","role":"user","content":"continue"}],"metadata":{"large_integer":9007199254740993}}`,
		},
		{
			name: "tool_schema_field",
			body: `{"model":"gpt-6-astra","input":"inspect schema","tools":[{"type":"function","name":"inspect","parameters":{"type":"object","properties":{"encrypted_content":{"type":"string","description":"ordinary application field"}}}}]}`,
		},
		{
			name: "client_shell_tools",
			body: `{"model":"gpt-6-astra","input":"continue","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},{"type":"custom","name":"exec"},{"type":"local_shell","name":"local"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := replayPolicyKey()
			resolver := &failoverResolver{}
			var visits []int64
			router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
				selected, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				visits = append(visits, *selected.GroupID)
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				// 比较原始字节，防止换组悄悄删除历史、改写工具或损失大整数。
				require.Equal(t, tt.body, string(body))
				if *selected.GroupID == 1 {
					replayPolicyMappedFailure(c)
					return
				}
				c.JSON(http.StatusOK, gin.H{"output": "second group success"})
			}, nil)

			w := callFailoverRouter(router, "/v1/responses", tt.body)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, []int64{1, 2}, visits)
			require.Contains(t, w.Body.String(), "second group success")
			require.NotContains(t, w.Body.String(), "mapped gateway failure")
			require.Empty(t, w.Header().Get("Retry-After"))
			require.Len(t, resolver.cooling, 1)
			require.Equal(t, resolver.now.Add(time.Minute), resolver.cooling[[2]int64{key.ID, 1}])

			// 后续请求复用冷却，只访问第二组，不能再次发送到首个失败组。
			visits = nil
			w = callFailoverRouter(router, "/v1/responses", tt.body)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, []int64{2}, visits)
			require.Len(t, resolver.cooling, 1)
			require.Nil(t, key.GroupID)
		})
	}
}

// 续接句柄和加密压缩历史必须保持原组，失败后冷却也不能把下一请求改派到其它候选。
func TestSmartRoutingReplayPolicyContinuationCooldownPreservesGroup(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"previous_response", `{"model":"gpt-6-astra","previous_response_id":"resp_original","input":[{"type":"function_call_output","call_id":"call_original","output":"result"}]}`},
		{"encrypted_compaction", `{"model":"gpt-6-astra","input":[{"type":"compaction","encrypted_content":"opaque-compressed-history"},{"type":"message","role":"user","content":"continue"}]}`},
		{"encrypted_compaction_summary", `{"model":"gpt-6-astra","input":[{"type":"compaction_summary","encrypted_content":"opaque-compressed-history"},{"type":"message","role":"user","content":"continue"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := replayPolicyKey()
			firstSchedulable := true
			resolver := &failoverResolver{availability: func(groupID int64) service.SmartRoutingGroupAvailability {
				return service.SmartRoutingGroupAvailability{HasModel: true, Schedulable: groupID != 1 || firstSchedulable}
			}}
			var visits []int64
			router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
				selected, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				visits = append(visits, *selected.GroupID)
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Equal(t, tt.body, string(body))
				firstSchedulable = false
				replayPolicyMappedFailure(c)
			}, nil)

			w := callFailoverRouter(router, "/v1/responses", tt.body)
			require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
			require.Equal(t, []int64{1}, visits)
			require.Len(t, resolver.cooling, 1)
			require.Equal(t, resolver.now.Add(time.Minute), resolver.cooling[[2]int64{key.ID, 1}])

			// 即使失败后首组账号已暂停调度，也必须先返回该组冷却，不能滑到下一组。
			visits = nil
			w = callFailoverRouter(router, "/v1/responses", tt.body)
			require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
			require.Equal(t, "SMART_ROUTING_CONTINUATION_GROUP_COOLING", gjson.GetBytes(w.Body.Bytes(), "error.code").String())
			require.Equal(t, "60", w.Header().Get("Retry-After"))
			require.Empty(t, visits)
			require.Len(t, resolver.cooling, 1)

			// 冷却到期且原组恢复后仍回到原组，句柄和压缩历史全程不改写。
			resolver.now = resolver.now.Add(61 * time.Second)
			firstSchedulable = true
			w = callFailoverRouter(router, "/v1/responses", tt.body)
			require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
			require.Equal(t, []int64{1}, visits)
		})
	}
}

// 后台和服务端工具可能已有副作用，当前请求只能执行一次，但真实上游失败仍须冷却该组。
func TestSmartRoutingReplayPolicyServerTasksDoNotReplayButCool(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"background", `{"model":"gpt-6-astra","background":true,"input":"work"}`},
		{"mcp", `{"model":"gpt-6-astra","input":"work","tools":[{"type":"mcp","server_label":"test"}]}`},
		{"shell", `{"model":"gpt-6-astra","input":"work","tools":[{"type":"shell"}]}`},
		{"code_execution", `{"model":"gpt-6-astra","input":"work","tools":[{"type":"code_execution"}]}`},
		{"code_interpreter", `{"model":"gpt-6-astra","input":"work","tools":[{"type":"code_interpreter"}]}`},
		{"computer", `{"model":"gpt-6-astra","input":"work","tools":[{"type":"computer"}]}`},
		{"computer_use_preview", `{"model":"gpt-6-astra","input":"work","tools":[{"type":"computer_use_preview"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := replayPolicyKey()
			resolver := &failoverResolver{}
			var visits []int64
			router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
				selected, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				visits = append(visits, *selected.GroupID)
				replayPolicyMappedFailure(c)
			}, nil)
			w := callFailoverRouter(router, "/v1/responses", tt.body)
			require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
			require.Equal(t, []int64{1}, visits)
			require.Len(t, resolver.cooling, 1)
			require.Equal(t, resolver.now.Add(time.Minute), resolver.cooling[[2]int64{key.ID, 1}])

			// 新的独立请求跳过冷却首组，只尝试第二组；第二组失败后也应进入冷却。
			visits = nil
			w = callFailoverRouter(router, "/v1/responses", tt.body)
			require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
			require.Equal(t, []int64{2}, visits)
			require.Len(t, resolver.cooling, 2)
			require.Equal(t, resolver.now.Add(time.Minute), resolver.cooling[[2]int64{key.ID, 2}])
		})
	}
}

// 本地参数和策略限流不能冒充上游故障；已有上游错误上下文也不能覆盖最终策略拒绝。
func TestSmartRoutingReplayPolicyLocalErrorsDoNotCool(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		upstream    bool
		localPolicy bool
	}{
		{"local_bad_request", http.StatusBadRequest, false, false},
		{"local_rate_limit", http.StatusTooManyRequests, false, false},
		{"policy_after_upstream_failure", http.StatusTooManyRequests, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := replayPolicyKey()
			resolver := &failoverResolver{}
			var visits []int64
			router := newFailoverRouter(t, key, resolver, false, func(c *gin.Context) {
				selected, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				visits = append(visits, *selected.GroupID)
				if tt.upstream {
					service.SetOpsUpstreamError(c, http.StatusServiceUnavailable, "previous upstream failure", "")
				}
				if tt.localPolicy {
					service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
				}
				c.JSON(tt.status, gin.H{"error": gin.H{"message": "local refusal"}})
			}, nil)
			body := `{"model":"gpt-6-astra","input":[{"type":"reasoning","encrypted_content":"opaque-history"}]}`
			for request := 0; request < 2; request++ {
				w := callFailoverRouter(router, "/v1/responses", body)
				require.Equal(t, tt.status, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), "local refusal")
			}
			require.Equal(t, []int64{1, 1}, visits)
			require.Empty(t, resolver.cooling)
		})
	}
}
