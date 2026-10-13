package handler

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dailyActivityHandlerStub struct {
	dailyActivityService
	userID int64
	calls  int
}

func (s *dailyActivityHandlerStub) Claim(_ context.Context, uid int64, _ service.DailyActivityClaimInput) (*service.DailyActivityClaimResult, error) {
	s.calls++
	s.userID = uid
	return &service.DailyActivityClaimResult{}, nil
}
func (s *dailyActivityHandlerStub) History(_ context.Context, uid int64, _ string, _, _ int) (*service.DailyActivityHistory, error) {
	s.calls++
	s.userID = uid
	return &service.DailyActivityHistory{Items: []service.DailyActivityRecord{}}, nil
}
func (s *dailyActivityHandlerStub) GetConfig(context.Context) (*service.DailyActivityConfig, error) {
	s.calls++
	c := service.DefaultDailyActivityConfig()
	return &c, nil
}

// 接口必须在调用资金服务前拒绝越权、伪造字段、超大请求和非法分页。
func TestDailyActivityHandlerBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, role, method, path, body string
		status, calls                  int
	}{
		{"匿名领取", "", "POST", "/claim", `{}`, 401, 0},
		{"匿名历史", "", "GET", "/history", ``, 401, 0},
		{"用户读取管理配置", "user", "GET", "/config", ``, 403, 0},
		{"用户保存管理配置", "user", "PUT", "/config", `{}`, 403, 0},
		{"管理员读取配置", "admin", "GET", "/config", ``, 200, 1},
		{"伪造用户", "user", "POST", "/claim", `{"user_id":99}`, 400, 0},
		{"伪造金额", "user", "POST", "/claim", `{"amount":100}`, 400, 0},
		{"拼接JSON", "user", "POST", "/claim", `{} {}`, 400, 0},
		{"超大请求", "user", "POST", "/claim", `{"idempotency_key":"` + strings.Repeat("a", 9000) + `"}`, 400, 0},
		{"分页溢出", "user", "GET", "/history?page=999999999999999999999999", ``, 400, 0},
		{"分页为负", "user", "GET", "/history?page=-1", ``, 400, 0},
		{"单页过大", "user", "GET", "/history?page_size=101", ``, 400, 0},
		{"合法领取取会话身份", "user", "POST", "/claim", `{"activity_type":"recharge","all":false,"idempotency_key":"0123456789abcdef"}`, 200, 1},
		{"管理员也只查自己", "admin", "GET", "/history?user_id=99", ``, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &dailyActivityHandlerStub{}
			h := &DailyActivityHandler{svc: s}
			r := gin.New()
			r.Use(func(c *gin.Context) {
				if tc.role != "" {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
					c.Set(string(middleware.ContextKeyUserRole), tc.role)
				}
			})
			r.POST("/claim", h.Claim)
			r.GET("/history", h.History)
			r.GET("/config", h.GetConfig)
			r.PUT("/config", h.SaveConfig)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, tc.calls, s.calls)
			if s.userID != 0 {
				require.Equal(t, int64(7), s.userID)
			}
		})
	}
}
