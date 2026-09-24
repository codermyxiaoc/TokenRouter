package handler

import (
	"context"
	"encoding/base64"
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

// 故障替身只拒绝终态写入，用于复现生成结束时 Redis 短暂不可写的边界。
type asyncDurabilityStore struct {
	asyncImageMemoryStore
	failTerminal  bool
	terminalSaves int
}

func (s *asyncDurabilityStore) Save(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	if task.Status != service.ImageTaskStatusProcessing {
		s.terminalSaves++
		if s.failTerminal {
			s.failTerminal = false
			return errors.New("injected terminal Redis write failure")
		}
	}
	return s.asyncImageMemoryStore.Save(ctx, task, ttl)
}

// 投影替身保留成功写入，故障写入不覆盖已保存状态，模拟独立元数据数据库。
type asyncDurabilityObserver struct {
	attempts int
	failAt   int
	stored   []service.MediaTaskObservation
}

func (o *asyncDurabilityObserver) ObserveMediaTask(_ context.Context, observation service.MediaTaskObservation) error {
	o.attempts++
	if o.attempts == o.failAt {
		return errors.New("injected metadata failure")
	}
	o.stored = append(o.stored, observation)
	return nil
}

type asyncDurabilityImageStorage struct{ attempts int }

func (s *asyncDurabilityImageStorage) Save(context.Context, string, string, []byte) (string, error) {
	s.attempts++
	return "", errors.New("injected S3 PutObject failure")
}

// 手动执行真实后台收尾流程，避免外部网络、真实账本以及等待异步 goroutine。
func runAsyncDurabilityTask(t *testing.T, h *AsyncImageHandler, uploader *service.ImageResultUploader, observer *asyncDurabilityObserver) string {
	t.Helper()
	owner := service.ImageTaskOwner{UserID: 7, APIKeyID: 9}
	task, err := h.tasks.Create(context.Background(), owner)
	require.NoError(t, err)
	observation := service.MediaTaskObservation{Source: "async_image", TaskID: task.ID, MediaType: "image", Platform: service.PlatformOpenAI, Status: "processing", UserID: owner.UserID, APIKeyID: owner.APIKeyID}
	if observer != nil {
		h.SetMediaTaskObserver(observer)
		require.NoError(t, observer.ObserveMediaTask(context.Background(), observation))
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", nil)
	background, recorder, cancel := newAsyncImageContext(c, []byte(`{"model":"gpt-image-2"}`), h.tasks.ExecutionTimeout())
	released := false
	h.run(task.ID, service.PlatformOpenAI, background, recorder, cancel, uploader, observation, func() { released = true })
	require.True(t, released, "所有终态收尾路径都应释放受理容量")
	return task.ID
}

func TestAsyncImageDurabilityStorageFailureReportsGeneratedButUnsaved(t *testing.T) {
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	storage := &asyncDurabilityImageStorage{}
	uploader := service.NewImageResultUploader(storage, "images", 0, nil)
	h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute), nil)
	executions := 0
	h.execute = func(_ string, c *gin.Context) {
		executions++
		// 固定图片字节只供存储测试，不调用真实生图或执行实际扣费。
		b64 := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake-png-payload"))
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"b64_json": b64}}, "usage": gin.H{"output_tokens": 100}})
	}
	observer := &asyncDurabilityObserver{}
	id := runAsyncDurabilityTask(t, h, uploader, observer)
	got, err := h.tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, id)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusFailed, got.Status)
	require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
	require.Contains(t, string(got.Error), "generation usage may already have been billed")
	require.Empty(t, got.Result, "S3 失败不能把大图片退回 Redis")
	require.Equal(t, 1, executions, "存图失败不能重新生成")
	require.Equal(t, 1, storage.attempts)
	require.Len(t, observer.stored, 2)
	require.Equal(t, "failed", observer.stored[1].Status)
	require.Equal(t, "图片生成完成但结果存储失败，生成用量可能已经计费", observer.stored[1].ErrorMessage)
}

