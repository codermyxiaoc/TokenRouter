package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 容量测试只替换认证快照，确保拒绝发生于正文读取和 Redis 任务创建之前。
func asyncCapacityRouter(h *AsyncImageHandler) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		groupID := int64(3)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 9, UserID: 7, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true}})
		c.Next()
	})
	router.POST("/v1/images/generations/async", h.Submit)
	return router
}

func TestAsyncImageCapacityRejectsWithoutReadingOrCreatingTask(t *testing.T) {
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute), nil)
	h.pending = make(chan struct{}, 1)
	h.pending <- struct{}{}
	reader := &asyncUntrustedBody{}
	w := httptest.NewRecorder()
	asyncCapacityRouter(h).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", reader))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "image_task_busy")
	require.Equal(t, "3", w.Header().Get("Retry-After"))
	require.Zero(t, reader.reads)
	require.Empty(t, store.tasks)
	require.Len(t, h.pending, 1)
}

func TestAsyncImageCapacityReleasedAfterExecution(t *testing.T) {
	for _, outcome := range []string{"success", "upstream_failure", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
			h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute), nil)
			h.pending = make(chan struct{}, 1)
			finish := make(chan struct{})
			h.execute = func(_ string, c *gin.Context) {
				<-finish
				if outcome == "panic" {
					panic("test execution panic")
				}
				if outcome == "upstream_failure" {
					c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "failed"}})
					return
				}
				c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/image.png"}}})
			}
			w := httptest.NewRecorder()
			asyncCapacityRouter(h).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"gpt-image-2","prompt":"cat"}`)))
			require.Equal(t, http.StatusAccepted, w.Code)
			require.Len(t, h.pending, 1)
			close(finish)
			require.Eventually(t, func() bool { return len(h.pending) == 0 }, time.Second, 10*time.Millisecond)
			release, accepted := h.acquirePending()
			require.True(t, accepted)
			release()
		})
	}
}

func TestAsyncImageCapacityReleasedOnInvalidRequest(t *testing.T) {
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute), nil)
	w := httptest.NewRecorder()
	asyncCapacityRouter(h).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, h.pending)
	require.Empty(t, store.tasks)
}
