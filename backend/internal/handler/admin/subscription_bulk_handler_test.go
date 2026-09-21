package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 参数与幂等键必须在进入领域服务前验证，错误输入不能触碰订阅。
func TestSubscriptionBulkHandlersRejectInvalidRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &SubscriptionHandler{}
	router := gin.New()
	router.POST("/extend", h.BulkExtend)
	router.POST("/reset", h.BulkResetQuota)
	router.POST("/revoke", h.BulkRevoke)
	router.POST("/restore", h.BulkRestore)
	router.POST("/assign", h.BulkAssign)
	for _, tt := range []struct{ path, body, key string }{
		{"/extend", `{"subscription_ids":[],"days":1}`, "key"},
		{"/extend", `{"subscription_ids":[0],"days":1}`, "key"},
		{"/extend", `{"subscription_ids":[1],"days":0}`, "key"},
		{"/extend", `{"subscription_ids":[1],"days":1.5}`, "key"},
		{"/extend", `{"subscription_ids":[1],"days":36501}`, "key"},
		{"/extend", `{"subscription_ids":[1],"days":1}`, ""},
		{"/reset", `{"subscription_ids":[1]}`, "key"},
		{"/reset", `{"subscription_ids":[1],"daily":true}`, ""},
		{"/revoke", `{"subscription_ids":[]}`, "key"},
		{"/restore", `{"subscription_ids":[-1]}`, "key"},
		{"/revoke", `{"subscription_ids":[1]}`, ""},
		{"/restore", `{"subscription_ids":[1]}`, " "},
		{"/assign", `{"user_ids":[0],"plan_id":1}`, "key"},
		{"/assign", `{"user_ids":[1],"plan_id":-1}`, "key"},
	} {
		req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", tt.key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, tt.body)
	}
}

// 测试仓储记录真实领域分配调用数，确保幂等重放不会重复发放排队权益。
type bulkAssignHandlerRepo struct {
	service.UserSubscriptionRepository
	rows       map[int64]*service.UserSubscription
	creates    int
	failCreate bool
}

func (r *bulkAssignHandlerRepo) GetLatestByUserIDAndPlanID(_ context.Context, userID, planID int64) (*service.UserSubscription, error) {
	var latest *service.UserSubscription
	for _, sub := range r.rows {
		if sub.UserID == userID && sub.PlanID == planID && (latest == nil || latest.ExpiresAt.Before(sub.ExpiresAt)) {
			latest = sub
		}
	}
	if latest == nil {
		return nil, service.ErrSubscriptionNotFound
	}
	return latest, nil
}

func (r *bulkAssignHandlerRepo) Create(_ context.Context, sub *service.UserSubscription) error {
	r.creates++
	if r.failCreate {
		return errors.New(strings.Repeat("模拟较长上游错误", 1024))
	}
	sub.ID = int64(r.creates)
	r.rows[sub.ID] = sub
	return nil
}

func (r *bulkAssignHandlerRepo) GetByID(_ context.Context, id int64) (*service.UserSubscription, error) {
	return r.rows[id], nil
}

func TestSubscriptionBulkAssignHandlerIdempotentReplay(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	repo := &bulkAssignHandlerRepo{rows: make(map[int64]*service.UserSubscription)}
	h := NewSubscriptionHandler(service.NewSubscriptionService(nil, repo, nil, nil, nil))
	router := gin.New()
	router.POST("/assign", h.BulkAssign)
	call := func(body, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	body := `{"user_ids":[11,12],"plan_id":7,"validity_days":30}`
	first := call(body, "bulk-assign")
	second := call(body, "bulk-assign")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.JSONEq(t, first.Body.String(), second.Body.String())
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 2, repo.creates)
	conflict := call(`{"user_ids":[13],"plan_id":7,"validity_days":30}`, "bulk-assign")
	require.Equal(t, http.StatusConflict, conflict.Code)
	require.Equal(t, 2, repo.creates)
	// 旧调用不带幂等键仍可合法分配，沿用同套餐排队流程。
	legacy := call(body, "")
	require.Equal(t, http.StatusOK, legacy.Code)
	require.Equal(t, 4, repo.creates)
	require.Equal(t, service.SubscriptionStatusPending, repo.rows[3].Status)
	require.True(t, repo.rows[3].StartsAt.After(time.Now()))
}

