package handler

import (
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

// NonStream 标记落库 stream=false，未标记的沿用流式。
func TestLogOpsStreamError_NonStreamInBandContentPolicy(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.7-flash:generateContent", nil)
	setOpsRequestContext(c, "gemini-3.7-flash", false)

	service.MarkOpsStreamErrorValue(c, service.OpsStreamError{
		ErrType:        "invalid_request_error",
		Code:           "PROHIBITED_CONTENT",
		Message:        "Gemini content policy stop (finishReason=PROHIBITED_CONTENT): Response stopped due to prohibited content.",
		IntendedStatus: http.StatusBadRequest,
		RequestScoped:  true,
		NonStream:      true,
	})

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	logOpsStreamError(c, ops, http.StatusOK)

	job := <-opsErrorLogQueue
	require.NotNil(t, job.entry)
	require.False(t, job.entry.Stream)
	require.Equal(t, "invalid_request_error", job.entry.ErrorType)
	require.Equal(t, "request", job.entry.ErrorPhase)
	require.Equal(t, "client", job.entry.ErrorOwner)
	require.True(t, job.entry.IsBusinessLimited)
	require.Equal(t, http.StatusBadRequest, job.entry.StatusCode, "请求级带内结果按 IntendedStatus 落库，才能进入错误列表")
	require.Equal(t, "P3", job.entry.Severity)
	require.Equal(t, "gemini-3.7-flash", job.entry.Model)
	require.Contains(t, job.entry.ErrorBody, "PROHIBITED_CONTENT")
}

// 请求级带内结果（RequestScoped）：此前尝试残留的上游错误上下文不参与分类、不做上游归因，
// 按业务限制计；透传规则的 skip_monitoring 残留也不影响它落库。
func TestLogOpsStreamError_RequestScopedIgnoresResidualUpstreamContext(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.7-flash:streamGenerateContent", nil)
	setOpsRequestContext(c, "gemini-3.7-flash", true)
	c.Set(service.OpsUpstreamStatusCodeKey, http.StatusTooManyRequests)
	c.Set(service.OpsUpstreamErrorMessageKey, "earlier attempt was rate limited")
	c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
		UpstreamStatusCode: http.StatusTooManyRequests,
		Message:            "earlier attempt was rate limited",
		SkipMonitoring:     true,
	}})

	service.MarkOpsStreamErrorValue(c, service.OpsStreamError{
		ErrType:        "invalid_request_error",
		Code:           "PROHIBITED_CONTENT",
		Message:        "Gemini content policy stop (finishReason=PROHIBITED_CONTENT): Response stopped due to prohibited content.",
		IntendedStatus: http.StatusBadRequest,
		RequestScoped:  true,
	})

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	logOpsStreamError(c, ops, http.StatusOK)

	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	job := <-opsErrorLogQueue
	require.NotNil(t, job.entry)
	require.Equal(t, "invalid_request_error", job.entry.ErrorType)
	require.Equal(t, "request", job.entry.ErrorPhase)
	require.Equal(t, "client", job.entry.ErrorOwner)
	require.Equal(t, "client_request", job.entry.ErrorSource)
	require.True(t, job.entry.IsBusinessLimited)
	require.True(t, job.entry.Stream)
	require.Equal(t, http.StatusBadRequest, job.entry.StatusCode)
	require.Nil(t, job.entry.UpstreamStatusCode, "残留的 429 不得贴到内容策略行上")
	require.Nil(t, job.entry.UpstreamErrorMessage)
	require.Nil(t, job.entry.UpstreamErrorsJSON)
	require.Contains(t, job.entry.ErrorBody, "PROHIBITED_CONTENT")
}

