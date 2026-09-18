package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// HTTP 200 内的上游失败依照真实观测归属，客户端的错误包装不能覆盖上游状态和消息。
func TestOpsErrorLoggerMiddleware_ObservedStreamFailureKeepsProviderAttribution(t *testing.T) {
	for _, eventOnly := range []bool{false, true} {
		name := "status_context"
		if eventOnly {
			name = "attempt_event"
		}
		t.Run(name, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			gin.SetMode(gin.TestMode)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				setOpsRequestContext(c, "test-model", true)
				if eventOnly {
					c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
						Platform: service.PlatformOpenAI, AccountID: 7, GroupID: 3,
						Kind: "stream_error", UpstreamStatusCode: http.StatusBadGateway,
						Message: "observed upstream stream failure", UpstreamRequestID: "upstream-request",
					}})
				} else {
					service.SetOpsUpstreamError(c, http.StatusBadGateway, "observed upstream stream failure", "")
				}
				c.Data(http.StatusOK, "text/event-stream", []byte("event: error\ndata: {\"type\":\"error\",\"sequence_number\":2,\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"The service is busy. Please retry later.\"}}\n\n"))
			})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Len(t, opsErrorLogQueue, 1)
			entry := (<-opsErrorLogQueue).entry
			require.Equal(t, http.StatusServiceUnavailable, entry.StatusCode, "流内失败继续按语义状态参与失败统计")
			require.Equal(t, "upstream", entry.ErrorPhase)
			require.Equal(t, "provider", entry.ErrorOwner)
			require.Equal(t, "upstream_http", entry.ErrorSource)
			require.False(t, entry.IsBusinessLimited)
			require.Equal(t, "P1", entry.Severity)
			require.NotNil(t, entry.UpstreamStatusCode)
			require.Equal(t, http.StatusBadGateway, *entry.UpstreamStatusCode)
			require.NotNil(t, entry.UpstreamErrorMessage)
			require.Equal(t, "observed upstream stream failure", *entry.UpstreamErrorMessage)
			require.Contains(t, entry.ErrorMessage, "The service is busy")
			if eventOnly {
				require.NotNil(t, entry.UpstreamErrorsJSON)
				events, err := service.ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
				require.NoError(t, err)
				require.Len(t, events, 1)
				require.Equal(t, "upstream-request", events[0].UpstreamRequestID)
			}
		})
	}
}

// 本地或来源未知的流内错误仍应入库，但不能凭错误码伪造上游状态、消息或尝试。
func TestOpsErrorLoggerMiddleware_LocalStreamFailureHasNoUpstreamFacts(t *testing.T) {
	for _, tc := range []struct {
		name, event, errType, code, message, phase, owner string
		status                                            int
		limited                                           bool
	}{
		{"unknown_busy", "error", "server_error", "server_error", "The service is busy. Please retry later.", "internal", "platform", http.StatusServiceUnavailable, false},
		{"local_store_failure", "response.failed", "api_error", "server_error", "Failed to check concurrency", "internal", "platform", http.StatusServiceUnavailable, false},
		{"local_queue_limit", "error", "rate_limit_error", "rate_limit_exceeded", "Too many pending requests in queue", "request", "client", http.StatusTooManyRequests, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			gin.SetMode(gin.TestMode)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				setOpsRequestContext(c, "test-model", true)
				c.SSEvent(tc.event, gin.H{"type": tc.event, "error": gin.H{
					"type": tc.errType, "code": tc.code, "message": tc.message,
				}})
			})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Len(t, opsErrorLogQueue, 1)
			entry := (<-opsErrorLogQueue).entry
			require.Equal(t, tc.status, entry.StatusCode)
			require.Equal(t, tc.phase, entry.ErrorPhase)
			require.Equal(t, tc.owner, entry.ErrorOwner)
			require.Equal(t, tc.limited, entry.IsBusinessLimited)
			require.Nil(t, entry.UpstreamStatusCode)
			require.Nil(t, entry.UpstreamErrorMessage)
			require.Nil(t, entry.UpstreamErrorDetail)
			require.Nil(t, entry.UpstreamErrorsJSON)
		})
	}
}

