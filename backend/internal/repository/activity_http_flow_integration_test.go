//go:build integration

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/handler"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 使用真实JWT、HTTP、服务和容器数据库串联完整流程；只以测试订单模拟外部到账，不调用支付渠道。
func TestActivityHTTPAuthenticatedFundsFlow(t *testing.T) {
	repo, uid, cfg := dailyActivityFixture(t)
	ctx := context.Background()
	users := NewUserRepository(integrationEntClient, integrationDB)
	user, err := users.GetByID(ctx, uid)
	require.NoError(t, err)
	admin := mustCreateUser(t, integrationEntClient, &service.User{Role: service.RoleAdmin})
	other := mustCreateUser(t, integrationEntClient, &service.User{})
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM users WHERE id IN ($1,$2)`, admin.ID, other.ID) })
	authConfig := &config.Config{JWT: config.JWTConfig{Secret: strings.Repeat("local-http-audit-", 3), ExpireHour: 1}}
	auth := service.NewAuthService(integrationEntClient, users, nil, nil, authConfig, nil, nil, nil, nil, nil, nil, nil, nil)
	userToken, err := auth.GenerateToken(ctx, user)
	require.NoError(t, err)
	adminToken, err := auth.GenerateToken(ctx, admin)
	require.NoError(t, err)
	otherToken, err := auth.GenerateToken(ctx, other)
	require.NoError(t, err)
	h := handler.NewDailyActivityHandler(service.NewDailyActivityService(repo, nil, nil))
	codes := NewRedeemCodeRepository(integrationEntClient)
	redeem := service.NewRedeemService(codes, users, nil, nil, nil, integrationEntClient, nil, nil)
	rh := handler.NewRedeemHandler(redeem)
	r := gin.New()
	r.Use(gin.HandlerFunc(middleware.NewJWTAuthMiddleware(auth, service.NewUserService(users, nil, nil, nil), nil, nil)))
	r.GET("/api/v1/daily-activities", h.Status)
	r.POST("/api/v1/daily-activities/claim", h.Claim)
	r.GET("/api/v1/daily-activities/history", h.History)
	r.GET("/api/v1/admin/settings/daily-activity", h.GetConfig)
	r.PUT("/api/v1/admin/settings/daily-activity", h.SaveConfig)
	r.POST("/api/v1/redeem", rh.Redeem)
	server := httptest.NewServer(r)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	call := func(method, path, token string, body any) (int, json.RawMessage, error) {
		var input []byte
		if body != nil {
			var e error
			input, e = json.Marshal(body)
			if e != nil {
				return 0, nil, e
			}
		}
		req, e := http.NewRequest(method, server.URL+path, bytes.NewReader(input))
		if e != nil {
			return 0, nil, e
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, e := client.Do(req)
		if e != nil {
			return 0, nil, e
		}
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		if e != nil {
			return resp.StatusCode, nil, e
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if e = json.Unmarshal(raw, &envelope); e != nil {
			return resp.StatusCode, nil, e
		}
		return resp.StatusCode, envelope.Data, nil
	}
	check := func(method, path, token string, body any, status int) json.RawMessage {
		t.Helper()
		got, data, e := call(method, path, token, body)
		require.NoError(t, e)
		require.Equal(t, status, got, path)
		return data
	}
	check("GET", "/api/v1/daily-activities", "", nil, 401)
	check("GET", "/api/v1/daily-activities", userToken+"invalid", nil, 401)
	check("PUT", "/api/v1/admin/settings/daily-activity", userToken, cfg, 403)
	cfg.IncludeRedeemBalance = true
	cfg.IncludeAdminBalance = true
	cfg.DailyGift.Threshold = 1
	cfg.Recharge.Threshold = 1
	cfg.Consumption.Threshold = 1
	check("PUT", "/api/v1/admin/settings/daily-activity", adminToken, cfg, 200)
	// 兑换3，加上外部订阅实付2，共五次充值奖励。外部订单不再次给用户加余额。
	code := &service.RedeemCode{Code: strings.ReplaceAll(uuid.NewString(), "-", ""), Type: service.RedeemTypeBalance, Value: 3, Status: service.StatusUnused, MaxUses: 1}
	require.NoError(t, codes.Create(ctx, code))
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM redeem_codes WHERE id=$1`, code.ID) })
	check("POST", "/api/v1/redeem", userToken, map[string]string{"code": code.Code}, 200)
	_, err = integrationDB.Exec(`INSERT INTO payment_orders(user_id,amount,pay_amount,status,order_type,payment_type,completed_at,expires_at) VALUES($1,1,2,'COMPLETED','subscription','alipay',clock_timestamp(),NOW()+INTERVAL '1 day')`, uid)
	require.NoError(t, err)
	var status service.DailyActivityStatus
	require.NoError(t, json.Unmarshal(check("GET", "/api/v1/daily-activities", userToken, nil, 200), &status))
	require.Equal(t, 5.0, status.Activities[1].EligibleAmount)
	require.Equal(t, int64(5), status.Activities[1].AvailableCount)
	// 不同HTTP请求争抢同一五次资格，只允许五次成功入账。
	var wg sync.WaitGroup
	results := make(chan int, 20)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, e := call("POST", "/api/v1/daily-activities/claim", userToken, service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()})
			results <- code
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	success := 0
	for code := range results {
		if code == 200 {
			success++
		} else {
			require.Equal(t, 409, code)
		}
	}
	require.Equal(t, 5, success)
	input := service.DailyActivityClaimInput{ActivityType: "daily_gift", IdempotencyKey: uuid.NewString()}
	first := check("POST", "/api/v1/daily-activities/claim", userToken, input, 200)
	cfg.DailyGift.Enabled = false
	check("PUT", "/api/v1/admin/settings/daily-activity", adminToken, cfg, 200)
	replay := check("POST", "/api/v1/daily-activities/claim", userToken, input, 200)
	var before, after service.DailyActivityClaimResult
	require.NoError(t, json.Unmarshal(first, &before))
	require.NoError(t, json.Unmarshal(replay, &after))
	require.True(t, after.Replayed)
	require.Equal(t, before.Items, after.Items)
	input.IdempotencyKey = uuid.NewString()
	check("POST", "/api/v1/daily-activities/claim", userToken, input, 403)
	// 非本人无法通过query参数查看奖励，也不能领取另一个人的资格。
	var history service.DailyActivityHistory
	require.NoError(t, json.Unmarshal(check("GET", fmt.Sprintf("/api/v1/daily-activities/history?user_id=%d", uid), otherToken, nil, 200), &history))
	require.Zero(t, history.Total)
	check("POST", "/api/v1/daily-activities/claim", otherToken, service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()}, 409)
	require.NoError(t, json.Unmarshal(check("GET", "/api/v1/daily-activities/history?page=2&page_size=2", userToken, nil, 200), &history))
	require.Equal(t, int64(6), history.Total)
	require.Len(t, history.Items, 2)
	final, err := users.GetByID(ctx, uid)
	require.NoError(t, err)
	require.InDelta(t, 8.6, final.Balance, 1e-8)
	var total, count float64
	require.NoError(t, integrationDB.QueryRow(`SELECT COALESCE(SUM(amount),0),COUNT(*) FROM daily_activity_claims WHERE user_id=$1`, uid).Scan(&total, &count))
	require.InDelta(t, .6, total, 1e-9)
	require.Equal(t, 6.0, count)
}
