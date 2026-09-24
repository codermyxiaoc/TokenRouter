//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	middleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

type videoTaskObserverStub struct {
	observations  []service.MediaTaskObservation
	err           error
	contextErrors []error
}

func (s *videoTaskObserverStub) ObserveMediaTask(ctx context.Context, observation service.MediaTaskObservation) error {
	s.contextErrors = append(s.contextErrors, ctx.Err())
	s.observations = append(s.observations, observation)
	return s.err
}

// 列表投影失败不能影响既有创建响应、任务归属或并发槽释放。
func TestGrokMediaTaskObservationPreservesGateway(t *testing.T) {
	for _, fail := range []bool{false, true} {
		h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false)
		observer := &videoTaskObserverStub{}
		if fail {
			observer.err = errors.New("projection unavailable")
		}
		h.SetMediaTaskObserver(observer)
		c, w := grokMediaSlotContext(context.Background(), true)
		h.GrokVideoGeneration(c)
		require.Equal(t, 200, w.Code, w.Body.String())
		require.JSONEq(t, `{"request_id":"task","status":"pending"}`, w.Body.String())
		require.Len(t, observer.observations, 1)
		observation := observer.observations[0]
		require.Equal(t, "grok_video", observation.Source)
		require.Equal(t, "task", observation.TaskID)
		require.Equal(t, int64(10), observation.UserID)
		require.Equal(t, int64(20), observation.APIKeyID)
		require.Equal(t, int64(24), *observation.GroupID)
		require.NotNil(t, observation.AccountID)
		require.Equal(t, "grok-imagine-video", observation.Model)
		require.WithinDuration(t, observation.CreatedAt.Add(24*time.Hour), *observation.ExpiresAt, time.Millisecond)
		require.Len(t, bindings.pending, 1)
		require.Empty(t, bindings.billed, "创建观测不应触发扣费")
		slots.assertReleased(t)

		upstream.call = func(*http.Request, int64) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"failed","error":{"message":"sk-secret prompt"}}`))}, nil
		}
		c, w = grokMediaSlotContext(context.Background(), false)
		h.GrokVideoStatus(c)
		require.Equal(t, 200, w.Code)
		require.Contains(t, w.Body.String(), "sk-secret prompt", "原生响应保持不变，脱敏仅针对列表投影")
		require.Len(t, observer.observations, 2)
		require.Equal(t, "failed", observer.observations[1].Status)
		require.Equal(t, "Upstream video task failed", observer.observations[1].ErrorMessage)
		require.Empty(t, bindings.billed)
		slots.assertReleased(t)
	}
}

// 任务归属使用已鉴权的用户与 Key，独立上下文让客户端断连后仍可保存观测。
func TestVideoTaskObservationOwnerAndCancellation(t *testing.T) {
	observer := &videoTaskObserverStub{}
	h := &OpenAIGatewayHandler{}
	h.SetMediaTaskObserver(observer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := grokMediaSlotContext(ctx, false)
	key, _ := middleware.GetAPIKeyFromContext(c)
	result := &service.OpenAIForwardResult{ResponseID: "seedance:ignored-response-id", Usage: service.OpenAIUsage{OutputTokens: 12}, MediaTaskObservation: &service.MediaTaskObservation{Source: "seedance_video", Platform: "openai", MediaType: "video", Status: "completed"}}
	h.observeVideoTask(c, service.SeedanceEndpointStatus, "seedance:task-original", key, 10, &service.Account{ID: 31}, result, "", "")
	require.Len(t, observer.observations, 1)
	observation := observer.observations[0]
	require.Equal(t, "task-original", observation.TaskID)
	require.Equal(t, "grok-video:seedance:task-original", observation.RequestID)
	require.Equal(t, int64(31), *observation.AccountID)
	require.Nil(t, observer.contextErrors[0])
	require.Zero(t, observation.CreatedAt)
	require.Nil(t, observation.ExpiresAt)
}

// 团队 Key 的任务列表属于实际成员，原视频缓存仍使用团队付款人的归属维度。
func TestVideoTaskObservationUsesTeamMemberNotBillingOwner(t *testing.T) {
	h, slots, _, _ := newGrokMediaSlotHandler(t, false, false)
	observer := &videoTaskObserverStub{}
	h.SetMediaTaskObserver(observer)
	c, w := grokMediaSlotContext(context.Background(), true)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.UserID = 37
	// 原请求主体与预加载 User 仍然是 ID 为 10 的团队付款人。
	require.Equal(t, int64(10), key.User.ID)
	h.GrokVideoGeneration(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, observer.observations, 1)
	require.Equal(t, int64(37), observer.observations[0].UserID)
	require.Equal(t, key.ID, observer.observations[0].APIKeyID)
	ownerAccount, err := h.gatewayService.ResolveGrokMediaVideoRequestAccount(context.Background(), key.GroupID, "task", 10, key.ID)
	require.NoError(t, err)
	require.Positive(t, ownerAccount, "投影修改不能改变原上游任务查询归属")
	slots.assertReleased(t)

	// 完成观测与 usage_logs 的成员 ID、Key ID、稳定扣费 ID 一致，可以准确关联费用。
	result := &service.OpenAIForwardResult{ResponseID: "task", VideoCount: 1,
		MediaTaskObservation: &service.MediaTaskObservation{Source: "grok_video", Platform: service.PlatformGrok, MediaType: "video", Status: "completed"}}
	h.observeVideoTask(c, service.GrokMediaEndpointVideoStatus, "task", key, 10, &service.Account{ID: ownerAccount}, result, "", "")
	require.Len(t, observer.observations, 2)
	require.Equal(t, int64(37), observer.observations[1].UserID)
	require.Equal(t, key.ID, observer.observations[1].APIKeyID)
	require.Equal(t, service.StableGrokVideoBillingRequestID("task"), observer.observations[1].RequestID)
}

// 未授权查询与暂时网络故障不能伪造任务失败，也不能污染另一个用户的任务投影。
func TestGrokMediaTaskObservationSkipsRejectedAndTransientQueries(t *testing.T) {
	h, slots, _, upstream := newGrokMediaSlotHandler(t, false, false)
	observer := &videoTaskObserverStub{}
	h.SetMediaTaskObserver(observer)
	c, w := grokMediaSlotContext(context.Background(), false)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 11, Concurrency: 5})
	h.GrokVideoStatus(c)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Zero(t, upstream.calls)
	require.Empty(t, observer.observations)
	slots.assertReleased(t)

	upstream.call = func(*http.Request, int64) (*http.Response, error) {
		return nil, errors.New("temporary network outage")
	}
	c, w = grokMediaSlotContext(context.Background(), false)
	h.GrokVideoStatus(c)
	require.GreaterOrEqual(t, w.Code, http.StatusBadRequest)
	require.Empty(t, observer.observations)
	slots.assertReleased(t)
}
