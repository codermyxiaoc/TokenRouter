package repository

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 307保留POST正文；图片策略必须在HTTP层阻止第二次生成，且不修改共享客户端。
func TestImageAttemptHTTPTransportDoesNotFollowPostRedirect(t *testing.T) {
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		if r.URL.Path == "/first" {
			w.Header().Set("Location", "/second")
			w.WriteHeader(307)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	ctx, cancel := service.WithImageAttemptPolicy(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/first", bytes.NewBufferString(`{"prompt":"fake"}`))
	require.NoError(t, err)
	base := server.Client()
	client := httpClientForUpstreamRequest(nil, base, req)
	resp, err := doUpstreamRequest(client, req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 307, resp.StatusCode)
	require.Equal(t, int64(1), sends.Load())
	require.Nil(t, base.CheckRedirect, "不能修改共享客户端")
}
