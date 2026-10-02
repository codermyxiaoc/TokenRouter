package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/util/responseheaders"
	"github.com/stretchr/testify/require"
)

// 替身使用真实本地 HTTP，并遵守生产传输的请求级禁跳转标记。
type videoRedirectHTTPFixture struct{ client *http.Client }

func (f videoRedirectHTTPFixture) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	client := *f.client
	if HTTPUpstreamRedirectsDisabled(req.Context()) {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return client.Do(req)
}

func (f videoRedirectHTTPFixture) DoWithTLS(req *http.Request, proxy string, accountID int64, limit int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(req, proxy, accountID, limit)
}

// 六种创建协议均只能发送一次，既不重放 POST，也不把 301/302/303 跳转当成成功查询。
func TestVideoSubmissionRedirectNeverFollows(t *testing.T) {
	for _, endpoint := range []VideoEndpoint{VideoEndpointCompat, VideoEndpointOpenAIVideos, VideoEndpointSeedance, VideoEndpointKling, VideoEndpointWan, VideoEndpointMiniMax} {
		for _, code := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("%s/%d", endpoint, code), func(t *testing.T) {
				var calls atomic.Int64
				body := `{"id":"original-task","status":"queued"}`
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.Path == "/create" {
						w.Header().Set("Location", "/second")
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(code)
						_, _ = io.WriteString(w, body)
						return
					}
					_, _ = io.WriteString(w, `{"id":"duplicate-task","status":"queued"}`)
				}))
				t.Cleanup(server.Close)
				account := videoFixtureAccount(endpoint)
				svc := NewVideoUpstreamService(&OpenAIGatewayService{
					httpUpstream:         videoRedirectHTTPFixture{client: server.Client()},
					responseHeaderFilter: responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{}),
				})
				target := VideoUpstreamTarget{Version: 1, Endpoint: endpoint, BaseURL: server.URL, CreatePath: "/create", Model: "m", AccountID: account.ID}
				ctx := context.Background()
				result, err := svc.Submit(ctx, &VideoUpstreamSelection{Account: account, Target: target, Body: []byte(`{"model":"m"}`)})
				require.NoError(t, err)
				require.Equal(t, int64(1), calls.Load())
				require.Equal(t, code, result.StatusCode)
				require.Equal(t, body, string(result.Body))
				require.Empty(t, result.Header.Get("Location"), "客户端也不能收到自动重放创建的跳转指令")
				require.NotEqual(t, "duplicate-task", result.TaskID)
				require.False(t, HTTPUpstreamRedirectsDisabled(ctx), "不能改变其他共享请求的重定向策略")
			})
		}
	}
}

// 3xx 不是明确创建拒绝；无归属时保持待核对，有归属时只查询原任务，均不重新生成。
func TestVideoSubmissionRedirectRetainsBudgetAndTask(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		for _, withTaskID := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/id=%t", code, withTaskID), func(t *testing.T) {
				s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideo)
				upstream.submit = &VideoUpstreamResponse{StatusCode: code, Body: []byte(`{"message":"redirect"}`)}
				if withTaskID {
					upstream.submit.TaskID = "accepted-task"
					upstream.submit.Body = []byte(`{"id":"accepted-task","status":"queued"}`)
				}
				req := VideoTaskSubmitRequest{Body: []byte(`{}`), IdempotencyKey: "redirect-once"}
				response, err := s.Submit(t.Context(), key, req)
				require.NoError(t, err)
				require.Equal(t, code, response.StatusCode)
				task, err := s.repo.Get(t.Context(), response.LocalTaskID)
				require.NoError(t, err)
				require.Equal(t, "submission_unknown", task.Status)
				require.Equal(t, "reconciliation", task.BillingStatus)
				require.Zero(t, billing.captures)
				require.Zero(t, billing.releases)
				_, err = s.Submit(t.Context(), key, req)
				require.NoError(t, err)
				require.Equal(t, 1, upstream.submitted)
				upstream.poll = &VideoUpstreamResponse{StatusCode: 200, TaskID: "accepted-task", Status: "failed", Body: []byte(`{"id":"accepted-task","status":"failed"}`)}
				s.advance(t.Context(), task)
				if withTaskID {
					require.Equal(t, "accepted-task", task.UpstreamTaskID)
					require.Equal(t, 1, upstream.polls)
					require.Equal(t, 1, billing.releases)
				} else {
					require.Empty(t, task.UpstreamTaskID)
					require.Zero(t, upstream.polls)
					require.Zero(t, billing.releases)
				}
				require.Equal(t, 1, upstream.submitted)
				require.Zero(t, billing.captures)
				require.Empty(t, logs.logs)
			})
		}
	}
}
