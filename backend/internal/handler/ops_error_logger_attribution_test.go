package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKeyPrefix(t *testing.T) {
	if got := keyPrefix("sk-3f2a9c7e", 8); got != "sk-3f2a9" {
		t.Errorf("keyPrefix=%q want %q", got, "sk-3f2a9")
	}
	if got := keyPrefix("abc", 8); got != "abc" {
		t.Errorf("short key should be returned as-is, got %q", got)
	}
}

// 恢复成功的账号和端点不能覆盖真正失败的分组；多个失败尝试仍完整保留供详情排障。
func TestOpsErrorLoggerMiddleware_RecoveredUsesFailedAttemptAttribution(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		finalGroup := &service.Group{ID: 7, Name: "test3", Platform: service.PlatformAnthropic}
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 1, GroupID: &finalGroup.ID, Group: finalGroup})
		c.Set(opsAccountIDKey, int64(20))
		c.Set(opsModelKey, "client-model")
		c.Set(opsUpstreamModelKey, "successful-model")
		setActualUpstreamEndpoint(c, "/v1/messages")
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{
			{GroupID: 3, GroupName: "test1", AccountID: 22, Platform: service.PlatformOpenAI, UpstreamEndpoint: "/v1/chat/completions", UpstreamModel: "failed-model-1", UpstreamStatusCode: 503, Message: "first error"},
			{GroupID: 5, GroupName: "test2", AccountID: 19, Platform: service.PlatformOpenAI, UpstreamEndpoint: "/backend-api/codex/responses", UpstreamModel: "failed-model-2", UpstreamStatusCode: 429, Message: "second error"},
		})
		service.MarkOpsRecoveredGroup(c, finalGroup)
		c.JSON(http.StatusOK, gin.H{"status": "completed"})
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Len(t, opsErrorLogQueue, 1)
	entry := (<-opsErrorLogQueue).entry
	require.Equal(t, http.StatusOK, entry.StatusCode)
	require.Equal(t, int64(5), *entry.GroupID)
	require.Equal(t, int64(19), *entry.AccountID)
	require.Equal(t, service.PlatformOpenAI, entry.Platform)
	require.Equal(t, "/backend-api/codex/responses", entry.UpstreamEndpoint)
	require.Equal(t, "failed-model-2", entry.UpstreamModel)
	require.Equal(t, "client-model", entry.RequestedModel)
	require.Equal(t, "/v1/responses", entry.InboundEndpoint)
	require.Equal(t, "Recovered upstream error 429: second error", entry.ErrorMessage)
	events, err := service.ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, int64(3), events[0].GroupID)
	require.Equal(t, "/v1/chat/completions", events[0].UpstreamEndpoint)
	for _, event := range events {
		require.Equal(t, int64(7), event.RecoveredGroupID)
		require.Equal(t, "test3", event.RecoveredGroupName)
		require.Equal(t, service.PlatformAnthropic, event.RecoveredPlatform)
	}
}

// 老事件没有归属快照时维持原回退；新事件跨组且端点未知时不得借用成功组的端点。
func TestApplyOpsFailedAttemptAttributionCompatibility(t *testing.T) {
	for _, test := range []struct {
		name         string
		phase        string
		eventGroupID int64
		wantGroupID  int64
		wantEndpoint string
	}{
		{"legacy_event", "upstream", 0, 5, "/v1/messages"},
		{"same_group_unknown_endpoint", "upstream", 5, 5, "/v1/messages"},
		{"cross_group_unknown_endpoint", "upstream", 3, 3, ""},
		{"local_failure", "routing", 3, 5, "/v1/messages"},
	} {
		t.Run(test.name, func(t *testing.T) {
			groupID := int64(5)
			entry := &service.OpsInsertErrorLogInput{GroupID: &groupID, ErrorPhase: test.phase, UpstreamEndpoint: "/v1/messages", UpstreamErrors: []*service.OpsUpstreamErrorEvent{{GroupID: test.eventGroupID, UpstreamStatusCode: 503}}}
			applyOpsFailedAttemptAttribution(entry)
			require.Equal(t, test.wantGroupID, *entry.GroupID)
			require.Equal(t, test.wantEndpoint, entry.UpstreamEndpoint)
		})
	}
}