func TestSubscriptionBulkStatusHandlerReplayAndStoreFailure(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	for _, scope := range []string{"admin.subscriptions.bulk_revoke", "admin.subscriptions.bulk_restore"} {
		t.Run(scope, func(t *testing.T) {
			service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
			h := &SubscriptionHandler{}
			calls := 0
			router := gin.New()
			router.POST("/status", func(c *gin.Context) {
				h.mutateBulkStatus(c, scope, func(_ context.Context, ids []int64) (*service.BulkSubscriptionResult, error) {
					calls++
					return &service.BulkSubscriptionResult{SubscriptionIDs: ids, UpdatedCount: len(ids)}, nil
				})
			})
			call := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/status", strings.NewReader(`{"subscription_ids":[2,1]}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "bulk-status")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				return rec
			}
			first, replay := call(), call()
			require.Equal(t, http.StatusOK, first.Code)
			require.JSONEq(t, first.Body.String(), replay.Body.String())
			require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
			require.Equal(t, 1, calls)
			service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(storeUnavailableRepoStub{}, service.DefaultIdempotencyConfig()))
			failed := call()
			require.Equal(t, http.StatusServiceUnavailable, failed.Code)
			require.Equal(t, 1, calls, "协调器不可用不能绕过幂等直接变更权益")
		})
	}
}

func TestSubscriptionBulkAssignRejectsOverLimit(t *testing.T) {
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	body, err := json.Marshal(map[string]any{"user_ids": ids, "plan_id": 1})
	require.NoError(t, err)
	router := gin.New()
	h := &SubscriptionHandler{}
	router.POST("/assign", h.BulkAssign)
	req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// 已完成领域分配但确认结果写入失败时，重试只能提示核对，不能再多发一份订阅。
func TestSubscriptionBulkAssignHandlerUnconfirmedDoesNotReassign(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	repo := &bulkAssignHandlerRepo{rows: make(map[int64]*service.UserSubscription)}
	h := NewSubscriptionHandler(service.NewSubscriptionService(nil, repo, nil, nil, nil))
	router := gin.New()
	router.POST("/assign", h.BulkAssign)
	store := &bulkAssignFailConfirmationRepo{newMemoryIdempotencyRepoStub()}
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(store, service.DefaultIdempotencyConfig()))
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(`{"user_ids":[11],"plan_id":7}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "assign-uncertain")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < 2; i++ {
		got := call()
		require.Equal(t, http.StatusConflict, got.Code, got.Body.String())
		require.Contains(t, got.Body.String(), "IDEMPOTENCY_RESULT_UNCONFIRMED")
	}
	require.Equal(t, 1, repo.creates)
	service.SetDefaultIdempotencyCoordinator(nil)
	got := call()
	require.Equal(t, http.StatusServiceUnavailable, got.Code)
	require.Equal(t, 1, repo.creates)
}

type bulkAssignFailConfirmationRepo struct{ *memoryIdempotencyRepoStub }

func (r *bulkAssignFailConfirmationRepo) MarkSucceeded(context.Context, int64, int, string, time.Time) error {
	return service.ErrIdempotencyStoreUnavail
}

// 100 项完整订阅和超长逐项错误均只保存摘要，确保首次与幂等重放都是有效 JSON。
func TestSubscriptionBulkAssignHandlerLargeSummaryCanReplay(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "long_errors"}[fail], func(t *testing.T) {
			service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
			repo := &bulkAssignHandlerRepo{rows: make(map[int64]*service.UserSubscription), failCreate: fail}
			h := NewSubscriptionHandler(service.NewSubscriptionService(nil, repo, nil, nil, nil))
			router := gin.New()
			router.POST("/assign", h.BulkAssign)
			ids := make([]int64, 100)
			for i := range ids {
				ids[i] = int64(i + 1)
			}
			body, err := json.Marshal(map[string]any{"user_ids": ids, "plan_id": 7, "notes": strings.Repeat("备注", 2048)})
			require.NoError(t, err)
			call := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "large-bulk")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				return rec
			}
			first, replay := call(), call()
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
			require.JSONEq(t, first.Body.String(), replay.Body.String())
			require.Less(t, first.Body.Len(), 64*1024)
			require.Contains(t, first.Body.String(), `"subscriptions":[]`)
			require.Equal(t, 100, repo.creates)
		})
	}
}
