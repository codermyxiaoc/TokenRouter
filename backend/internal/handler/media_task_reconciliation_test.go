package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 用真实服务验证 HTTP 身份与请求解析，替身不模拟任何真实用户扣款。
type mediaSettlementHTTPRepo struct {
	service.MediaTaskRepository
	commands []service.MediaTaskSettlementCommand
	reads    int
	err      error
}

func (r *mediaSettlementHTTPRepo) ResolveMediaTaskBilling(_ context.Context, command service.MediaTaskSettlementCommand) (*service.MediaTaskSettlementResult, error) {
	r.commands = append(r.commands, command)
	return nil, r.err
}
func (r *mediaSettlementHTTPRepo) Get(_ context.Context, actor service.MediaTaskActor, id int64) (*service.MediaTask, error) {
	r.reads++
	return &service.MediaTask{ID: id, Source: "async_image", UserID: 91, APIKeyID: 92, Status: "failed"}, nil
}
func (r *mediaSettlementHTTPRepo) GetMediaTaskBillingReview(context.Context, *service.MediaTask) (*service.MediaTaskBillingReview, error) {
	actor := int64(7)
	return &service.MediaTaskBillingReview{Status: "released", ActorID: &actor, Reason: "已核对失败"}, nil
}
func (r *mediaSettlementHTTPRepo) ListExpiredMediaTaskBilling(context.Context, time.Time, int) ([]int64, error) {
	return nil, nil
}

func mediaSettlementRouter(repo *mediaSettlementHTTPRepo, loggedIn bool, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if loggedIn {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), role)
		}
	})
	router.POST("/api/v1/admin/media-tasks/:id/settlement", NewMediaTaskHandler(service.NewMediaTaskService(repo)).AdminSettleBilling)
	return router
}

func TestMediaTaskReconciliationHTTPAuthorizationAndValidation(t *testing.T) {
	for _, test := range []struct {
		name, role, id, body string
		loggedIn             bool
		code                 int
	}{
		{"无会话", "", "1", `{"action":"release","reason":"核对"}`, false, 401},
		{"普通用户", "user", "1", `{"action":"release","reason":"核对"}`, true, 403},
		{"非法ID", "admin", "bad", `{"action":"release","reason":"核对"}`, true, 400},
		{"非正ID", "admin", "0", `{"action":"release","reason":"核对"}`, true, 400},
		{"损坏JSON", "admin", "1", `{`, true, 400},
		{"空JSON", "admin", "1", `{}`, true, 400},
		{"缺少理由", "admin", "1", `{"action":"charge"}`, true, 400},
		{"空白理由", "admin", "1", `{"action":"release","reason":"  "}`, true, 400},
		{"超长理由", "admin", "1", `{"action":"release","reason":"` + strings.Repeat("字", 501) + `"}`, true, 400},
		{"不能伪造自动过期", "admin", "1", `{"action":"expire","reason":"核对"}`, true, 400},
		{"不能传数字动作", "admin", "1", `{"action":1,"reason":"核对"}`, true, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &mediaSettlementHTTPRepo{}
			router := mediaSettlementRouter(repo, test.loggedIn, test.role)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/media-tasks/"+test.id+"/settlement", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, test.code, response.Code, response.Body.String())
			require.Empty(t, repo.commands)
			require.Zero(t, repo.reads)
		})
	}
}

func TestMediaTaskReconciliationHTTPIgnoresClientFundingAndReturnsFreshState(t *testing.T) {
	repo := &mediaSettlementHTTPRepo{}
	router := mediaSettlementRouter(repo, true, "admin")
	// 额外字段不能改变认证管理员、真正付款人、资金来源或原账本金额。
	body := `{"action":"release","reason":"  已核对失败  ","actor_id":999,"user_id":888,"amount":999999,"held_balance_amount":0,"funding":"balance","failed_before":"2100-01-01"}`
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/media-tasks/5/settlement", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		var envelope struct {
			Data service.MediaTask `json:"data"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		require.EqualValues(t, 91, envelope.Data.UserID)
		require.EqualValues(t, 5, envelope.Data.ID)
		require.Equal(t, "released", envelope.Data.BillingReview.Status)
		require.EqualValues(t, 7, *envelope.Data.BillingReview.ActorID)
	}
	require.Equal(t, 2, repo.reads)
	require.Equal(t, []service.MediaTaskSettlementCommand{
		{TaskID: 5, ActorID: 7, Action: "release", Reason: "已核对失败"},
		{TaskID: 5, ActorID: 7, Action: "release", Reason: "已核对失败"},
	}, repo.commands)
}

func TestMediaTaskReconciliationHTTPConflictIsNotRetried(t *testing.T) {
	repo := &mediaSettlementHTTPRepo{err: service.ErrMediaTaskSettlementConflict}
	router := mediaSettlementRouter(repo, true, "admin")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/media-tasks/5/settlement", strings.NewReader(`{"action":"charge","reason":"核对"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Len(t, repo.commands, 1)
	require.Zero(t, repo.reads)
}
