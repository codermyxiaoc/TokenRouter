//go:build unit

package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 同一 session 上并行的 turn 与 memory 不得覆盖彼此的 HTTP 状态来源。
func TestOpenAIExecutionScopeHTTPProvenanceIsolatesLanes(t *testing.T) {
	svc := &OpenAIGatewayService{}
	turn, _ := newTurnStateTestContext(t, 7, "shared-session")
	memory, _ := newTurnStateTestContext(t, 7, "shared-session")
	turnBody := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"thread_id\":\"thread\",\"request_kind\":\"turn\"}"},"input":"hello"}`)
	memoryBody := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"thread_id\":\"thread\",\"request_kind\":\"memory\"}"},"input":"summarize"}`)
	turnScope := rememberOpenAIWSExecutionScope(turn, turnBody)
	memoryScope := rememberOpenAIWSExecutionScope(memory, memoryBody)
	require.NotEmpty(t, turnScope)
	require.NotEqual(t, turnScope, memoryScope)
	svc.relayOpenAICodexTurnState(turn, &Account{ID: 11}, http.Header{"X-Codex-Turn-State": {"turn-state"}})
	svc.relayOpenAICodexTurnState(memory, &Account{ID: 12}, http.Header{"X-Codex-Turn-State": {"memory-state"}})
	for _, tc := range []struct {
		c       *gin.Context
		account int64
		state   string
	}{{turn, 11, "turn-state"}, {memory, 12, "memory-state"}} {
		same := http.Header{"X-Codex-Turn-State": {tc.state}}
		svc.guardOpenAICodexTurnStateEcho(tc.c, &Account{ID: tc.account}, same)
		require.Equal(t, tc.state, same.Get("X-Codex-Turn-State"))
		foreign := http.Header{"X-Codex-Turn-State": {tc.state}}
		svc.guardOpenAICodexTurnStateEcho(tc.c, &Account{ID: 99}, foreign)
		require.Empty(t, foreign.Get("X-Codex-Turn-State"), "故障转移后仍须剥离其它账号签发的状态")
	}
	require.Equal(t, svc.GenerateSessionHash(turn, turnBody), svc.GenerateSessionHash(memory, memoryBody), "账号粘性规则不随执行状态隔离而改变")
}

// 保存空身份同样重要：指纹 full 后注入的公共 thread 不能反过来成为客户端身份。
func TestOpenAIExecutionScopeRemembersOriginalIdentityAcrossRewrites(t *testing.T) {
	c, _ := newTurnStateTestContext(t, 7, "")
	original := []byte(`{"model":"gpt-5.1","input":"hello"}`)
	require.Empty(t, rememberOpenAIWSExecutionScope(c, original))
	rewritten := []byte(`{"model":"gpt-5.1","client_metadata":{"thread_id":"injected-thread"},"prompt_cache_key":"account-default","input":"hello"}`)
	require.Empty(t, rememberOpenAIWSExecutionScope(c, rewritten))
	require.Empty(t, openAICodexTurnStateSeed(c))

	explicit, _ := newTurnStateTestContext(t, 7, "")
	raw := []byte(`{"type":"response.create","response":{"model":"gpt-5.1","prompt_cache_key":"explicit-key","client_metadata":{"thread_id":"original-thread","x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"}}}`)
	flat := []byte(`{"model":"gpt-5.1","prompt_cache_key":"explicit-key","client_metadata":{"thread_id":"original-thread","x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"}}`)
	got := rememberOpenAIWSExecutionScope(explicit, raw)
	want, thread := resolveOpenAIWSExecutionScope(explicit, flat, 7)
	require.Equal(t, "original-thread", thread)
	require.NotEmpty(t, got)
	require.Equal(t, want, got, "嵌套与平铺的 response.create 使用同一身份")
	require.Equal(t, got, rememberOpenAIWSExecutionScope(explicit, rewritten))
}

// 失效密文读写必须随执行身份迁移，不能在新键未命中时读取其它线程留下的旧键。
func TestOpenAIExecutionScopeEncryptedLineageDoesNotReadLegacyState(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: store}
	group := int64(9)
	c, _ := newTurnStateTestContext(t, 7, "shared-session")
	c.Set("api_key", &APIKey{ID: 7, GroupID: &group})
	raw := []byte(`{"client_metadata":{"thread_id":"child-thread"},"input":"hello"}`)
	scope := rememberOpenAIWSExecutionScope(c, raw)
	legacy := svc.GenerateSessionHash(c, raw)
	require.NotEqual(t, legacy, scope)
	store.MarkSessionInvalidEncryptedContent(group, legacy, []string{"parent-invalid"}, time.Hour)
	require.Equal(t, scope, svc.openAIWSLineageSessionHashFromContext(c, []byte(`{"client_metadata":{"thread_id":"rewritten"}}`)))
	require.Empty(t, svc.sessionInvalidEncryptedContentDigests(group, scope))
	svc.markOpenAIWSInvalidEncryptedContentLineage(group, scope, []string{"child-invalid"})
	require.Contains(t, svc.sessionInvalidEncryptedContentDigests(group, scope), "child-invalid")
	require.NotContains(t, svc.sessionInvalidEncryptedContentDigests(group, scope), "parent-invalid")
	c.Set(openAIWSIngressSessionHashContextKey, scope)
	require.Equal(t, scope, svc.openAIWSLineageSessionHashFromContext(c, nil), "桥接恢复沿用入站原始作用域")
}

// 客户端可使用任意会话标识，分隔符文本不得让主通道冒充 memory/guardian 通道。
func TestOpenAIExecutionScopeSeedRejectsDelimiterCollisions(t *testing.T) {
	for _, identity := range []string{"thread", "session"} {
		for _, lane := range []string{"kind=memory", "subagent=guardian"} {
			plain := openAIWSExecutionScopeSeed(7, identity, "t|"+lane, "")
			detached := openAIWSExecutionScopeSeed(7, identity, "t", lane)
			require.NotEqual(t, plain, detached)
			plainHash, _ := deriveOpenAISessionHashes(plain)
			detachedHash, _ := deriveOpenAISessionHashes(detached)
			require.NotEqual(t, plainHash, detachedHash)
		}
	}
}
