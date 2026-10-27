package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 记录查询期间的写操作，防止轮询重新保存终态或延长任务有效期。
type pollContractStore struct {
	asyncImageMemoryStore
	saves int
}

func (s *pollContractStore) Save(ctx context.Context, record *service.ImageTaskRecord, ttl time.Duration) error {
	s.saves++
	return s.asyncImageMemoryStore.Save(ctx, record, ttl)
}

// 覆盖真实查询响应和日志采集链路，查询失败也不得进入生成、预占或扣费执行器。
func TestAsyncImagePollContractAndEndpoints(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformGrok} {
		for _, prefix := range []string{"/v1/images/tasks/", "/images/tasks/"} {
			for _, tc := range []struct {
				name       string
				status     string
				httpStatus int
				missing    bool
				wrongKey   bool
				wrongUser  bool
			}{
				{name: "processing", status: service.ImageTaskStatusProcessing},
				{name: "completed", status: service.ImageTaskStatusCompleted, httpStatus: http.StatusOK},
				{name: "rate_limited", status: service.ImageTaskStatusFailed, httpStatus: http.StatusTooManyRequests},
				{name: "timed_out", status: service.ImageTaskStatusFailed, httpStatus: http.StatusRequestTimeout},
				{name: "unconfirmed", status: service.ImageTaskStatusFailed, httpStatus: http.StatusBadGateway},
				{name: "missing_or_cleaned_up", missing: true},
				{name: "other_key", status: service.ImageTaskStatusCompleted, wrongKey: true},
				{name: "other_user", status: service.ImageTaskStatusCompleted, wrongUser: true},
			} {
				t.Run(platform+prefix+tc.name, func(t *testing.T) {
					setupOpsErrorLogTestQueue(t, 4)
					store := &pollContractStore{asyncImageMemoryStore: asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}}
					record := &service.ImageTaskRecord{ID: "imgtask_poll_contract", UserID: 7, APIKeyID: 9, Status: tc.status, HTTPStatus: tc.httpStatus, ExpiresAt: time.Now().Add(time.Hour).Unix()}
					if tc.wrongKey {
						record.APIKeyID++
					}
					if tc.wrongUser {
						record.UserID++
					}
					if tc.status == service.ImageTaskStatusCompleted {
						record.Result = json.RawMessage(`{"data":[{"url":"https://example.test/image.png"}]}`)
					} else if tc.status == service.ImageTaskStatusFailed {
						record.Error = json.RawMessage(`{"type":"api_error","message":"generation failed"}`)
					}
					if !tc.missing {
						store.tasks[record.ID] = record
					}
					tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
					h := NewAsyncImageHandler(tasks, nil)
					executions := 0
					h.SetGatewayExecutor(func(_ string, _ *gin.Context) { executions++ })
					ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
					router := gin.New()
					router.Use(InboundEndpointMiddleware(), OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
						c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 9, UserID: 7, Group: &service.Group{Platform: platform}})
						c.Next()
					})
					router.GET(prefix+":task_id", h.Get)
					for i := 0; i < 3; i++ {
						response := httptest.NewRecorder()
						router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, prefix+record.ID, nil))
						require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
						if tc.missing || tc.wrongKey || tc.wrongUser {
							require.Equal(t, http.StatusNotFound, response.Code)
							require.JSONEq(t, `{"error":{"type":"IMAGE_TASK_NOT_FOUND","code":"IMAGE_TASK_NOT_FOUND","message":"image task not found"}}`, response.Body.String())
							require.Empty(t, response.Header().Get("Retry-After"))
							select {
							case job := <-opsErrorLogQueue:
								require.Equal(t, prefix+record.ID, job.entry.RequestPath)
								require.Equal(t, EndpointImagesTasks, job.entry.InboundEndpoint)
								require.Empty(t, job.entry.UpstreamEndpoint)
								require.Nil(t, job.entry.AccountID)
							default:
								t.Fatal("任务查询 404 应保留本地错误日志")
							}
						} else {
							require.Equal(t, http.StatusOK, response.Code)
							var task service.ImageTask
							require.NoError(t, json.Unmarshal(response.Body.Bytes(), &task))
							require.Equal(t, tc.status, task.Status)
							require.Equal(t, tc.httpStatus, task.HTTPStatus)
							require.Equal(t, record.ExpiresAt, task.ExpiresAt)
							if tc.status == service.ImageTaskStatusProcessing {
								require.Equal(t, "3", response.Header().Get("Retry-After"))
							} else {
								require.Empty(t, response.Header().Get("Retry-After"))
							}
						}
					}
					require.Zero(t, executions, "查询不能进入生成和账务执行链路")
					require.Zero(t, store.saves, "重复查询不能刷新终态和有效期")
				})
			}
		}
	}
}
