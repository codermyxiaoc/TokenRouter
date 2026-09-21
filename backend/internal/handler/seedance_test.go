//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	middleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSeedanceHandlerLifecycleAndOwnership(t *testing.T) {
	h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	var owner int64
	upstream.call = func(req *http.Request, id int64) (*http.Response, error) {
		body := `{"id":"task-ark","status":"queued"}`
		if req.Method == http.MethodPost {
			owner = id
		} else {
			require.Equal(t, owner, id)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	newContext := func(method string) (*gin.Context, *httptest.ResponseRecorder) {
		c, w := grokMediaSlotContext(context.Background(), method == http.MethodPost)
		key, _ := middleware.GetAPIKeyFromContext(c)
		key.Group.Platform = service.PlatformOpenAI
		body := ""
		if method == http.MethodPost {
			body = `{"model":"doubao-seedance","content":[{"type":"text","text":"waves"}]}`
		}
		c.Request = httptest.NewRequest(method, "/api/v3/contents/generations/tasks", strings.NewReader(body))
		c.Params = gin.Params{{Key: "task_id", Value: "task-ark"}}
		return c, w
	}
	c, w := newContext(http.MethodPost)
	h.SeedanceTasks(c)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Positive(t, owner)
	require.Len(t, bindings.pending, 1)
	slots.assertReleased(t)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		c, w = newContext(method)
		h.SeedanceTasks(c)
		require.Equal(t, 200, w.Code, w.Body.String())
		slots.assertReleased(t)
	}
	for _, other := range []string{"user", "key", "group", "task", "provider"} {
		c, w = newContext(http.MethodGet)
		key, _ := middleware.GetAPIKeyFromContext(c)
		switch other {
		case "user":
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 11, Concurrency: 5})
		case "key":
			key.ID = 21
		case "group":
			group := int64(25)
			key.GroupID = &group
		case "task":
			c.Params = gin.Params{{Key: "task_id", Value: "other"}}
		case "provider":
			c.Params = gin.Params{{Key: "request_id", Value: "task-ark"}}
		}
		before := upstream.calls
		if other == "provider" {
			h.GrokVideoStatus(c)
		} else {
			h.SeedanceTasks(c)
		}
		require.Equal(t, 404, w.Code, other+": "+w.Body.String())
		require.Equal(t, before, upstream.calls)
		slots.assertReleased(t)
	}
	c, _ = newContext(http.MethodGet)
	key, _ := middleware.GetAPIKeyFromContext(c)
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	result := &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 12345}, ResponseID: "seedance:task-ark"}
	for i := range 20 {
		billed, snapshot := prepareSeedanceCompletionBilling(context.Background(), h, key, subject, result.ResponseID, result)
		if i == 0 {
			require.NotNil(t, billed)
			require.NotNil(t, snapshot)
			require.Equal(t, "doubao-seedance", billed.BillingModel)
			require.Equal(t, 12345, billed.Usage.OutputTokens)
			require.Zero(t, billed.VideoCount)
		} else {
			require.Nil(t, billed)
		}
	}
	require.Len(t, bindings.billed, 1)
}

