package repository

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 真实共享传输必须遵守视频创建的禁重放标记，且不能改变后续普通请求的跳转行为。
func TestHTTPUpstreamVideoPostRedirectIsolation(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, withTLS := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/tls-entry=%t", code, withTLS), func(t *testing.T) {
				var redirectedCalls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/create" {
						http.Redirect(w, r, "/target", code)
						return
					}
					redirectedCalls.Add(1)
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(server.Close)
				upstream := NewHTTPUpstream(nil)
				req, err := http.NewRequestWithContext(service.WithHTTPUpstreamRedirectsDisabled(t.Context()), http.MethodPost, server.URL+"/create", bytes.NewBufferString(`{"model":"video"}`))
				require.NoError(t, err)
				var response *http.Response
				if withTLS {
					response, err = upstream.DoWithTLS(req, "", 1, 1, nil)
				} else {
					response, err = upstream.Do(req, "", 1, 1)
				}
				require.NoError(t, err)
				require.Equal(t, code, response.StatusCode)
				require.NoError(t, response.Body.Close())
				require.Zero(t, redirectedCalls.Load())

				ordinary, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/create", nil)
				require.NoError(t, err)
				response, err = upstream.Do(ordinary, "", 1, 1)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.NoError(t, response.Body.Close())
				require.Equal(t, int64(1), redirectedCalls.Load())
			})
		}
	}
}
