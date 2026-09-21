//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 固定请求入口便于核对首次、并发和重放请求是否仍使用相同参数。
func callBulkAssignRegression(router *gin.Engine, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

// 屏障模拟领域事务仍在发放，其他同键请求不得进入第二次创建。
type blockingBulkAssignRegressionRepo struct {
	*bulkAssignHandlerRepo
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingBulkAssignRegressionRepo) Create(ctx context.Context, sub *service.UserSubscription) error {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
		return r.bulkAssignHandlerRepo.Create(ctx, sub)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSubscriptionBulkAssignConcurrentRequestsDoNotDuplicateEntitlements(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	repo := &blockingBulkAssignRegressionRepo{
		bulkAssignHandlerRepo: &bulkAssignHandlerRepo{rows: make(map[int64]*service.UserSubscription)},
		entered:               make(chan struct{}), release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(repo.release) }) }
	t.Cleanup(finish)
	h := NewSubscriptionHandler(service.NewSubscriptionService(nil, repo, nil, nil, nil))
	router := gin.New()
	router.POST("/assign", h.BulkAssign)
	body := `{"user_ids":[11,12],"plan_id":7,"validity_days":30}`
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- callBulkAssignRegression(router, body, "concurrent-assign") }()
	select {
	case <-repo.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("首次分配未进入领域创建")
	}
	const concurrentRequests = 32
	responses := make(chan *httptest.ResponseRecorder, concurrentRequests)
	for range concurrentRequests {
		go func() { responses <- callBulkAssignRegression(router, body, "concurrent-assign") }()
	}
	for range concurrentRequests {
		select {
		case response := <-responses:
			require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "IDEMPOTENCY_RESULT_UNCONFIRMED")
		case <-time.After(5 * time.Second):
			t.Fatal("并发同键请求未及时返回")
		}
	}
	finish()
	var committed *httptest.ResponseRecorder
	select {
	case committed = <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("首次分配未完成")
	}
	require.Equal(t, http.StatusOK, committed.Code, committed.Body.String())
	replayed := callBulkAssignRegression(router, body, "concurrent-assign")
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
	require.Equal(t, "true", replayed.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, committed.Body.String(), replayed.Body.String())
	require.Equal(t, 2, repo.creates, "两个用户各发放一次，不能因并发重试多发排队权益")
	require.Len(t, repo.rows, 2)
}

// 按用户失败保留部分成功语义，重试成功结果与重试失败用户必须使用不同的键。
type selectiveBulkAssignRegressionRepo struct {
	*bulkAssignHandlerRepo
	failUser int64
}

func (r *selectiveBulkAssignRegressionRepo) Create(ctx context.Context, sub *service.UserSubscription) error {
	if sub.UserID == r.failUser {
		return errors.New("模拟该用户分配失败")
	}
	return r.bulkAssignHandlerRepo.Create(ctx, sub)
}

func TestSubscriptionBulkAssignPartialResultReplayAndFailedUsersOnly(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	repo := &selectiveBulkAssignRegressionRepo{bulkAssignHandlerRepo: &bulkAssignHandlerRepo{rows: make(map[int64]*service.UserSubscription)}, failUser: 12}
	h := NewSubscriptionHandler(service.NewSubscriptionService(nil, repo, nil, nil, nil))
	router := gin.New()
	router.POST("/assign", h.BulkAssign)
	body := `{"user_ids":[11,12],"plan_id":7,"validity_days":30}`
	first := callBulkAssignRegression(router, body, "partial-result")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Contains(t, first.Body.String(), `"success_count":1`)
	require.Contains(t, first.Body.String(), `"failed_count":1`)
	require.Contains(t, first.Body.String(), `"11":"active"`)
	require.Contains(t, first.Body.String(), `"12":"failed"`)
	require.Equal(t, 1, repo.creates)
	repo.failUser = 0
	replayed := callBulkAssignRegression(router, body, "partial-result")
	require.JSONEq(t, first.Body.String(), replayed.Body.String())
	require.Equal(t, 1, repo.creates)
	completed := callBulkAssignRegression(router, `{"user_ids":[12],"plan_id":7,"validity_days":30}`, "retry-only-failed")
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
	require.Contains(t, completed.Body.String(), `"12":"active"`)
	require.Equal(t, 2, repo.creates)
	require.Len(t, repo.rows, 2)
	for _, sub := range repo.rows {
		require.Equal(t, service.SubscriptionStatusActive, sub.Status, "成功用户不能被重复补发成排队订阅")
	}
}

type subscriptionBulkAuthRegressionRepo struct {
	service.UserRepository
	users map[int64]*service.User
}

func (r *subscriptionBulkAuthRegressionRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	if user := r.users[id]; user != nil {
		return user, nil
	}
	return nil, service.ErrUserNotFound
}

func (r *subscriptionBulkAuthRegressionRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

// 真实管理员认证中间件必须在五个批量业务入口之前拒绝未登录和普通用户。
func TestSubscriptionBulkEndpointsRequireAdminAuthentication(t *testing.T) {
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-bulk-auth-regression", ExpireHour: 1}}
	auth := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	users := &subscriptionBulkAuthRegressionRepo{users: map[int64]*service.User{
		11: {ID: 11, Email: "user@example.invalid", Role: service.RoleUser, Status: service.StatusActive},
		12: {ID: 12, Email: "admin@example.invalid", Role: service.RoleAdmin, Status: service.StatusActive},
	}}
	userToken, err := auth.GenerateToken(context.Background(), users.users[11])
	require.NoError(t, err)
	adminToken, err := auth.GenerateToken(context.Background(), users.users[12])
	require.NoError(t, err)
	router := gin.New()
	group := router.Group("/api/v1/admin/subscriptions")
	group.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, service.NewUserService(users, nil, nil, nil), nil, nil)))
	h := &SubscriptionHandler{}
	for path, handle := range map[string]gin.HandlerFunc{
		"bulk-assign": h.BulkAssign, "bulk-extend": h.BulkExtend, "bulk-reset-quota": h.BulkResetQuota,
		"bulk-revoke": h.BulkRevoke, "bulk-restore": h.BulkRestore,
	} {
		group.POST("/"+path, handle)
	}
	for _, path := range []string{"bulk-assign", "bulk-extend", "bulk-reset-quota", "bulk-revoke", "bulk-restore"} {
		for _, scenario := range []struct {
			name, token string
			status      int
		}{
			{"anonymous", "", http.StatusUnauthorized}, {"ordinary_user", userToken, http.StatusForbidden},
			{"admin_reaches_validation", adminToken, http.StatusBadRequest},
		} {
			t.Run(path+"/"+scenario.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions/"+path, strings.NewReader(`{}`))
				req.Header.Set("Content-Type", "application/json")
				if scenario.token != "" {
					req.Header.Set("Authorization", "Bearer "+scenario.token)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				require.Equal(t, scenario.status, response.Code, response.Body.String())
			})
		}
	}
}

// 新状态批量入口与既有延长、重置共用百条限制，越界请求不得开始修改。
func TestSubscriptionBulkStatusHandlersRejectOverLimit(t *testing.T) {
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	body, err := json.Marshal(map[string]any{"subscription_ids": ids})
	require.NoError(t, err)
	h := &SubscriptionHandler{}
	router := gin.New()
	router.POST("/revoke", h.BulkRevoke)
	router.POST("/restore", h.BulkRestore)
	for _, path := range []string{"revoke", "restore"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/"+path, strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "over-limit")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}
}