func TestSeedanceBillingSnapshotKeepsCreationFundingAndMapping(t *testing.T) {
	h, _, _, _ := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	c, _ := grokMediaSlotContext(context.Background(), false)
	key, _ := middleware.GetAPIKeyFromContext(c)
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	subscriptionID := int64(31)
	key.BillingMode = service.APIKeyBillingModeSubscription
	key.PreferredSubscriptionID = &subscriptionID
	key.Group.Platform = service.PlatformOpenAI
	key.Group.RateMultiplier = 2
	subscription := &service.UserSubscription{ID: subscriptionID, UserID: key.User.ID, User: &service.User{ID: key.User.ID}, Plan: &service.SubscriptionPlan{ID: 7, Name: "创建时套餐"}}
	mapping := service.ChannelMappingResult{Mapped: true, MappedModel: "video-channel", ChannelID: 15, BillingModelSource: service.BillingModelSourceRequested}
	snapshot := newSeedanceBillingSnapshot(key, subscription, mapping)
	require.Nil(t, snapshot.Subscription.User)
	require.Nil(t, snapshot.Group.AccountGroups)
	pending := service.GrokVideoPendingBilling{Model: "video-client", BillingModel: "video-channel", UpstreamModel: "ep-video", SeedanceBilling: snapshot}
	require.NoError(t, h.gatewayService.StoreGrokVideoPendingBilling(context.Background(), "seedance:billing-task", subject.UserID, key.ID, pending))
	key.BillingMode = service.APIKeyBillingModeBalance
	key.PreferredSubscriptionID = nil
	key.Group.RateMultiplier = 9
	result := &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 456}, ResponseID: "seedance:billing-task"}
	billed, saved := prepareSeedanceCompletionBilling(context.Background(), h, key, subject, result.ResponseID, result)
	require.NotNil(t, billed)
	require.Equal(t, service.APIKeyBillingModeSubscription, saved.BillingMode)
	require.Equal(t, subscriptionID, *saved.PreferredSubscriptionID)
	require.Equal(t, subscriptionID, saved.Subscription.ID)
	require.Equal(t, "创建时套餐", saved.Subscription.Plan.Name)
	require.Equal(t, float64(2), saved.Group.RateMultiplier)
	require.Equal(t, mapping, saved.ChannelMapping)
	require.Equal(t, "video-client", billed.Model)
	require.Equal(t, "video-channel", billed.BillingModel)
	require.Equal(t, "ep-video", billed.UpstreamModel)
	require.Equal(t, service.StableGrokVideoBillingRequestID(result.ResponseID), billed.RequestID)
	require.NoError(t, h.gatewayService.ReleaseGrokVideoBilling(context.Background(), result.ResponseID, subject.UserID, key.ID))
	retry, _ := prepareSeedanceCompletionBilling(context.Background(), h, key, subject, result.ResponseID, result)
	require.NotNil(t, retry, "结算失败释放领取后可以重试")
}

func TestSeedanceMissingSnapshotDoesNotClaimOrGuessBilling(t *testing.T) {
	h, _, bindings, _ := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	c, _ := grokMediaSlotContext(context.Background(), false)
	key, _ := middleware.GetAPIKeyFromContext(c)
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	result := &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 123}, ResponseID: "seedance:missing"}
	billed, snapshot := prepareSeedanceCompletionBilling(context.Background(), h, key, subject, result.ResponseID, result)
	require.Nil(t, billed)
	require.Nil(t, snapshot)
	require.Empty(t, bindings.billed)
	key.BillingMode = service.APIKeyBillingModeAuto
	saved := newSeedanceBillingSnapshot(key, nil, service.ChannelMappingResult{})
	require.Equal(t, service.APIKeyBillingModeBalance, saved.BillingMode, "创建时余额结算不能在轮询时改扣新购买套餐")
	saved = newSeedanceBillingSnapshot(key, &service.UserSubscription{ID: 42, UserID: key.User.ID}, service.ChannelMappingResult{})
	require.Equal(t, service.APIKeyBillingModeAuto, saved.BillingMode)
	require.Equal(t, int64(42), saved.Subscription.ID, "自动模式也不能在轮询时改扣另一套餐")
}

func TestSeedanceCreatePersistsAfterClientCancellation(t *testing.T) {
	h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream.call = func(*http.Request, int64) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"task-accepted"}`))}, nil
	}
	c, w := grokMediaSlotContext(ctx, true)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformOpenAI
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"seedance","content":[{"type":"text","text":"waves"}]}`)).WithContext(ctx)
	h.SeedanceTasks(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, upstream.calls)
	require.Len(t, bindings.pending, 1)
	accountID, err := h.gatewayService.ResolveGrokMediaVideoRequestAccount(context.Background(), key.GroupID, "seedance:task-accepted", 10, key.ID)
	require.NoError(t, err)
	require.Positive(t, accountID)
	slots.assertReleased(t)
}

func TestSeedanceHandlerDoesNotReplayRejectedCreation(t *testing.T) {
	h, slots, _, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	upstream.call = func(*http.Request, int64) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"ServiceUnavailable","message":"busy"}}`))}, nil
	}
	c, w := grokMediaSlotContext(context.Background(), true)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformOpenAI
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"seedance","content":[{"type":"text","text":"waves"}]}`))
	h.SeedanceTasks(c)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.JSONEq(t, `{"error":{"code":"ServiceUnavailable","message":"busy"}}`, w.Body.String())
	require.Equal(t, 1, upstream.calls, "异步创建失败不得切换账号重复创建")
	slots.assertReleased(t)
}

