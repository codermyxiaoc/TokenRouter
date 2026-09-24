package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type responsesPartialImageUpstream struct {
	service.HTTPUpstream
	body  string
	calls int
}

func (u *responsesPartialImageUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}

func (u *responsesPartialImageUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

type responsesPartialImageUsageSink struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (s *responsesPartialImageUsageSink) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	s.logs = append(s.logs, log)
	return true, nil
}

// 运行真实 Responses handler 和 Forward，防止图片 partial 再次绕过失败收尾进入成功调度。
func TestOpenAIResponsesPartialImagePreservesBillingAndFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	image := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"img_partial\",\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"aW1hZ2U=\"}}\n\n"
	for _, passthrough := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			tail    string
			success bool
		}{
			{name: "missing terminal"},
			{name: "upstream failed terminal", tail: "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_image\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"local failed after image\"}}}\n\n"},
			{name: "completed image remains successful", tail: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_image\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n", success: true},
		} {
			name := "native/" + tc.name
			if passthrough {
				name = "passthrough/" + tc.name
			}
			t.Run(name, func(t *testing.T) {
				logSink, restore := captureHandlerStructuredLog(t)
				defer restore()
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Default.RateMultiplier = 1
				groupID := int64(33001)
				accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{{
					ID: 33002, Name: "image-partial", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "local-test-only", "base_url": "https://upstream.example.test"},
					Extra:       map[string]any{"openai_passthrough": passthrough, "openai_text_route_mode": "force_responses"},
				}}}
				upstream := &responsesPartialImageUpstream{body: image + tc.tail}
				usage := &responsesPartialImageUsageSink{}
				cache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(cache.Stop)
				gateway := service.NewOpenAIGatewayService(accountRepo, usage, nil, nil, nil, nil, nil, cfg, nil, nil,
					service.NewBillingService(cfg, nil), nil, cache, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), cache,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.2","input":"hello","stream":true}`))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 33003, UserID: 33004, GroupID: &groupID,
					User: &service.User{ID: 33004, Status: service.StatusActive}, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 33004})
				h.Responses(c)
				require.Equal(t, 1, upstream.calls, "已经生成图片不能重试上游")
				require.Len(t, usage.logs, 1, "已生成图片必须且只能提交一次用量")
				require.Equal(t, 1, usage.logs[0].ImageCount)
				require.Contains(t, rec.Body.String(), "aW1hZ2U=", "追加失败终态不能覆盖已发送图片")
				if tc.success {
					require.NotContains(t, rec.Body.String(), `"type":"response.failed"`)
					require.True(t, logSink.ContainsMessageAtLevel("openai.request_completed", "debug"))
					return
				}
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.failed"`), "已有错误终态不能重复发送，缺少终态时须补齐")
				require.False(t, logSink.ContainsMessageAtLevel("openai.request_completed", "debug"), "部分图片失败不能落入成功调度分支")
				require.True(t, logSink.ContainsMessageAtLevel("openai.forward_failed", "warn") || logSink.ContainsMessageAtLevel("openai.forward_failed", "error"))
			})
		}
	}
}
