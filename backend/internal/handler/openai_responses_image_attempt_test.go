//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const responsesImageAttemptSuccess = `{"id":"resp_image_safe","status":"completed","object":"response","output":[{"type":"image_generation_call","id":"img_safe","status":"completed","result":"aW1hZ2U="}],"usage":{"input_tokens":3,"output_tokens":2}}`

func newResponsesImageAttemptHandler(t *testing.T, passthrough bool, upstream *imageRoutePolicyUpstream, cooldown *imageRoutePolicyCache) (*OpenAIGatewayHandler, *responsesPartialImageUsageSink) {
	t.Helper()
	accounts := make([]service.Account, 0, 3)
	for id := int64(81001); id <= 81003; id++ {
		accounts = append(accounts, service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: int(id),
			Credentials: map[string]any{"api_key": "local-test", "base_url": "https://upstream.example.test", "pool_mode": true, "pool_mode_retry_count": 10},
			Extra:       map[string]any{"openai_passthrough": passthrough, "openai_text_route_mode": "force_responses"}})
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	usage := &responsesPartialImageUsageSink{}
	gw := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: accounts}, usage, nil, nil, nil, nil, cooldown, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billing, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	return NewOpenAIGatewayHandler(gw, service.NewConcurrencyService(&fakeConcurrencyCache{}), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg), usage
}

