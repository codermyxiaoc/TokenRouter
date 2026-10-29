//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	middleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type imageRoutePolicyCache struct {
	cyberSessionBlockHandlerCacheStub
	mu    sync.Mutex
	until map[string]time.Time
}

func imageRouteKey(id int64, model, operation string) string {
	return fmt.Sprintf("%d:%s:%s", id, model, operation)
}
func (c *imageRoutePolicyCache) GetImageModelCooldown(_ context.Context, id int64, model, operation string) (time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return max(0, time.Until(c.until[imageRouteKey(id, model, operation)])), nil
}
func (c *imageRoutePolicyCache) CooldownImageModel(_ context.Context, id int64, model, operation string, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := imageRouteKey(id, model, operation)
	if !c.until[key].After(time.Now()) {
		c.until[key] = time.Now().Add(d)
	}
	return nil
}

type imageRoutePolicyUpstream struct {
	service.HTTPUpstream
	calls     []int64
	deadlines []time.Time
	respond   func(int64) (int, string, error)
}

func (u *imageRoutePolicyUpstream) DoWithTLS(req *http.Request, _ string, id int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls = append(u.calls, id)
	deadline, _ := req.Context().Deadline()
	u.deadlines = append(u.deadlines, deadline)
	status, body, err := u.respond(id)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(body))}, nil
}

func newImageRoutePolicyHandler(t *testing.T, upstream *imageRoutePolicyUpstream, cache *imageRoutePolicyCache) *OpenAIGatewayHandler {
	t.Helper()
	accounts := []service.Account{}
	for i := int64(1); i <= 3; i++ {
		accounts = append(accounts, service.Account{ID: 81000 + i, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: int(i), Credentials: map[string]any{"api_key": "local-test", "pool_mode": true, "pool_mode_retry_count": 10}})
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gw := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: accounts}, &openAIWSUsageHandlerUsageLogRepoStub{}, nil, nil, nil, nil, cache, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billing, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	return NewOpenAIGatewayHandler(gw, service.NewConcurrencyService(&fakeConcurrencyCache{}), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
}

func runImageRoutePolicyRequest(h *OpenAIGatewayHandler, model string) *httptest.ResponseRecorder {
	body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"local image route test","n":1,"response_format":"b64_json"}`, model))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	groupID := int64(81001)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 81001, UserID: 81001, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true, RateMultiplier: 1}, User: &service.User{ID: 81001, Balance: 100}})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 81001})
	h.Images(c)
	return rec
}

// 即使池账号配置十次重试，也只会向原账号和一个不同备选各提交一次。
func TestPublicImagesSafeSwitchAndNextRequestCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := &imageRoutePolicyUpstream{respond: func(id int64) (int, string, error) {
		if id == 81001 {
			return 429, `{"error":{"code":"rate_limit_exceeded","message":"rate limited"}}`, nil
		}
		return 200, `{"data":[{"b64_json":"aW1hZ2U="}]}`, nil
	}}
	cache := &imageRoutePolicyCache{until: map[string]time.Time{}}
	h := newImageRoutePolicyHandler(t, up, cache)
	require.Equal(t, 200, runImageRoutePolicyRequest(h, "gpt-image-2").Code)
	require.Equal(t, []int64{81001, 81002}, up.calls)
	require.Equal(t, up.deadlines[0], up.deadlines[1], "换号不得延长总截止时间")
	require.Equal(t, 200, runImageRoutePolicyRequest(h, "gpt-image-2").Code)
	require.Equal(t, []int64{81001, 81002, 81002}, up.calls, "后续请求避开冷却中的账号1")
	remaining, _ := cache.GetImageModelCooldown(context.Background(), 81001, "gpt-image-2", "edit")
	require.Zero(t, remaining, "生成失败不能扩大到编辑操作")
}

func TestPublicImagesStopUnknownAndDoNotPunishInvalidRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		cool   bool
	}{
		{"generic_502", 502, `{"error":{"message":"upstream unavailable"}}`, true},
		{"bad_image", 400, `{"error":{"code":"invalid_image","message":"bad input"}}`, false},
		{"empty_200", 200, `{"data":[]}`, true},
		{"usage_200", 200, `{"data":[],"usage":{"output_tokens":1}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &imageRoutePolicyCache{until: map[string]time.Time{}}
			up := &imageRoutePolicyUpstream{respond: func(int64) (int, string, error) { return tc.status, tc.body, nil }}
			h := newImageRoutePolicyHandler(t, up, cache)
			runImageRoutePolicyRequest(h, "gpt-image-2")
			require.Equal(t, []int64{81001}, up.calls)
			remaining, _ := cache.GetImageModelCooldown(context.Background(), 81001, "gpt-image-2", "generate")
			require.Equal(t, tc.cool, remaining > 0)
		})
	}
}

