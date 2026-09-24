package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 只替换持久化读取，测试使用真实 API Key 认证、复合选组和智能路由中间件。
type asyncGatewayKeyRepo struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r *asyncGatewayKeyRepo) GetByKey(_ context.Context, key string) (*service.APIKey, error) {
	if key != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	copy := *r.key
	return &copy, nil
}
func (r *asyncGatewayKeyRepo) GetByKeyForAuth(ctx context.Context, key string) (*service.APIKey, error) {
	return r.GetByKey(ctx, key)
}
func (*asyncGatewayKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type asyncGatewayResolver struct{}

func (asyncGatewayResolver) EvaluateSmartRoutingGroup(context.Context, *service.Group, string, string) (service.SmartRoutingGroupAvailability, error) {
	return service.SmartRoutingGroupAvailability{HasModel: true, Schedulable: true}, nil
}

// TestAsyncImageRealAuthReentry 用真实认证链验证复合前缀、单跳映射和图片不跨组重放的原有边界。
func TestAsyncImageRealAuthReentry(t *testing.T) {
	for _, mode := range []string{"ordinary", "composite", "smart"} {
		t.Run(mode, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			group := &service.Group{ID: 1, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, AllowImageGeneration: true}
			key := &service.APIKey{ID: 9, UserID: 7, Key: "sk-test-async", Status: service.StatusActive, GroupID: &group.ID, Group: group, BillingMode: service.APIKeyBillingModeBalance,
				User: &service.User{ID: 7, Status: service.StatusActive, Balance: 100}, ModelMapping: map[string]string{"alias": "gpt-image-2", "gpt-image-2": "wrong-second-hop"}}
			model := "alias"
			if mode != "ordinary" {
				key.GroupID, key.Group = nil, nil
				key.CompositeGroups = []service.APIKeyCompositeGroup{{GroupID: 1, Prefix: "GROUP", NormalizedPrefix: "group", Group: group}}
				if mode == "composite" {
					key.IsComposite = true
					model = "GROUP/alias"
				} else {
					key.SmartRouting = true
					second := *group
					second.ID = 2
					key.CompositeGroups = append(key.CompositeGroups, service.APIKeyCompositeGroup{GroupID: 2, Group: &second})
				}
			}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			keys := service.NewAPIKeyService(&asyncGatewayKeyRepo{key: key}, nil, nil, nil, nil, nil, cfg)
			auth := gin.HandlerFunc(middleware2.NewAPIKeyAuthMiddleware(keys, nil, cfg))
			store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
			tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
			h := NewAsyncImageHandler(tasks, nil)
			router := gin.New()
			router.Use(middleware2.ClientRequestID(), h.CaptureRequest, middleware2.WithSmartRoutingResolver(asyncGatewayResolver{}, auth, func(*gin.Context) bool { return true }))
			var mu sync.Mutex
			var visited []int64
			var models, requestIDs []string
			successes := 0
			router.POST("/v1/images/generations/async", h.Submit)
			router.POST("/v1/images/generations", func(c *gin.Context) {
				selected, _ := middleware2.GetAPIKeyFromContext(c)
				body, _ := io.ReadAll(c.Request.Body)
				var payload struct {
					Model string `json:"model"`
				}
				_ = json.Unmarshal(body, &payload)
				requestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
				mu.Lock()
				visited = append(visited, *selected.GroupID)
				models = append(models, payload.Model)
				requestIDs = append(requestIDs, requestID)
				mu.Unlock()
				if mode == "smart" && *selected.GroupID == 1 {
					service.SetOpsUpstreamError(c, http.StatusServiceUnavailable, "upstream busy", "")
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "upstream busy"}})
					return
				}
				mu.Lock()
				successes++
				mu.Unlock()
				c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/image.png"}}, "usage": gin.H{"input_tokens": 12, "output_tokens": 32}})
			})
			h.SetGatewayExecutor(func(_ string, c *gin.Context) { router.ServeHTTP(c.Writer, c.Request) })
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"`+model+`","prompt":"cat"}`))
			req.Header.Set("Authorization", "Bearer "+key.Key)
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
			var accepted service.ImageTask
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &accepted))
			require.Eventually(t, func() bool {
				got, err := tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, accepted.TaskID)
				return err == nil && got.Status != service.ImageTaskStatusProcessing
			}, 2*time.Second, 10*time.Millisecond)
			got, err := tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, accepted.TaskID)
			require.NoError(t, err)
			if mode == "smart" {
				require.Equal(t, service.ImageTaskStatusFailed, got.Status)
			} else {
				require.Equal(t, service.ImageTaskStatusCompleted, got.Status, string(got.Error))
			}
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, []int64{1}, visited)
			for _, actual := range models {
				require.Equal(t, "gpt-image-2", actual)
			}
			for _, id := range requestIDs {
				require.Equal(t, response.Header().Get("X-Sub2API-Request-ID"), id)
			}
			if mode == "smart" {
				require.Zero(t, successes)
			} else {
				require.Equal(t, 1, successes)
				require.Contains(t, string(got.Result), `"input_tokens":12`)
			}
		})
	}
}

type asyncUntrustedBody struct{ reads int }

func (r *asyncUntrustedBody) Read([]byte) (int, error) { r.reads++; return 0, io.EOF }

// 未认证流量不能因为异步请求快照而提前分配大正文内存。
func TestAsyncImageCaptureDoesNotReadBeforeAuthentication(t *testing.T) {
	reader := &asyncUntrustedBody{}
	router := gin.New()
	h := NewAsyncImageHandler(nil, nil)
	router.Use(h.CaptureRequest, func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })
	router.POST("/v1/images/generations/async", h.Submit)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", reader))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Zero(t, reader.reads)
}
