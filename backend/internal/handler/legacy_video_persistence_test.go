//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	middleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 持久存储桩只接受原任务身份，方便检查响应提交与保存的先后关系。
type legacyVideoHandlerStore struct {
	service.LegacyVideoTaskRepository
	task         *service.LegacyVideoTask
	checkErr     error
	saveErrors   int
	saves        int
	onSave       func(context.Context, *service.LegacyVideoTask)
	deletions    int
	deleteErrors int
	onDelete     func(context.Context, *service.LegacyVideoTask)
}

func (s *legacyVideoHandlerStore) MarkUpstreamDeleted(ctx context.Context, task *service.LegacyVideoTask) error {
	s.deletions++
	if s.onDelete != nil {
		s.onDelete(ctx, task)
	}
	if s.deletions <= s.deleteErrors {
		return errors.New("删除事实暂时不可写")
	}
	now := time.Now()
	s.task.UpstreamDeletedAt = &now
	return nil
}

func (s *legacyVideoHandlerStore) Save(ctx context.Context, task *service.LegacyVideoTask) error {
	s.saves++
	if s.onSave != nil {
		s.onSave(ctx, task)
	}
	if s.saves <= s.saveErrors {
		return errors.New("任务存储暂时不可用")
	}
	copy := *task
	s.task = &copy
	return nil
}

func (s *legacyVideoHandlerStore) Get(_ context.Context, source, taskID string, userID, keyID int64) (*service.LegacyVideoTask, error) {
	if taskID == "storage-check" {
		if s.checkErr != nil {
			return nil, s.checkErr
		}
		return nil, service.ErrLegacyVideoTaskNotFound
	}
	if task := s.task; task != nil && task.Source == source && task.TaskID == taskID && task.APIKeyID == keyID && (task.UserID == userID || task.BillingUserID == userID) {
		return task, nil
	}
	return nil, service.ErrLegacyVideoTaskNotFound
}

// 两种旧视频协议共用资金与归属持久化，但仍返回各自原生任务格式。
func legacyVideoHandlerContext(ctx context.Context, seedance, create bool) (*gin.Context, *httptest.ResponseRecorder) {
	c, w := grokMediaSlotContext(ctx, create)
	if !seedance {
		return c, w
	}
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformOpenAI
	method, body := http.MethodGet, ""
	if create {
		method, body = http.MethodPost, `{"model":"seedance","content":[{"type":"text","text":"waves"}]}`
	}
	c.Request = httptest.NewRequest(method, "/api/v3/contents/generations/tasks", strings.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "task_id", Value: "task"}}
	return c, w
}

// 已受理的生成只允许重试本地保存；任何存储或响应异常均不得再次请求生成接口。
func TestLegacyVideoHandlerCreationPersistence(t *testing.T) {
	for _, provider := range []string{"grok", "seedance"} {
		for _, scenario := range []string{"success", "cache unavailable", "save retry", "save unavailable", "preflight unavailable", "client cancelled", "missing task id", "transport error"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				seedance := provider == "seedance"
				var platforms []string
				if seedance {
					platforms = []string{service.PlatformOpenAI}
				}
				h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, platforms...)
				h.maxAccountSwitches = 2
				store := &legacyVideoHandlerStore{}
				service.NewLegacyVideoRecovery(store, h.gatewayService, nil, nil, nil)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c, w := legacyVideoHandlerContext(ctx, seedance, true)
				store.onSave = func(saveCtx context.Context, task *service.LegacyVideoTask) {
					require.False(t, c.Writer.Written(), "持久保存完成前不得提交成功响应")
					require.NoError(t, saveCtx.Err(), "客户端断连不能取消已受理任务的保存")
					require.Equal(t, int64(10), task.UserID)
					require.Equal(t, int64(10), task.BillingUserID)
					require.Equal(t, int64(20), task.APIKeyID)
					require.Equal(t, int64(24), task.GroupID)
					require.NotNil(t, task.Pending.SeedanceBilling)
					require.NotEmpty(t, task.RequestID)
				}
				switch scenario {
				case "cache unavailable":
					bindings.bindingErr, bindings.pendingWriteErr = errors.New("缓存不可用"), errors.New("缓存不可用")
				case "save retry":
					store.saveErrors = 1
				case "save unavailable":
					store.saveErrors = 2
				case "preflight unavailable":
					store.checkErr = errors.New("数据库不可用")
				}
				body := `{"request_id":"task","status":"pending"}`
				if seedance {
					body = `{"id":"task","status":"queued"}`
				}
				if scenario == "missing task id" {
					body = `{"status":"pending"}`
				}
				upstream.call = func(req *http.Request, _ int64) (*http.Response, error) {
					require.Equal(t, http.MethodPost, req.Method)
					if scenario == "client cancelled" {
						cancel()
					}
					if scenario == "transport error" {
						return nil, context.DeadlineExceeded
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}
				if seedance {
					h.SeedanceTasks(c)
				} else {
					h.GrokVideoGeneration(c)
				}
				slots.assertReleased(t)
				require.Empty(t, bindings.claimContexts, "新建任务不领取扣费权")
				if scenario == "preflight unavailable" {
					require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
					require.Zero(t, upstream.calls)
					require.Zero(t, store.saves)
					return
				}
				require.Equal(t, 1, upstream.calls, "不能因任务结果或保存异常重复创建付费任务")
				switch scenario {
				case "missing task id", "transport error":
					require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
					require.Zero(t, store.saves)
				case "save unavailable":
					require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
					require.Equal(t, 2, store.saves)
					require.Equal(t, "task", gjson.Get(w.Body.String(), "id").String())
					require.Equal(t, "TaskStorageUnavailable", gjson.Get(w.Body.String(), "error.code").String())
					require.Contains(t, gjson.Get(w.Body.String(), "error.message").String(), "do not resubmit")
				default:
					require.Equal(t, http.StatusOK, w.Code, w.Body.String())
					require.JSONEq(t, body, w.Body.String())
					require.NotNil(t, store.task)
					if scenario == "save retry" {
						require.Equal(t, 2, store.saves)
					} else {
						require.Equal(t, 1, store.saves)
					}
				}
			})
		}
	}
}