// 最终失败来自另一账号且没有尝试事件时，早前已恢复的事件不能覆盖当前失败归属。
func TestApplyOpsFailedAttemptAttributionKeepsCurrentFailureAccount(t *testing.T) {
	groupID, accountID := int64(5), int64(19)
	entry := &service.OpsInsertErrorLogInput{
		GroupID: &groupID, AccountID: &accountID, StatusCode: http.StatusBadGateway,
		ErrorPhase: "upstream", UpstreamEndpoint: "/v1/responses",
		UpstreamErrors: []*service.OpsUpstreamErrorEvent{{GroupID: 3, AccountID: 22, UpstreamEndpoint: "/v1/chat/completions", UpstreamStatusCode: 503}},
	}
	applyOpsFailedAttemptAttribution(entry)
	require.Equal(t, int64(5), *entry.GroupID)
	require.Equal(t, int64(19), *entry.AccountID)
	require.Equal(t, "/v1/responses", entry.UpstreamEndpoint)
}

// 多轮 WebSocket 在连接结束后集中落库，每轮只能读取自己的失败快照。
func TestLogOpsStreamError_WebSocketAttemptAttributionUsesTurnSnapshot(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	service.SetOpenAIClientTransport(c, service.OpenAIClientTransportWS)
	events := []*service.OpsUpstreamErrorEvent{
		{GroupID: 3, AccountID: 22, Platform: service.PlatformOpenAI, UpstreamEndpoint: "/v1/chat/completions", UpstreamModel: "model-one", UpstreamStatusCode: 503, Message: "first turn failure"},
		{GroupID: 5, AccountID: 19, Platform: service.PlatformOpenAI, UpstreamEndpoint: "/v1/responses", UpstreamModel: "model-two", UpstreamStatusCode: 429, Message: "second turn failure"},
	}
	for index, event := range events {
		service.BeginOpsStreamTurn(c, index+1)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.AccountID, event.AccountID))
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{event})
		service.SetOpsUpstreamModel(c, event.UpstreamModel)
		service.SetOpsUpstreamError(c, event.UpstreamStatusCode, event.Message, "")
		service.MarkOpsStreamFailure(c, "upstream_error", "server_error", event.Message, event.UpstreamStatusCode)
	}
	// 第三轮成功使用另一账号；前两轮不能借用它的归属或合并彼此的尝试事件。
	service.BeginOpsStreamTurn(c, 3)
	finalGroup := &service.Group{ID: 7, Platform: service.PlatformAnthropic}
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 1, GroupID: &finalGroup.ID, Group: finalGroup})
	c.Set(opsAccountIDKey, int64(20))
	service.SetOpsUpstreamModel(c, "final-model")
	setActualUpstreamEndpoint(c, "/v1/messages")
	logOpsStreamError(c, service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), http.StatusSwitchingProtocols)
	require.Len(t, opsErrorLogQueue, 2)
	for _, event := range events {
		entry := (<-opsErrorLogQueue).entry
		require.Equal(t, event.GroupID, *entry.GroupID)
		require.Equal(t, event.AccountID, *entry.AccountID)
		require.Equal(t, event.UpstreamEndpoint, entry.UpstreamEndpoint)
		require.Equal(t, event.UpstreamModel, entry.UpstreamModel)
		require.Equal(t, event.UpstreamStatusCode, entry.StatusCode)
		require.Equal(t, event.UpstreamStatusCode, *entry.UpstreamStatusCode)
		storedEvents, err := service.ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
		require.NoError(t, err)
		require.Len(t, storedEvents, 1)
		require.Equal(t, event.AccountID, storedEvents[0].AccountID)
	}
}
