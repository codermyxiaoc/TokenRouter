package handler

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// 真实 WS 首帧拒绝必须发生在账号选择之前，并归还入站连接租约。
func TestOpenAIWSRejectsAmbiguousModelBeforeAccountSelection(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.create","model":"allowed","model":"blocked"}`,
		`{"type":"response.create","model":"allowed","\u006dodel":"blocked"}`,
		`{"type":"response.create","model":"allowed","Model":"blocked"}`,
		`{"type":"noop","type":"response.create","model":"allowed"}`,
		`{"Type":"noop","type":"response.create","model":"allowed"}`,
		`{"type":"response.create","model":"allowed","previous_response_id":null,"previous_response_id":"resp_foreign"}`,
		`{"type":"response.create","model":"allowed","prompt_cache_key":"one","prompt_cache_key":"two"}`,
		`{"type":"response.create","model":"allowed","previous_response_id":null,"previous_reſponse_id":"resp_foreign"}`,
	} {
		t.Run(payload, func(t *testing.T) {
			cache := &concurrencyCacheMock{acquireIngressLeaseFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
			h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
			h.cfg = &config.Config{}
			h.cfg.Gateway.OpenAIWS.MaxIngressConnectionsPerAPIKey = 1
			server := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
			require.NoError(t, err)
			defer conn.CloseNow()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(payload)))
			_, _, err = conn.Read(ctx)
			var closeErr coderws.CloseError
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
			require.Contains(t, closeErr.Reason, "duplicate model")
			require.Eventually(t, func() bool { return atomic.LoadInt32(&cache.releaseIngressCalled) == 1 }, time.Second, 10*time.Millisecond)
		})
	}
}