func runResponsesImageAttemptRequest(h *OpenAIGatewayHandler, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"model":"gpt-5.2","input":%s,"tools":[{"type":"image_generation","model":"gpt-image-2"}],"stream":false}`, input)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	groupID := int64(81001)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 81001, UserID: 81001, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true, RateMultiplier: 1},
		User:  &service.User{ID: 81001, Balance: 100, Status: service.StatusActive}})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 81001})
	h.Responses(c)
	return rec
}

// 两条真实 Responses 转发路径都不得沿用文本池的同号重试或超过两次发送。
func TestPublicResponsesImageSafeSwitchAndSharedCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%t", passthrough), func(t *testing.T) {
			upstream := &imageRoutePolicyUpstream{respond: func(id int64) (int, string, error) {
				if id == 81001 {
					return 429, `{"error":{"code":"rate_limit_exceeded"}}`, nil
				}
				return 200, responsesImageAttemptSuccess, nil
			}}
			cooldown := &imageRoutePolicyCache{until: map[string]time.Time{}}
			h, usage := newResponsesImageAttemptHandler(t, passthrough, upstream, cooldown)
			rec := runResponsesImageAttemptRequest(h, `"draw"`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, []int64{81001, 81002}, upstream.calls)
			require.Equal(t, upstream.deadlines[0], upstream.deadlines[1], "备用账号不能刷新图片执行截止")
			require.Len(t, usage.logs, 1, "只给实际成功的账号记录一次使用量")
			require.Equal(t, int64(81002), usage.logs[0].AccountID)
			require.Equal(t, 1, usage.logs[0].ImageCount)
			rec = runResponsesImageAttemptRequest(h, `"draw another"`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, []int64{81001, 81002, 81002}, upstream.calls, "新请求避开失败账号的同一图片模型")
			textCooldown, err := cooldown.GetImageModelCooldown(context.Background(), 81001, "gpt-5.2", "generate")
			require.NoError(t, err)
			require.Zero(t, textCooldown, "图片工具故障不能冷却文本主模型")
		})
	}
}

// 错误 HTTP、网络未知与成功响应后失败分开验证，防止自动回退造成重复上游费用。
func TestPublicResponsesImageFailureSafetyMatrix(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			status int
			body   string
			err    error
			calls  int
			cool   bool
		}{
			{"reject_two_accounts", 429, `{"error":{"code":"rate_limit_exceeded"}}`, nil, 2, true},
			{"unknown_502", 502, `{"error":{"type":"invalid_request_error"}}`, nil, 1, true},
			{"bad_parameter", 400, `{"error":{"code":"invalid_parameter","message":"Unsupported parameter: store"}}`, nil, 1, false},
			{"network_unknown", 0, "", io.ErrUnexpectedEOF, 1, true},
			{"accepted_task_in_error", 429, `{"id":"resp_accepted","error":{"code":"rate_limit_exceeded"}}`, nil, 1, false},
			{"failed_200", 200, `{"id":"resp_failed","status":"failed","error":{"code":"server_error"}}`, nil, 1, false},
		} {
			t.Run(fmt.Sprintf("%t/%s", passthrough, tc.name), func(t *testing.T) {
				upstream := &imageRoutePolicyUpstream{respond: func(int64) (int, string, error) { return tc.status, tc.body, tc.err }}
				cooldown := &imageRoutePolicyCache{until: map[string]time.Time{}}
				h, usage := newResponsesImageAttemptHandler(t, passthrough, upstream, cooldown)
				runResponsesImageAttemptRequest(h, `"draw"`)
				require.Len(t, upstream.calls, tc.calls)
				require.Empty(t, usage.logs, "无实际输出或计量不能捏造扣费")
				remaining, err := cooldown.GetImageModelCooldown(context.Background(), 81001, "gpt-image-2", "generate")
				require.NoError(t, err)
				require.Equal(t, tc.cool, remaining > 0)
			})
		}
	}
}

// 总预算结束必须返回504或已有SSE的错误终态，不能把服务超时当成主动取消后留下空200。
func TestPublicResponsesImageDeadlineReturnsExplicitFailure(t *testing.T) {
	for _, streamStarted := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", streamStarted), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			ctx, cancel := service.WithImageAttemptPolicy(context.Background(), time.Nanosecond)
			defer cancel()
			<-ctx.Done()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			if streamStarted {
				c.Writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = c.Writer.WriteString(": keepalive\n\n")
			}
			before := c.Writer.Size()
			h := &OpenAIGatewayHandler{}
			require.False(t, h.retryResponsesImageOnDifferentAccount(c, &service.Account{ID: 1}, context.DeadlineExceeded, before, streamStarted))
			require.Contains(t, rec.Body.String(), "image_execution_timeout")
			if !streamStarted {
				require.Equal(t, http.StatusGatewayTimeout, rec.Code)
			}
		})
	}
}

// 全冷却不提交上游，Retry-After取三个账号中最早的截止，而不是最后一个账号的时长。
func TestPublicResponsesImageAllCoolingReturnsEarliestRetryAfter(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%t", passthrough), func(t *testing.T) {
			upstream := &imageRoutePolicyUpstream{respond: func(int64) (int, string, error) { return 200, responsesImageAttemptSuccess, nil }}
			cooldown := &imageRoutePolicyCache{until: map[string]time.Time{}}
			for id, duration := range map[int64]time.Duration{81001: 2 * time.Second, 81002: 10 * time.Second, 81003: 30 * time.Second} {
				require.NoError(t, cooldown.CooldownImageModel(context.Background(), id, "gpt-image-2", "generate", duration))
			}
			h, usage := newResponsesImageAttemptHandler(t, passthrough, upstream, cooldown)
			rec := runResponsesImageAttemptRequest(h, `"draw"`)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			require.Equal(t, "2", rec.Header().Get("Retry-After"))
			require.Empty(t, upstream.calls)
			require.Empty(t, usage.logs)
		})
	}
}

// HTTP200里的失败状态不能当成功，但其中已有的Token或图片必须保留且只结算一次。
func TestPublicResponsesImageFailedJSONPreservesActualUsage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, withImage := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough_%t/image_%t", passthrough, withImage), func(t *testing.T) {
				body := `{"id":"resp_failed_usage","status":"failed","error":{"code":"server_error"},"usage":{"input_tokens":3,"output_tokens":2}}`
				if withImage {
					body = `{"id":"resp_failed_image","status":"failed","error":{"code":"server_error"},"output":[{"type":"image_generation_call","status":"completed","result":"aW1hZ2U="}]} `
				}
				upstream := &imageRoutePolicyUpstream{respond: func(int64) (int, string, error) { return 200, body, nil }}
				cooldown := &imageRoutePolicyCache{until: map[string]time.Time{}}
				h, usage := newResponsesImageAttemptHandler(t, passthrough, upstream, cooldown)
				rec := runResponsesImageAttemptRequest(h, `"draw"`)
				require.Equal(t, []int64{81001}, upstream.calls)
				require.Len(t, usage.logs, 1)
				if withImage {
					require.Equal(t, 1, usage.logs[0].ImageCount)
					require.Contains(t, rec.Body.String(), "aW1hZ2U=")
					require.Contains(t, rec.Body.String(), `"status":"failed"`)
				} else {
					require.Equal(t, http.StatusBadGateway, rec.Code)
					require.Zero(t, usage.logs[0].ImageCount)
					require.EqualValues(t, 3, usage.logs[0].InputTokens)
					require.EqualValues(t, 2, usage.logs[0].OutputTokens)
				}
			})
		}
	}
}
