//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// requestDataCacheSettingRepo 是只保存数据缓存配置的内存设置仓储。
type requestDataCacheSettingRepo struct {
	ingressRejectSettingRepo
	mu     sync.Mutex
	values map[string]string
}

func (r *requestDataCacheSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (r *requestDataCacheSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

// requestDataCacheOpsRepo 记录写入的请求快照。
type requestDataCacheOpsRepo struct {
	ingressRejectOpsRepo
	details chan *service.OpsRequestPayloadDetail
}

func (r *requestDataCacheOpsRepo) UpsertRequestPayloadDetail(_ context.Context, detail *service.OpsRequestPayloadDetail) error {
	r.details <- detail
	return nil
}

func newRequestDataCacheTestRouter(t *testing.T, cfg service.RequestDataCacheSettings, status int) (*gin.Engine, *requestDataCacheOpsRepo) {
	return newRequestDataCacheTestRouterWith(t, cfg, func(c *gin.Context) {
		c.Data(status, "application/json", []byte(`{"id":"resp"}`))
	})
}

func newRequestDataCacheTestRouterWith(t *testing.T, cfg service.RequestDataCacheSettings, respond gin.HandlerFunc) (*gin.Engine, *requestDataCacheOpsRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := &requestDataCacheOpsRepo{details: make(chan *service.OpsRequestPayloadDetail, 4)}
	settings := &requestDataCacheSettingRepo{values: map[string]string{}}
	ops := service.NewOpsService(repo, settings, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err := ops.UpdateRequestDataCacheSettings(context.Background(), &cfg)
	require.NoError(t, err)

	groupID := int64(5)
	key := &service.APIKey{ID: 11, UserID: 7, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "req-cache-1"))
		c.Next()
	})
	r.Use(OpsErrorLoggerMiddleware(ops))
	r.POST("/v1/responses", func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), key)
		respond(c)
	})
	return r, repo
}

func serveRequestDataCacheRequest(r *gin.Engine) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1"}`))
	r.ServeHTTP(httptest.NewRecorder(), req)
}

// 默认配置下成功请求不写快照，保持上线前行为。
func TestRequestDataCacheSkipsNormalRequestsByDefault(t *testing.T) {
	r, repo := newRequestDataCacheTestRouter(t, service.DefaultRequestDataCacheSettings(), http.StatusOK)
	serveRequestDataCacheRequest(r)
	select {
	case detail := <-repo.details:
		t.Fatalf("unexpected cached detail: %+v", detail)
	case <-time.After(200 * time.Millisecond):
	}
}

// 开启普通请求缓存后成功请求写入快照，带归属、类别与过期时间。
func TestRequestDataCacheRecordsNormalRequestWhenEnabled(t *testing.T) {
	cfg := service.DefaultRequestDataCacheSettings()
	cfg.Normal = service.RequestDataCacheScope{Enabled: true, TTLMinutes: 60, GroupIDs: []int64{5}}
	r, repo := newRequestDataCacheTestRouter(t, cfg, http.StatusOK)
	serveRequestDataCacheRequest(r)
	select {
	case detail := <-repo.details:
		require.Equal(t, service.RequestDataCacheKindNormal, detail.Kind)
		require.Equal(t, "req-cache-1", detail.RequestID)
		require.Equal(t, int64(7), *detail.UserID)
		require.Equal(t, int64(11), *detail.APIKeyID)
		require.Equal(t, int64(5), *detail.GroupID)
		require.NotNil(t, detail.ExpiresAt)
		require.WithinDuration(t, detail.CompletedAt.Add(time.Hour), *detail.ExpiresAt, time.Second)
		require.Equal(t, "/v1/responses", detail.Path)
	case <-time.After(2 * time.Second):
		t.Fatal("normal request was not cached")
	}
}

// 错误请求按错误缓存设置独立判断：分组不在范围内时不写入。
func TestRequestDataCacheErrorScopeIsIndependent(t *testing.T) {
	cfg := service.DefaultRequestDataCacheSettings()
	cfg.Error = service.RequestDataCacheScope{Enabled: true, TTLMinutes: 60, GroupIDs: []int64{9}}
	r, repo := newRequestDataCacheTestRouter(t, cfg, http.StatusBadGateway)
	serveRequestDataCacheRequest(r)
	select {
	case detail := <-repo.details:
		t.Fatalf("out-of-scope error was cached: %+v", detail)
	case <-time.After(200 * time.Millisecond):
	}

	cfg.Error.GroupIDs = []int64{5}
	r, repo = newRequestDataCacheTestRouter(t, cfg, http.StatusBadGateway)
	serveRequestDataCacheRequest(r)
	select {
	case detail := <-repo.details:
		require.Equal(t, service.RequestDataCacheKindError, detail.Kind)
		raw, _ := json.Marshal(detail)
		require.Contains(t, string(raw), "req-cache-1")
	case <-time.After(2 * time.Second):
		t.Fatal("in-scope error was not cached")
	}
}

// HTTP 200 的 SSE 带内失败（如 Gemini RESOURCE_EXHAUSTED）已登记为流失败，必须按错误请求缓存：
// 只开普通缓存时不写入，开启错误缓存时以 error 类别和错误缓存时间写入。
func TestRequestDataCacheClassifiesMarkedStreamFailureAsError(t *testing.T) {
	respond := func(c *gin.Context) {
		service.MarkOpsStreamFailure(c, "rate_limit_error", "RESOURCE_EXHAUSTED", "quota exhausted", http.StatusTooManyRequests)
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write([]byte("data: {\"candidates\":[]}\n\n"))
	}

	normalOnly := service.DefaultRequestDataCacheSettings()
	normalOnly.Normal = service.RequestDataCacheScope{Enabled: true, TTLMinutes: 1440, AllGroups: true}
	normalOnly.Error = service.RequestDataCacheScope{Enabled: false, TTLMinutes: 60, AllGroups: true}
	r, repo := newRequestDataCacheTestRouterWith(t, normalOnly, respond)
	serveRequestDataCacheRequest(r)
	select {
	case detail := <-repo.details:
		t.Fatalf("stream failure cached as normal request: kind=%s", detail.Kind)
	case <-time.After(200 * time.Millisecond):
	}

	errorOn := normalOnly
	errorOn.Error = service.RequestDataCacheScope{Enabled: true, TTLMinutes: 60, AllGroups: true}
	r, repo = newRequestDataCacheTestRouterWith(t, errorOn, respond)
	serveRequestDataCacheRequest(r)
	select {
	case detail := <-repo.details:
		require.Equal(t, service.RequestDataCacheKindError, detail.Kind)
		require.NotNil(t, detail.ExpiresAt)
		require.WithinDuration(t, detail.CompletedAt.Add(time.Hour), *detail.ExpiresAt, time.Second)
	case <-time.After(2 * time.Second):
		t.Fatal("stream failure was not cached as error request")
	}
}
