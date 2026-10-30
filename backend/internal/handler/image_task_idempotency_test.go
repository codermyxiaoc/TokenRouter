package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// asyncImageIdempotentStore 在内存存储上增加幂等键索引，模拟数据库唯一约束。
type asyncImageIdempotentStore struct {
	asyncImageMemoryStore
}

func (s *asyncImageIdempotentStore) Save(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	s.mu.Lock()
	for id, existing := range s.tasks {
		if id != task.ID && task.IdempotencyKey != "" && existing.APIKeyID == task.APIKeyID && existing.IdempotencyKey == task.IdempotencyKey {
			s.mu.Unlock()
			return service.ErrImageTaskDuplicate
		}
	}
	s.mu.Unlock()
	return s.asyncImageMemoryStore.Save(ctx, task, ttl)
}

func (s *asyncImageIdempotentStore) FindIdempotent(_ context.Context, apiKeyID int64, key string) (*service.ImageTaskRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, task := range s.tasks {
		if task.APIKeyID == apiKeyID && task.IdempotencyKey == key {
			copy := *task
			return &copy, nil
		}
	}
	return nil, service.ErrImageTaskNotFound
}

func newIdempotentAsyncImageRouter(t *testing.T, store service.ImageTaskStore) (*gin.Engine, *atomic.Int32) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
	h := NewAsyncImageHandler(tasks, nil)
	router := gin.New()
	router.Use(middleware2.ClientRequestID(), h.CaptureRequest, func(c *gin.Context) {
		groupID := int64(3)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 9, UserID: 7, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true}})
		c.Next()
	})
	var executions atomic.Int32
	router.POST("/v1/images/generations/async", h.Submit)
	router.POST("/v1/images/generations", func(c *gin.Context) {
		executions.Add(1)
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/image.png"}}})
	})
	h.SetGatewayExecutor(func(_ string, c *gin.Context) { router.ServeHTTP(c.Writer, c.Request) })
	return router, &executions
}

func submitAsyncImage(router *gin.Engine, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

// 同一幂等键的重试返回原任务且只执行一次；同键不同内容返回 409；超长幂等键拒绝。
func TestAsyncImageIdempotentSubmitReplaysOriginalTask(t *testing.T) {
	store := &asyncImageIdempotentStore{asyncImageMemoryStore{tasks: map[string]*service.ImageTaskRecord{}}}
	router, executions := newIdempotentAsyncImageRouter(t, store)
	body := `{"model":"gpt-image-2","prompt":"cat"}`
	first := submitAsyncImage(router, "retry-1", body)
	require.Equal(t, http.StatusAccepted, first.Code)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &created))
	require.Eventually(t, func() bool { return executions.Load() == 1 }, time.Second, 10*time.Millisecond)

	again := submitAsyncImage(router, "retry-1", body)
	require.Equal(t, http.StatusAccepted, again.Code)
	require.Equal(t, "true", again.Header().Get("Idempotent-Replayed"))
	var replay struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(again.Body.Bytes(), &replay))
	require.Equal(t, created.ID, replay.ID)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), executions.Load(), "重放不能再次生成")

	conflict := submitAsyncImage(router, "retry-1", `{"model":"gpt-image-2","prompt":"dog"}`)
	require.Equal(t, http.StatusConflict, conflict.Code)
	require.Contains(t, conflict.Body.String(), "IMAGE_TASK_IDEMPOTENCY_CONFLICT")

	tooLong := submitAsyncImage(router, strings.Repeat("k", 129), body)
	require.Equal(t, http.StatusBadRequest, tooLong.Code)

	// 不带幂等键的同内容请求仍创建新任务。
	plain := submitAsyncImage(router, "", body)
	require.Equal(t, http.StatusAccepted, plain.Code)
	require.Empty(t, plain.Header().Get("Idempotent-Replayed"))
	require.Eventually(t, func() bool { return executions.Load() == 2 }, time.Second, 10*time.Millisecond)
}

// 仓储不支持幂等查询时不能静默忽略幂等键，否则客户端重试会重复生成和扣费。
func TestAsyncImageIdempotencyRequiresSupportingStore(t *testing.T) {
	router, executions := newIdempotentAsyncImageRouter(t, &asyncImageMemoryStore{tasks: map[string]*service.ImageTaskRecord{}})
	response := submitAsyncImage(router, "retry-1", `{"model":"gpt-image-2","prompt":"cat"}`)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Zero(t, executions.Load())
}

// multipart 每次的 boundary 随机生成，指纹只取字段内容；字段或文件内容不同则指纹不同，生成与编辑接口也不同。
func TestAsyncImagePayloadHashIgnoresMultipartBoundary(t *testing.T) {
	build := func(prompt string, image []byte) (string, []byte) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("prompt", prompt))
		part, err := writer.CreateFormFile("image", "a.png")
		require.NoError(t, err)
		_, _ = part.Write(image)
		require.NoError(t, writer.Close())
		return writer.FormDataContentType(), buf.Bytes()
	}
	typeA, bodyA := build("cat", []byte("png-1"))
	typeB, bodyB := build("cat", []byte("png-1"))
	require.NotEqual(t, typeA, typeB, "两次请求的 boundary 不同")
	hashA, err := asyncImagePayloadHash("/v1/images/edits/async", typeA, bodyA)
	require.NoError(t, err)
	hashB, err := asyncImagePayloadHash("/v1/images/edits/async", typeB, bodyB)
	require.NoError(t, err)
	require.Equal(t, hashA, hashB)
	typeC, bodyC := build("cat", []byte("png-2"))
	hashC, err := asyncImagePayloadHash("/v1/images/edits/async", typeC, bodyC)
	require.NoError(t, err)
	require.NotEqual(t, hashA, hashC)
	jsonEdit, err := asyncImagePayloadHash("/v1/images/edits/async", "application/json", []byte(`{"prompt":"cat"}`))
	require.NoError(t, err)
	jsonGenerate, err := asyncImagePayloadHash("/v1/images/generations/async", "application/json", []byte(`{"prompt":"cat"}`))
	require.NoError(t, err)
	require.NotEqual(t, jsonEdit, jsonGenerate)
}