func TestAsyncImageDurabilityTransientTerminalWriteFailureRecoversWithoutRegeneration(t *testing.T) {
	for _, upstreamStatus := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(upstreamStatus), func(t *testing.T) {
			store := &asyncDurabilityStore{asyncImageMemoryStore: asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}, failTerminal: true}
			h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute), nil)
			executions := 0
			h.execute = func(_ string, c *gin.Context) {
				executions++
				if upstreamStatus == http.StatusOK {
					c.JSON(upstreamStatus, gin.H{"data": []gin.H{{"url": "https://example.test/image.png"}}})
				} else {
					c.JSON(upstreamStatus, gin.H{"error": gin.H{"message": "upstream failed"}})
				}
			}
			observer := &asyncDurabilityObserver{}
			id := runAsyncDurabilityTask(t, h, nil, observer)
			require.Equal(t, 2, store.terminalSaves)
			// 保存短暂失败后只补写终态，轮询不能再次生成或把终态回退为处理中。
			for i := 0; i < 5; i++ {
				got, err := h.tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, id)
				require.NoError(t, err)
				if upstreamStatus == http.StatusOK {
					require.Equal(t, service.ImageTaskStatusCompleted, got.Status)
					require.NotEmpty(t, got.Result)
					require.Empty(t, got.Error)
				} else {
					require.Equal(t, service.ImageTaskStatusFailed, got.Status)
					require.Empty(t, got.Result)
					require.Contains(t, string(got.Error), "upstream failed")
				}
			}
			require.Equal(t, 2, store.terminalSaves)
			require.Equal(t, 1, executions)
			require.Len(t, observer.stored, 2)
			require.NotEqual(t, "processing", observer.stored[1].Status)
		})
	}
}

func TestAsyncImageDurabilityInitialMetadataFailureRefusesExecution(t *testing.T) {
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute), nil)
	observer := &asyncDurabilityObserver{failAt: 1}
	h.SetMediaTaskObserver(observer)
	executions := 0
	h.execute = func(string, *gin.Context) { executions++ }
	response := httptest.NewRecorder()
	asyncCapacityRouter(h).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"gpt-image-2","prompt":"test"}`)))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Zero(t, executions, "没有任务列表记录时不应调用上游")
	require.Empty(t, observer.stored)
	require.Empty(t, h.pending)
	require.Len(t, store.tasks, 1)
	for _, record := range store.tasks {
		require.Equal(t, service.ImageTaskStatusFailed, record.Status)
		require.Contains(t, string(record.Error), "task index unavailable")
	}
}

// 旧存储适配器仍支持独立观察者；正式持久仓储的列表由同一事务写入，不经过此兼容路径。
func TestAsyncImageDurabilityTerminalMetadataFailureDoesNotUndoResult(t *testing.T) {
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute), nil)
	h.execute = func(_ string, c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/image.png"}}})
	}
	observer := &asyncDurabilityObserver{failAt: 2}
	id := runAsyncDurabilityTask(t, h, nil, observer)
	require.Equal(t, 2, observer.attempts)
	require.Len(t, observer.stored, 1)
	require.Equal(t, "processing", observer.stored[0].Status)
	for i := 0; i < 5; i++ {
		got, err := h.tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, id)
		require.NoError(t, err)
		require.Equal(t, service.ImageTaskStatusCompleted, got.Status)
		require.Equal(t, "https://example.test/image.png", got.ImageURL)
	}
	require.Equal(t, 2, observer.attempts, "普通轮询不会修复元数据列表滞后的终态")
}

func TestAsyncImageDurabilityExecutionDeadlineRecordsFailure(t *testing.T) {
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Millisecond), nil)
	h.execute = func(_ string, c *gin.Context) { <-c.Request.Context().Done() }
	observer := &asyncDurabilityObserver{}
	id := runAsyncDurabilityTask(t, h, nil, observer)
	got, err := h.tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, id)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusFailed, got.Status)
	require.Equal(t, http.StatusGatewayTimeout, got.HTTPStatus)
	var taskError map[string]string
	require.NoError(t, json.Unmarshal(got.Error, &taskError))
	require.Equal(t, "timeout_error", taskError["type"])
	require.Equal(t, "图片生成任务超时", observer.stored[1].ErrorMessage)
}

// 异步入口提前拒绝空提示词，不改动同步解析器的默认模型和历史校验语义。
func TestAsyncImageDurabilityEmptyObjectUsesSynchronousValidation(t *testing.T) {
	gateway := &service.OpenAIGatewayService{}
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	h := NewAsyncImageHandler(service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute), &OpenAIGatewayHandler{gatewayService: gateway})
	executions := 0
	h.SetGatewayExecutor(func(string, *gin.Context) { executions++ })
	for _, path := range []string{"/v1/images/generations", "/v1/images/generations/async"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		c.Request.Header.Set("Content-Type", "application/json")
		parsed, err := gateway.ParseOpenAIImagesRequestForRouting(c, []byte(`{}`))
		require.NoError(t, err)
		require.Equal(t, "gpt-image-2", parsed.Model)
		require.Empty(t, parsed.Prompt)
		require.ErrorContains(t, h.validateRequest(c, service.PlatformOpenAI, []byte(`{}`)), "prompt is required")
	}
	response := httptest.NewRecorder()
	asyncCapacityRouter(h).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "prompt is required")
	require.Zero(t, executions)
	require.Empty(t, store.tasks)
	require.Empty(t, h.pending)
}
