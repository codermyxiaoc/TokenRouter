package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 并发提交与查询只替换外部存储及上游执行，验证受理容量、任务完整性和查询的只读边界。
func TestAsyncImageConcurrentAdmissionAndPollingPreserveAcceptedTasks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
	h := NewAsyncImageHandler(tasks, nil)
	finish := make(chan struct{})
	var finishOnce sync.Once
	defer finishOnce.Do(func() { close(finish) })
	var executions, canceledExecutions atomic.Int32
	h.execute = func(_ string, c *gin.Context) {
		sequence := executions.Add(1)
		<-finish
		if c.Request.Context().Err() != nil {
			canceledExecutions.Add(1)
		}
		if sequence%2 == 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "test upstream unavailable"}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/image.png"}}, "usage": gin.H{"input_tokens": 12}})
	}
	// 认证替身只选择测试主体，不读取任何真实 Key 或业务数据库。
	router := gin.New()
	router.Use(func(c *gin.Context) {
		userID, keyID := int64(7), int64(9)
		switch c.GetHeader("X-Test-Identity") {
		case "other-key":
			keyID = 10
		case "other-user":
			userID = 8
		}
		groupID := int64(3)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: keyID, UserID: userID, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true}})
		c.Next()
	})
	router.POST("/v1/images/generations/async", h.Submit)
	router.GET("/v1/images/tasks/:task_id", h.Get)

	const submitted = 96
	responses := make([]*httptest.ResponseRecorder, submitted)
	var ready, completed sync.WaitGroup
	ready.Add(submitted)
	completed.Add(submitted)
	start := make(chan struct{})
	for i := range responses {
		go func(i int) {
			defer completed.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"gpt-image-2","prompt":"test"}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			responses[i] = httptest.NewRecorder()
			ready.Done()
			<-start
			router.ServeHTTP(responses[i], req)
		}(i)
	}
	ready.Wait()
	close(start)
	completed.Wait()

	accepted := make(map[string]struct{})
	rejected := 0
	for _, response := range responses {
		switch response.Code {
		case http.StatusAccepted:
			var task service.ImageTask
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &task))
			require.NotEmpty(t, task.TaskID)
			_, duplicate := accepted[task.TaskID]
			require.False(t, duplicate, "受理任务 ID 不能重复")
			accepted[task.TaskID] = struct{}{}
		case http.StatusServiceUnavailable:
			rejected++
			require.Contains(t, response.Body.String(), "image_task_busy")
			require.Equal(t, "3", response.Header().Get("Retry-After"))
		default:
			t.Fatalf("非预期受理结果：%d %s", response.Code, response.Body.String())
		}
	}
	require.Len(t, accepted, asyncImageMaxPendingTasks)
	require.Equal(t, submitted-asyncImageMaxPendingTasks, rejected)
	store.mu.RLock()
	stored := len(store.tasks)
	store.mu.RUnlock()
	require.Equal(t, len(accepted), stored, "被拒绝的请求不能创建任务，已受理任务不能丢失")
	require.Eventually(t, func() bool { return int(executions.Load()) == len(accepted) }, 2*time.Second, time.Millisecond)

	poll := func(id, identity string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/images/tasks/"+id, nil)
		req.Header.Set("X-Test-Identity", identity)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	for id := range accepted {
		for range 3 {
			response := poll(id, "")
			require.Equal(t, http.StatusOK, response.Code)
			var task service.ImageTask
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &task))
			require.Equal(t, service.ImageTaskStatusProcessing, task.Status)
		}
		for _, identity := range []string{"other-key", "other-user"} {
			require.Equal(t, http.StatusNotFound, poll(id, identity).Code)
		}
	}
	require.EqualValues(t, len(accepted), executions.Load(), "轮询与越权查询不能重新派发生图")

	finishOnce.Do(func() { close(finish) })
	require.Eventually(t, func() bool { return len(h.pending) == 0 }, 3*time.Second, time.Millisecond)
	require.Zero(t, canceledExecutions.Load(), "提交连接取消不能取消后台生成上下文")
	succeeded, failed := 0, 0
	for id := range accepted {
		for repeat := range 3 {
			response := poll(id, "")
			require.Equal(t, http.StatusOK, response.Code, "所有已受理任务均可查询")
			var task service.ImageTask
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &task))
			require.NotNil(t, task.CompletedAt)
			switch task.Status {
			case service.ImageTaskStatusCompleted:
				require.Equal(t, http.StatusOK, task.HTTPStatus)
				require.Contains(t, string(task.Result), `"input_tokens":12`)
				if repeat == 0 {
					succeeded++
				}
			case service.ImageTaskStatusFailed:
				require.Equal(t, http.StatusServiceUnavailable, task.HTTPStatus)
				require.NotEmpty(t, task.Error)
				if repeat == 0 {
					failed++
				}
			default:
				t.Fatalf("已释放容量的任务仍未终结：%s %s", id, task.Status)
			}
		}
	}
	require.Equal(t, asyncImageMaxPendingTasks/2, succeeded)
	require.Equal(t, asyncImageMaxPendingTasks/2, failed)
	require.EqualValues(t, len(accepted), executions.Load(), "终态轮询不能再次执行或重复产生上游成本")
	t.Logf("96 并发提交：%d 受理、%d 拒绝；%d 成功、%d 失败；全部任务可查，反复查询未重复执行", len(accepted), rejected, succeeded, failed)
}