// 本地请求级终态不能借用之前失败账号的归属；此前真实上游失败仍作为恢复遥测保留。
func TestOpsErrorLoggerMiddleware_RequestScopedTerminalStreamKeepsLocalOrigin(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 3)
	gin.SetMode(gin.TestMode)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		setOpsRequestContext(c, "test-model", true)
		service.SetOpsUpstreamError(c, http.StatusBadGateway, "earlier attempt failed", "")
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			AccountID: 7, UpstreamStatusCode: http.StatusBadGateway, Message: "earlier attempt failed",
		}})
		service.MarkOpsStreamErrorValue(c, service.OpsStreamError{
			ErrType: "invalid_request_error", Code: "invalid_request", Message: "Request policy rejected",
			IntendedStatus: http.StatusBadRequest, RequestScoped: true,
		})
		c.SSEvent("response.failed", gin.H{"type": "response.failed", "response": gin.H{
			"error": gin.H{"code": "invalid_request", "message": "Request policy rejected"},
		}})
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, opsErrorLogQueue, 2)
	terminal := (<-opsErrorLogQueue).entry
	require.Equal(t, http.StatusBadRequest, terminal.StatusCode)
	require.Equal(t, "request", terminal.ErrorPhase)
	require.Equal(t, "client", terminal.ErrorOwner)
	require.True(t, terminal.IsBusinessLimited)
	require.Nil(t, terminal.UpstreamStatusCode)
	require.Nil(t, terminal.UpstreamErrorMessage)
	require.Nil(t, terminal.UpstreamErrorsJSON)
	recovered := (<-opsErrorLogQueue).entry
	require.Equal(t, http.StatusOK, recovered.StatusCode)
	require.Equal(t, "upstream", recovered.ErrorPhase)
	require.Equal(t, "provider", recovered.ErrorOwner)
	require.Equal(t, "Recovered upstream error 502: earlier attempt failed", recovered.ErrorMessage)
}

// 中间尝试失败后完成的请求保留上游遥测；HTTP 200 和恢复标记避免污染最终失败统计。
func TestOpsErrorLoggerMiddleware_BusyRetryRecoveryRemainsOutsideFailureSLA(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)
	repo := &ingressRejectOpsRepo{}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		setOpsRequestContext(c, "test-model", true)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			Platform: service.PlatformOpenAI, AccountID: 7, Kind: "failover",
			UpstreamStatusCode: http.StatusServiceUnavailable,
			Message:            "The service is busy. Please retry later.",
		}})
		c.SSEvent("response.completed", gin.H{"type": "response.completed", "response": gin.H{"status": "completed"}})
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, opsErrorLogQueue, 1)
	job := <-opsErrorLogQueue
	flushOpsErrorLogBatch([]opsErrorLogJob{job})
	require.Len(t, repo.entries, 1)
	entry := repo.entries[0]
	require.Equal(t, http.StatusOK, entry.StatusCode)
	require.Equal(t, "provider", entry.ErrorOwner)
	require.Equal(t, "upstream", entry.ErrorPhase)
	require.Contains(t, entry.ErrorMessage, "Recovered upstream error 503:")
	require.NotNil(t, entry.UpstreamStatusCode)
	require.Equal(t, http.StatusServiceUnavailable, *entry.UpstreamStatusCode)
	require.NotNil(t, entry.UpstreamErrorsJSON)
	row := service.OpsErrorLog{Phase: entry.ErrorPhase, Message: entry.ErrorMessage}
	row.SetClientStatus(entry.StatusCode)
	require.True(t, row.RecoveredUpstream)
}

// 明确跳过监控的最终上游失败保持既有规则，不因新增流内来源判断重复入库。
func TestOpsErrorLoggerMiddleware_ObservedStreamFailureHonorsSkipMonitoring(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: http.StatusServiceUnavailable,
			Message:            "hidden stream failure", SkipMonitoring: true,
		}})
		c.SSEvent("error", gin.H{"type": "error", "error": gin.H{
			"type": "server_error", "code": "server_error", "message": "hidden stream failure",
		}})
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, opsErrorLogQueue)
}