// 请求级带内结果与此前尝试的上游错误并存时，中间件既落带内错误行，也保留恢复行的 provider 遥测。
func TestOpsErrorLoggerMiddleware_RequestScopedInBandErrorKeepsRecoveredTelemetry(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	gin.SetMode(gin.TestMode)

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1beta/models/gemini-3.7-flash:generateContent", func(c *gin.Context) {
		setOpsRequestContext(c, "gemini-3.7-flash", false)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: http.StatusTooManyRequests,
			Message:            "earlier attempt was rate limited",
		}})
		service.MarkOpsStreamErrorValue(c, service.OpsStreamError{
			ErrType:        "invalid_request_error",
			Code:           "SAFETY",
			Message:        "Gemini content policy stop (finishReason=SAFETY): Response stopped due to safety reasons.",
			IntendedStatus: http.StatusBadRequest,
			RequestScoped:  true,
			NonStream:      true,
		})
		c.JSON(http.StatusOK, gin.H{"candidates": []any{}})
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.7-flash:generateContent", nil))
	require.Equal(t, http.StatusOK, recorder.Code)

	require.Equal(t, int64(2), OpsErrorLogQueueLength())
	inBand := <-opsErrorLogQueue
	require.Equal(t, "invalid_request_error", inBand.entry.ErrorType)
	require.Equal(t, "request", inBand.entry.ErrorPhase)
	require.Equal(t, "client", inBand.entry.ErrorOwner)
	require.True(t, inBand.entry.IsBusinessLimited)
	require.False(t, inBand.entry.Stream)
	require.Equal(t, http.StatusBadRequest, inBand.entry.StatusCode)
	require.Nil(t, inBand.entry.UpstreamStatusCode)
	require.Nil(t, inBand.entry.UpstreamErrorsJSON)

	recovered := <-opsErrorLogQueue
	require.Equal(t, "upstream", recovered.entry.ErrorPhase)
	require.Equal(t, "provider", recovered.entry.ErrorOwner)
	require.Equal(t, http.StatusOK, recovered.entry.StatusCode)
	require.Equal(t, "Recovered upstream error 429: earlier attempt was rate limited", recovered.entry.ErrorMessage)
}

// 非请求级带内失败（如上游错误信封）仍与既有语义一致：上游上下文参与分类并做归因，只落一行。
func TestOpsErrorLoggerMiddleware_UpstreamInBandFailureStillSingleRow(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	gin.SetMode(gin.TestMode)

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1beta/models/gemini-3.7-flash:streamGenerateContent", func(c *gin.Context) {
		setOpsRequestContext(c, "gemini-3.7-flash", true)
		service.SetOpsUpstreamError(c, http.StatusTooManyRequests, "quota", "")
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: http.StatusTooManyRequests,
			Message:            "quota",
			Kind:               "stream_failed",
		}})
		service.MarkOpsStreamFailure(c, "rate_limit_error", "RESOURCE_EXHAUSTED", "quota", http.StatusTooManyRequests)
		c.Data(http.StatusOK, "text/event-stream", []byte("data: {\"error\":{\"code\":429}}\n\n"))
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.7-flash:streamGenerateContent", nil))

	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	job := <-opsErrorLogQueue
	require.Equal(t, "rate_limit_error", job.entry.ErrorType)
	require.Equal(t, "upstream", job.entry.ErrorPhase)
	require.Equal(t, "provider", job.entry.ErrorOwner)
	require.Equal(t, http.StatusTooManyRequests, job.entry.StatusCode)
	require.True(t, job.entry.Stream)
	require.NotNil(t, job.entry.UpstreamStatusCode)
	require.Equal(t, http.StatusTooManyRequests, *job.entry.UpstreamStatusCode)
}

// Gemini 带内信号登记使用的错误类型必须都在 ops 白名单里，否则会被归一化成 api_error。
func TestNormalizeOpsErrorType_KeepsGeminiInBandSignalTypes(t *testing.T) {
	for _, errType := range []string{
		"invalid_request_error", "authentication_error", "permission_error",
		"not_found_error", "rate_limit_error", "upstream_error",
	} {
		require.Equal(t, errType, normalizeOpsErrorType(errType, "PROHIBITED_CONTENT"), errType)
	}
}