func TestPublicImagesTwoSendCapAndAllCoolingReturnImmediately(t *testing.T) {
	cache := &imageRoutePolicyCache{until: map[string]time.Time{}}
	up := &imageRoutePolicyUpstream{respond: func(int64) (int, string, error) { return 429, `{"error":{"code":"rate_limit_exceeded"}}`, nil }}
	h := newImageRoutePolicyHandler(t, up, cache)
	runImageRoutePolicyRequest(h, "gpt-image-2")
	require.Equal(t, []int64{81001, 81002}, up.calls, "不允许第三账号发送或同账号重发")
	_ = cache.CooldownImageModel(context.Background(), 81003, "gpt-image-2", "generate", time.Minute)
	rec := runImageRoutePolicyRequest(h, "gpt-image-2")
	require.Equal(t, 503, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	require.Equal(t, "image_models_cooling_down", gjson.Get(rec.Body.String(), "error.type").String())
	require.Len(t, up.calls, 2, "全冷却只返回，不睡眠或触发探测生成")
}

// 异步任务通过完整同步图片handler重入，轮询和后台落库不能重建一轮账号发送预算。
func TestAsyncImagesReentrySharesSafeTwoAccountPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firstStatus   int
		expectedCalls int
		finalStatus   string
	}{
		{"safe_rejection", 429, 2, service.ImageTaskStatusCompleted},
		{"unknown_after_send", 502, 1, service.ImageTaskStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &imageRoutePolicyUpstream{respond: func(id int64) (int, string, error) {
				if id == 81001 {
					return tc.firstStatus, `{"error":{"code":"upstream_busy","message":"upstream unavailable"}}`, nil
				}
				return 200, `{"data":[{"url":"https://example.test/image.png"}]}`, nil
			}}
			cache := &imageRoutePolicyCache{until: map[string]time.Time{}}
			images := newImageRoutePolicyHandler(t, up, cache)
			store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
			tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
			h := NewAsyncImageHandler(tasks, nil)
			router := gin.New()
			router.Use(h.CaptureRequest, func(c *gin.Context) {
				groupID := int64(81001)
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 81001, UserID: 81001, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true, RateMultiplier: 1}, User: &service.User{ID: 81001, Balance: 100}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 81001})
				c.Next()
			})
			router.POST("/v1/images/generations/async", h.Submit)
			router.POST("/v1/images/generations", images.Images)
			h.SetGatewayExecutor(func(_ string, c *gin.Context) { router.ServeHTTP(c.Writer, c.Request) })
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"gpt-image-2","prompt":"async local test"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			var accepted service.ImageTask
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))
			owner := service.ImageTaskOwner{UserID: 81001, APIKeyID: 81001}
			require.Eventually(t, func() bool {
				task, err := tasks.Get(context.Background(), owner, accepted.TaskID)
				return err == nil && task.Status != service.ImageTaskStatusProcessing
			}, 3*time.Second, 10*time.Millisecond)
			for range 20 {
				task, err := tasks.Get(context.Background(), owner, accepted.TaskID)
				require.NoError(t, err)
				require.Equal(t, tc.finalStatus, task.Status)
			}
			require.Len(t, up.calls, tc.expectedCalls)
		})
	}
}
