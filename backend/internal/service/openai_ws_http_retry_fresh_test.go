//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 池内两条空闲连接都已过期时，HTTP 经 WS 池转发也必须在安全重试中拨新连接。
func TestOpenAIWSHTTPRetryForcesFreshConn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	completed := func(id string) []byte {
		return []byte(`{"type":"response.completed","response":{"id":"` + id + `","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`)
	}
	staleA := &openAIWSCaptureConn{events: [][]byte{completed("resp_prime")}}
	staleB := &openAIWSCaptureConn{}
	fresh := &openAIWSCaptureConn{events: [][]byte{completed("resp_fresh")}}
	dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{staleA, fresh}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	defer pool.Close()
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	account := &Account{ID: 458, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://ws.test"}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	forward := func() *OpenAIForwardResult {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("session-id", "shared-session")
		group := int64(9)
		c.Set("api_key", &APIKey{ID: 21, GroupID: &group})
		result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":false,"store":false,"input":"hello"}`))
		require.NoError(t, err)
		require.NotNil(t, result)
		return result
	}
	require.Equal(t, "resp_prime", forward().RequestID)
	require.Equal(t, 1, dialer.DialCount())
	ap := pool.getOrCreateAccountPool(account.ID)
	ap.mu.Lock()
	lastAcquire := cloneOpenAIWSAcquireRequestPtr(ap.lastAcquire)
	ap.mu.Unlock()
	require.NotNil(t, lastAcquire)
	idle := newOpenAIWSConn(pool.nextConnID(account.ID), account.ID, staleB, nil, lastAcquire.TLSProfile, lastAcquire.TLSProfileKey)
	idle.handshakeCompatibility = normalizeOpenAIWSHandshakeCompatibility(account, lastAcquire.Headers)
	idle.routingAffinity = normalizeOpenAIWSRoutingAffinity(lastAcquire.Headers)
	ap.mu.Lock()
	ap.conns[idle.id] = idle
	ap.mu.Unlock()
	require.Equal(t, "resp_fresh", forward().RequestID)
	require.Equal(t, 2, dialer.DialCount(), "失败后的第二次尝试必须拨新连接，不能消耗另一条陈旧空闲连接")
	staleB.mu.Lock()
	staleBWrites := len(staleB.writes)
	staleB.mu.Unlock()
	require.Zero(t, staleBWrites, "另一条陈旧空闲连接不应收到重放请求")
}
