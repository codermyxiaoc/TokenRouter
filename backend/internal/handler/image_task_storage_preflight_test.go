package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 用可控存储故障验证生成入口的隔离，不访问真实模型、桶或用户余额。
type asyncPreflightStorage struct {
	checks atomic.Int32
	saves  atomic.Int32
	check  func(context.Context, int32) error
	save   func(context.Context, int32) error
}

func (s *asyncPreflightStorage) CheckReadiness(ctx context.Context) error {
	n := s.checks.Add(1)
	if s.check != nil {
		return s.check(ctx, n)
	}
	return nil
}

func (s *asyncPreflightStorage) Save(ctx context.Context, key, _ string, _ []byte) (string, error) {
	n := s.saves.Add(1)
	if s.save != nil {
		if err := s.save(ctx, n); err != nil {
			return "", err
		}
	}
	return "https://cdn.example.test/" + key, nil
}

func submitPreflightImage(router http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"gpt-image-2.5","prompt":"a red ball"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func newPreflightHandler(storage *asyncPreflightStorage) (*AsyncImageHandler, *asyncImageMemoryStore) {
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	uploader := service.NewImageResultUploader(storage, "images/", 1<<20, nil)
	h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute), nil)
	return h, store
}

// 在任务持久化和网关调用之前拒绝坏桶，不能通过重试造成预占或生成费用。
func TestAsyncImageStoragePreflightRejectsBeforeTaskAndGateway(t *testing.T) {
	storage := &asyncPreflightStorage{check: func(context.Context, int32) error {
		return errors.New("AccessDenied https://private.test/?secret=DO_NOT_EXPOSE")
	}}
	h, store := newPreflightHandler(storage)
	var executions atomic.Int32
	h.execute = func(string, *gin.Context) { executions.Add(1) }
	router := asyncCapacityRouter(h)
	for range 2 {
		w := submitPreflightImage(router)
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		require.Contains(t, w.Body.String(), "image_storage_unavailable")
		require.Contains(t, w.Body.String(), "generation has not started")
		require.NotContains(t, w.Body.String(), "DO_NOT_EXPOSE")
		require.Equal(t, "30", w.Header().Get("Retry-After"))
		require.Empty(t, w.Header().Get("Location"))
		require.Empty(t, h.pending)
	}
	require.Zero(t, executions.Load())
	require.Zero(t, storage.saves.Load())
	require.Empty(t, store.tasks)
}

// 受理之后发生故障时仍要在付费网关之前结束任务，查询不能触发新的探测或生成。
func TestAsyncImageStoragePreflightRechecksBeforeGateway(t *testing.T) {
	storage := &asyncPreflightStorage{check: func(_ context.Context, n int32) error {
		if n > 1 {
			return errors.New("bucket became unavailable")
		}
		return nil
	}}
	h, _ := newPreflightHandler(storage)
	var executions atomic.Int32
	h.execute = func(string, *gin.Context) { executions.Add(1) }
	router := asyncCapacityRouter(h)
	router.GET("/v1/images/tasks/:task_id", h.Get)
	w := submitPreflightImage(router)
	require.Equal(t, http.StatusAccepted, w.Code)
	require.Eventually(t, func() bool { return len(h.pending) == 0 }, 2*time.Second, 5*time.Millisecond)
	require.Zero(t, executions.Load())
	require.Zero(t, storage.saves.Load())
	for range 2 {
		poll := httptest.NewRecorder()
		router.ServeHTTP(poll, httptest.NewRequest(http.MethodGet, w.Header().Get("Location"), nil))
		require.Equal(t, http.StatusOK, poll.Code)
		var task service.ImageTask
		require.NoError(t, json.Unmarshal(poll.Body.Bytes(), &task))
		require.Equal(t, service.ImageTaskStatusFailed, task.Status)
		require.Equal(t, http.StatusServiceUnavailable, task.HTTPStatus)
		require.Contains(t, string(task.Error), "image_storage_unavailable")
	}
	require.EqualValues(t, 2, storage.checks.Load())
}

// 预检只改善存储准入，生成后的上传失败仍不允许重新调用模型或隐式重复结算。
func TestAsyncImageStorageFailureDoesNotReenterGeneration(t *testing.T) {
	for _, failUpload := range []bool{false, true} {
		t.Run(map[bool]string{false: "stored", true: "upload_failed"}[failUpload], func(t *testing.T) {
			storage := &asyncPreflightStorage{}
			if failUpload {
				storage.save = func(context.Context, int32) error { return errors.New("storage denied") }
			}
			h, _ := newPreflightHandler(storage)
			var executions atomic.Int32
			h.execute = func(_ string, c *gin.Context) {
				executions.Add(1)
				c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"b64_json": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j8e0AAAAASUVORK5CYII="}}})
			}
			router := asyncCapacityRouter(h)
			router.GET("/v1/images/tasks/:task_id", h.Get)
			w := submitPreflightImage(router)
			require.Equal(t, http.StatusAccepted, w.Code)
			require.Eventually(t, func() bool { return len(h.pending) == 0 }, 2*time.Second, 5*time.Millisecond)
			poll := httptest.NewRecorder()
			router.ServeHTTP(poll, httptest.NewRequest(http.MethodGet, w.Header().Get("Location"), nil))
			var task service.ImageTask
			require.NoError(t, json.Unmarshal(poll.Body.Bytes(), &task))
			if failUpload {
				require.Equal(t, service.ImageTaskStatusFailed, task.Status)
				require.Empty(t, task.Result)
				require.Contains(t, string(task.Error), "generation usage may already have been billed")
			} else {
				require.Equal(t, service.ImageTaskStatusCompleted, task.Status)
				require.Contains(t, string(task.Result), "https://cdn.example.test/images/")
				require.NotContains(t, string(task.Result), "b64_json")
			}
			require.EqualValues(t, 1, executions.Load())
			require.EqualValues(t, 1, storage.saves.Load())
		})
	}
}

// 无效参数与请求取消不能留下后台任务；坏参数也不应消耗桶探测流量。
func TestAsyncImageStoragePreflightInvalidAndCancelledRequests(t *testing.T) {
	h, store := newPreflightHandler(&asyncPreflightStorage{check: func(ctx context.Context, _ int32) error {
		return ctx.Err()
	}})
	var executions atomic.Int32
	h.execute = func(string, *gin.Context) { executions.Add(1) }
	router := asyncCapacityRouter(h)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"prompt":""}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"prompt":"cat"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Zero(t, executions.Load())
	require.Empty(t, h.pending)
	require.Empty(t, store.tasks)
}
