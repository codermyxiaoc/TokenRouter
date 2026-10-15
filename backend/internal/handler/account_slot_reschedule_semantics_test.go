//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 取消发生在第一次排队满之后、第二次选号内部，模拟容易误写为429的竞争窗口。
type queueCancelSnapshot struct {
	fakeSchedulerCache
	state  *queueSemanticCache
	cancel context.CancelFunc
}

func (s *queueCancelSnapshot) GetSnapshot(ctx context.Context, bucket service.SchedulerBucket) ([]*service.Account, bool, error) {
	if s.state.queued && s.state.cancelOnSelect {
		s.cancel()
		return nil, false, context.Canceled
	}
	return s.fakeSchedulerCache.GetSnapshot(ctx, bucket)
}

type queueSemanticCache struct {
	fakeConcurrencyCache
	queued          bool
	cancelOnSelect  bool
	accountAcquires int
	waitReleases    int
}

func (s *queueSemanticCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	s.accountAcquires++
	if s.queued && !s.cancelOnSelect {
		return false, errors.New("redis temporarily unavailable")
	}
	return false, nil
}

func (s *queueSemanticCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	s.queued = true
	return !s.cancelOnSelect, nil
}

func (s *queueSemanticCache) DecrementAccountWaitCount(context.Context, int64) error {
	s.waitReleases++
	return nil
}

// 使用真实调度器和三个公开入口验证取消优先，不通过直接调用错误writer绕开重选路径。
func TestGatewayQueueReselectionCancellationKeepsClientStatus(t *testing.T) {
	for _, protocol := range []string{"gemini_native", "gemini_messages", "anthropic_messages"} {
		t.Run(protocol, func(t *testing.T) {
			c, rec, state := runQueueSemanticHandler(t, protocol, true)
			require.True(t, state.queued)
			require.ErrorIs(t, c.Request.Context().Err(), context.Canceled)
			require.Equal(t, statusClientClosedRequest, c.Writer.Status())
			require.Empty(t, rec.Body.String(), "已取消请求不能再写容量或上游错误正文")
			require.Zero(t, state.waitReleases, "队列未入队不能释放不存在的等待计数")
		})
	}
}

// Gemini 原生排队使用 SSEPingFormatNone；并发服务故障应保留JSON 503而不是伪装成用户429。
func TestGeminiAccountQueueServiceFailureKeeps503(t *testing.T) {
	c, rec, state := runQueueSemanticHandler(t, "gemini_native", false)
	require.NoError(t, c.Request.Context().Err())
	require.Equal(t, http.StatusServiceUnavailable, c.Writer.Status())
	// 沿用项目现有 Google 错误枚举映射：所有5xx均为 INTERNAL，不在本次修改全局协议。
	require.JSONEq(t, `{"error":{"code":503,"status":"INTERNAL","message":"Service temporarily unavailable, please retry later"}}`, rec.Body.String())
	require.Equal(t, 1, state.waitReleases, "并发服务报错后也必须释放已取得的等待名额")
}

func runQueueSemanticHandler(t *testing.T, protocol string, cancelOnSelect bool) (*gin.Context, *httptest.ResponseRecorder, *queueSemanticCache) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	platform, model := service.PlatformGemini, "gemini-3.7-flash"
	if protocol == "anthropic_messages" {
		platform, model = service.PlatformAnthropic, "claude-sonnet-4-5"
	}
	group := &service.Group{ID: 9901, Hydrated: true, Platform: platform, Status: service.StatusActive}
	account := &service.Account{ID: 9902, Platform: platform, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-only"}, AccountGroups: []service.AccountGroup{{AccountID: 9902, GroupID: group.ID}}}
	state := &queueSemanticCache{cancelOnSelect: cancelOnSelect}
	snapshotCache := &queueCancelSnapshot{fakeSchedulerCache: fakeSchedulerCache{accounts: []*service.Account{account}}, state: state, cancel: cancel}
	snapshot := service.NewSchedulerSnapshotService(snapshotCache, nil, nil, nil, nil)
	concurrency := service.NewConcurrencyService(state)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	cfg.Gateway.Scheduling.FallbackWaitTimeout = time.Second
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gateway := service.NewGatewayService(nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil,
		cfg, snapshot, concurrency, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &GatewayHandler{gatewayService: gateway, billingCacheService: billing,
		concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatClaude, time.Second),
		cfg:               cfg, maxAccountSwitches: 1, maxAccountSwitchesGemini: 1}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"model":"` + model + `","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`
	path := "/v1/messages"
	if protocol == "gemini_native" {
		body = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
		path = "/v1beta/models/" + model + ":generateContent"
		c.Params = gin.Params{{Key: "modelAction", Value: model + ":generateContent"}}
	}
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	key := &service.APIKey{ID: 9903, UserID: 9904, GroupID: &group.ID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 9904, Concurrency: 1, Balance: 100, Status: service.StatusActive}}
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 1})
	if protocol == "gemini_native" {
		h.GeminiV1BetaModels(c)
	} else {
		h.Messages(c)
	}
	return c, rec, state
}