// 装配持久恢复后重复客户端查询只展示结果，由后台任务持有唯一结算职责。
func TestLegacyVideoHandlerCompletedPollingDoesNotClaimBilling(t *testing.T) {
	for _, provider := range []string{"grok", "seedance"} {
		t.Run(provider, func(t *testing.T) {
			seedance := provider == "seedance"
			var platforms []string
			if seedance {
				platforms = []string{service.PlatformOpenAI}
			}
			h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, platforms...)
			c, _ := legacyVideoHandlerContext(context.Background(), seedance, false)
			key, _ := middleware.GetAPIKeyFromContext(c)
			source, model := "grok_video", "grok-imagine-video"
			response := `{"status":"done","video":{"url":"https://media.example/video.mp4","duration":6}}`
			if seedance {
				source, model = "seedance_video", "seedance"
				response = `{"id":"task","status":"succeeded","usage":{"completion_tokens":12345}}`
			}
			store := &legacyVideoHandlerStore{task: &service.LegacyVideoTask{
				Source: source, TaskID: "task", UserID: 10, BillingUserID: 10, APIKeyID: 20, GroupID: 24, AccountID: 1,
				Pending: &service.GrokVideoPendingBilling{Model: model, VideoDurationSeconds: 6, SeedanceBilling: newSeedanceBillingSnapshot(key, nil, service.ChannelMappingResult{})},
			}}
			service.NewLegacyVideoRecovery(store, h.gatewayService, nil, nil, nil)
			upstream.call = func(req *http.Request, _ int64) (*http.Response, error) {
				require.Equal(t, http.MethodGet, req.Method)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
			}
			for range 5 {
				c, w := legacyVideoHandlerContext(context.Background(), seedance, false)
				if seedance {
					h.SeedanceTasks(c)
				} else {
					h.GrokVideoStatus(c)
				}
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				slots.assertReleased(t)
			}
			require.Equal(t, 5, upstream.calls)
			require.Empty(t, bindings.claimContexts)
			require.Empty(t, bindings.billed)
			require.Zero(t, store.saves)
		})
	}
}

// 删除只重试本地事实写入，不会因数据库、缓存或客户端取消再次发出 DELETE。
func TestLegacyVideoHandlerDeletionPersistence(t *testing.T) {
	for _, scenario := range []string{"success", "client cancelled", "cache unavailable", "save unavailable", "upstream failure"} {
		t.Run(scenario, func(t *testing.T) {
			h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
			store := &legacyVideoHandlerStore{task: &service.LegacyVideoTask{Source: "seedance_video", TaskID: "task", UserID: 10, BillingUserID: 10, APIKeyID: 20, GroupID: 24, AccountID: 1}}
			service.NewLegacyVideoRecovery(store, h.gatewayService, nil, nil, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, w := legacyVideoHandlerContext(ctx, true, false)
			c.Request.Method = http.MethodDelete
			store.onDelete = func(saveCtx context.Context, task *service.LegacyVideoTask) {
				require.False(t, c.Writer.Written())
				require.NoError(t, saveCtx.Err())
				require.Equal(t, int64(10), task.UserID)
				require.Equal(t, int64(10), task.BillingUserID)
				require.Equal(t, int64(20), task.APIKeyID)
				require.Equal(t, int64(24), task.GroupID)
				require.Equal(t, int64(1), task.AccountID)
			}
			if scenario == "save unavailable" {
				store.deleteErrors = 2
			}
			if scenario == "cache unavailable" {
				bindings.pendingReadErr = errors.New("缓存暂时不可用")
			}
			upstream.call = func(req *http.Request, _ int64) (*http.Response, error) {
				require.Equal(t, http.MethodDelete, req.Method)
				status, body := http.StatusNoContent, ""
				if scenario == "client cancelled" {
					cancel()
				}
				if scenario == "upstream failure" {
					status, body = http.StatusServiceUnavailable, `{"error":{"message":"busy"}}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			h.SeedanceTasks(c)
			require.Equal(t, 1, upstream.calls)
			slots.assertReleased(t)
			require.Empty(t, bindings.claimContexts)
			switch scenario {
			case "save unavailable":
				require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
				require.Equal(t, "task", gjson.Get(w.Body.String(), "id").String())
				require.Contains(t, w.Body.String(), "already deleted; do not resubmit")
				require.Equal(t, 2, store.deletions)
			case "upstream failure":
				require.Equal(t, http.StatusServiceUnavailable, w.Code)
				require.Zero(t, store.deletions)
			default:
				require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
				require.Empty(t, w.Body.String())
				require.Equal(t, 1, store.deletions)
				require.NotNil(t, store.task.UpstreamDeletedAt)
			}
		})
	}
}
