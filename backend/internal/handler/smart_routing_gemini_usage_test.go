package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 只有智能路由实际观测到的无计量失败跳过成功后处理，不能丢失部分用量或借用历史拒绝。
func TestSmartRoutingGeminiFailureUsageBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		mutate func(*gin.Context, *service.APIKey, *service.ForwardResult)
		want   bool
	}{
		{name: "upstream_400_without_usage", want: true},
		{name: "normal_key", mutate: func(_ *gin.Context, key *service.APIKey, _ *service.ForwardResult) { key.SmartRouting = false }},
		{name: "input_tokens", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) { r.Usage.InputTokens = 4 }},
		{name: "output_tokens", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) { r.Usage.OutputTokens = 2 }},
		{name: "cached_tokens", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) { r.Usage.CacheReadInputTokens = 9 }},
		{name: "cache_creation", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) {
			r.Usage.CacheCreationInputTokens = 6
		}},
		{name: "cache_5m", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) { r.Usage.CacheCreation5mTokens = 1 }},
		{name: "cache_1h", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) { r.Usage.CacheCreation1hTokens = 1 }},
		{name: "image_tokens", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) { r.Usage.ImageOutputTokens = 1 }},
		{name: "image_count", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) { r.ImageCount = 1 }},
		{name: "image_sizes", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) { r.ImageOutputSizes = []string{"1K"} }},
		{name: "image_breakdown", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) {
			r.ImageSizeBreakdown = map[string]int{"1K": 1}
		}},
		{name: "search_usage", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) { r.SearchCount = 1 }},
		{name: "audio_usage", mutate: func(_ *gin.Context, _ *service.APIKey, r *service.ForwardResult) {
			r.AudioUsage = &service.AudioUsage{DurationOrUnits: 1}
		}},
		{name: "local_stream_error", mutate: func(c *gin.Context, _ *service.APIKey, _ *service.ForwardResult) {
			c.Set(service.OpsStreamErrorKey, service.OpsStreamError{IntendedStatus: http.StatusTooManyRequests})
		}},
		{name: "request_scoped_after_upstream_failure", mutate: func(c *gin.Context, _ *service.APIKey, _ *service.ForwardResult) {
			failure, _ := service.GetOpsStreamError(c)
			failure.RequestScoped = true
			c.Set(service.OpsStreamErrorKey, failure)
		}},
		{name: "stale_event", mutate: func(c *gin.Context, _ *service.APIKey, _ *service.ForwardResult) {
			failure, _ := service.GetOpsStreamError(c)
			failure.UpstreamStatus = http.StatusForbidden
			c.Set(service.OpsStreamErrorKey, failure)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:streamGenerateContent", nil)
			c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 1, UpstreamStatusCode: http.StatusBadRequest, Kind: "stream_failed"}})
			service.SetOpsUpstreamError(c, http.StatusBadRequest, "upstream invalid request", "")
			service.MarkOpsStreamFailure(c, "invalid_request_error", "INVALID_ARGUMENT", "upstream invalid request", http.StatusBadRequest)
			key := &service.APIKey{SmartRouting: true}
			result := &service.ForwardResult{Stream: true}
			if test.mutate != nil {
				test.mutate(c, key, result)
			}
			require.Equal(t, test.want, smartRoutingGeminiFailureWithoutUsage(c, key, result))
		})
	}
}