func TestSeedanceBillingCannotBeDroppedByFullUsageQueue(t *testing.T) {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: 1, TaskTimeout: time.Second, OverflowPolicy: "drop", OverflowSamplePercent: 0,
	})
	t.Cleanup(pool.Stop)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	pool.Submit(func(context.Context) { close(started); <-release })
	<-started
	pool.Submit(func(context.Context) {})
	var seedanceBilled, ordinaryBilled atomic.Bool
	h.submitMediaUsageRecordTask(nil, &service.OpenAIForwardResult{ResponseID: "seedance:task-1", Usage: service.OpenAIUsage{OutputTokens: 123}}, func(ctx context.Context) {
		require.NoError(t, ctx.Err())
		seedanceBilled.Store(true)
	})
	h.submitMediaUsageRecordTask(nil, &service.OpenAIForwardResult{ResponseID: "grok-task", VideoCount: 1}, func(context.Context) { ordinaryBilled.Store(true) })
	require.True(t, seedanceBilled.Load(), "方舟任务已领取计费权，队列满时必须同步结算")
	require.False(t, ordinaryBilled.Load(), "已有 Grok 媒体维持原本队列策略")
}

// seedanceCancelWriter 模拟客户端刚读完成功响应就关闭请求连接。
type seedanceCancelWriter struct {
	gin.ResponseWriter
	cancel context.CancelFunc
}

func (w *seedanceCancelWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	w.cancel()
	return n, err
}

func TestSeedanceCompletionClaimSurvivesResponseCancellation(t *testing.T) {
	h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, w := grokMediaSlotContext(ctx, false)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformOpenAI
	require.NoError(t, h.gatewayService.BindGrokMediaVideoRequestAccount(context.Background(), key.GroupID, "seedance:task-complete", 10, key.ID, 1))
	require.NoError(t, h.gatewayService.StoreGrokVideoPendingBilling(context.Background(), "seedance:task-complete", 10, key.ID, service.GrokVideoPendingBilling{
		Model: "seedance", SeedanceBilling: newSeedanceBillingSnapshot(key, nil, service.ChannelMappingResult{}),
	}))
	upstream.call = func(*http.Request, int64) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"task-complete","status":"succeeded","usage":{"completion_tokens":123}}`))}, nil
	}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v3/contents/generations/tasks/task-complete", nil).WithContext(ctx)
	c.Params = gin.Params{{Key: "task_id", Value: "task-complete"}}
	c.Writer = &seedanceCancelWriter{ResponseWriter: c.Writer, cancel: cancel}
	h.SeedanceTasks(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Len(t, bindings.claimContexts, 1, "响应后的取消不能跳过计费领取")
	require.NoError(t, bindings.claimContexts[0])
	slots.assertReleased(t)
}

func TestSeedanceStorageFailuresDoNotClaimSuccessfulCreation(t *testing.T) {
	for _, failure := range []string{"preflight", "owner", "pending"} {
		t.Run(failure, func(t *testing.T) {
			h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
			storageError := errors.New("task storage unavailable")
			switch failure {
			case "preflight":
				bindings.pendingReadErr = storageError
			case "owner":
				bindings.bindingErr = storageError
			case "pending":
				bindings.pendingWriteErr = storageError
			}
			upstream.call = func(*http.Request, int64) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"task-already-created"}`))}, nil
			}
			c, w := grokMediaSlotContext(context.Background(), true)
			key, _ := middleware.GetAPIKeyFromContext(c)
			key.Group.Platform = service.PlatformOpenAI
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"seedance","content":[{"type":"text","text":"waves"}]}`))
			h.SeedanceTasks(c)
			require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
			if failure == "preflight" {
				require.Zero(t, upstream.calls)
			} else {
				require.Equal(t, 1, upstream.calls)
				require.Equal(t, "task-already-created", gjson.Get(w.Body.String(), "id").String())
				require.Equal(t, "TaskStorageUnavailable", gjson.Get(w.Body.String(), "error.code").String())
				require.Contains(t, gjson.Get(w.Body.String(), "error.message").String(), "do not resubmit")
			}
			slots.assertReleased(t)
		})
	}
}
