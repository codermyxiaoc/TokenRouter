//go:build unit

package handler

import (
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
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 仓储按分组返回独立账号快照，允许同一真实账号出现在不同组，避免夹具隐藏跨组重复调用。
type failoverStressAccounts struct {
	service.AccountRepository
	accounts map[int64]service.Account
	groups   map[int64][]int64
}

func (r *failoverStressAccounts) copy(id int64) service.Account {
	a := r.accounts[id]
	a.Credentials = cloneCredentialMap(a.Credentials)
	a.Extra = cloneCredentialMap(a.Extra)
	a.GroupIDs = append([]int64(nil), a.GroupIDs...)
	return a
}

func (r *failoverStressAccounts) ListAllWithFilters(_ context.Context, platform, _, status, _ string, groupID int64, _ string) ([]service.Account, error) {
	if groupID <= 0 || status != "" {
		return nil, fmt.Errorf("目录必须限定分组，不能提前删除故障账号")
	}
	return r.ListSchedulableByGroupIDAndPlatform(context.Background(), groupID, platform)
}

func (r *failoverStressAccounts) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	var out []service.Account
	for _, id := range r.groups[groupID] {
		if platform == "" || r.accounts[id].Platform == platform {
			out = append(out, r.copy(id))
		}
	}
	return out, nil
}

// Gemini 的既有混合调度会同时查询原生及 Antigravity，仓储替身必须实现相同查询形状。
func (r *failoverStressAccounts) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]service.Account, error) {
	var out []service.Account
	for _, platform := range platforms {
		accounts, err := r.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
		if err != nil {
			return nil, err
		}
		out = append(out, accounts...)
	}
	return out, nil
}

func (r *failoverStressAccounts) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	var out []service.Account
	for _, id := range []int64{7101, 7102} {
		if r.accounts[id].Platform == platform {
			out = append(out, r.copy(id))
		}
	}
	return out, nil
}

func (r *failoverStressAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if _, ok := r.accounts[id]; !ok {
		return nil, service.ErrAccountNotFound
	}
	a := r.copy(id)
	return &a, nil
}

// 测试缓存同时保留历史整组接口与当前账号接口，因此该文件可直接在旧版本上作差异对照。
type failoverStressCache struct {
	service.GatewayCache
	mu       sync.Mutex
	groups   map[[2]int64]time.Time
	accounts map[string]time.Time
}

func (s *failoverStressCache) GetSmartRoutingCooldown(_ context.Context, key, group int64) (time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(time.Duration(0), time.Until(s.groups[[2]int64{key, group}])), nil
}

func (s *failoverStressCache) CooldownSmartRoutingGroup(_ context.Context, key, group int64, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups[[2]int64{key, group}] = time.Now().Add(ttl)
	return nil
}

func (s *failoverStressCache) GetSmartRoutingAccountCooldowns(_ context.Context, key int64, ids []int64, model, endpoint string) (map[int64]time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]time.Duration)
	for _, id := range ids {
		if ttl := time.Until(s.accounts[fmt.Sprintf("%d/%d/%s/%s", key, id, model, endpoint)]); ttl > 0 {
			out[id] = ttl
		}
	}
	return out, nil
}

func (s *failoverStressCache) CooldownSmartRoutingAccounts(_ context.Context, key int64, ids []int64, model, endpoint string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		name := fmt.Sprintf("%d/%d/%s/%s", key, id, model, endpoint)
		if !s.accounts[name].After(time.Now()) {
			s.accounts[name] = time.Now().Add(ttl)
		}
	}
	return nil
}

type failoverStressHarness struct {
	router *gin.Engine
	usage  *routingConcurrentUsage
	cache  *failoverStressCache
	mu     sync.Mutex
	visits map[string][]int64
}

// 网络边界是真实本机 HTTP 服务，认证、选号、错误分类、流式转换和用量提交均走生产实现。
// simple 模式只验证 RecordUsage 提交，余额、订阅及预扣原子性另由 PostgreSQL 压测验证。
func newFailoverStressHarness(t *testing.T, shared bool, cooldown int) *failoverStressHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &failoverStressHarness{usage: &routingConcurrentUsage{}, visits: make(map[string][]int64), cache: &failoverStressCache{groups: make(map[[2]int64]time.Time), accounts: make(map[string]time.Time)}}
	repo := &failoverStressAccounts{accounts: make(map[int64]service.Account), groups: map[int64][]int64{1: {7101, 7102}}}
	if shared {
		repo.groups = map[int64][]int64{1: {7101}, 2: {7101, 7102}}
	}
	upstream := &encryptedRoutingLocalUpstream{allowed: make(map[string]bool)}
	for _, accountID := range []int64{7101, 7102} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil || r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
				t.Errorf("错误的本机上游调用: %s %s, %v", r.Method, r.URL.Path, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			id := gjson.GetBytes(body, "input").String()
			h.mu.Lock()
			h.visits[id] = append(h.visits[id], accountID)
			h.mu.Unlock()
			w.Header().Set("X-Request-Id", fmt.Sprintf("%s-a%d", id, accountID))
			w.Header().Set("Content-Type", "application/json")
			if strings.HasPrefix(id, "param-") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"property_name_above_max_length","param":"tools[0].parameters","message":"A property name exceeds its allowed length"}}`)
				return
			}
			if accountID == 7101 {
				switch {
				case strings.HasPrefix(id, "model404-"):
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","code":"model_not_found","message":"requested model or endpoint is unavailable"}}`)
					return
				case strings.HasPrefix(id, "quota429-"):
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"rate limit reached"}}`)
					return
				case strings.HasPrefix(id, "http503-"):
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"upstream unavailable"}}`)
					return
				case strings.HasPrefix(id, "busy-"):
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, responsesBusyEvent)
					return
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			usage := `"usage":{"input_tokens":10,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}`
			if strings.HasPrefix(id, "partial-") {
				_, _ = io.WriteString(w, responsesBusyText+`data: {"type":"response.failed","response":{"id":"resp-`+id+`","status":"failed","error":{"code":"server_error","message":"stream interrupted"},`+usage+`}}`+"\n\n")
				return
			}
			if strings.HasPrefix(id, "usage-only-") {
				_, _ = io.WriteString(w, `data: {"type":"response.in_progress","response":{"id":"resp-`+id+`",`+usage+`}}`+"\n\n")
				return
			}
			_, _ = io.WriteString(w, responsesBusyText+`data: {"type":"response.completed","response":{"id":"resp-`+id+`","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"visible-response"}]}],`+usage+`}}`+"\n\n")
		}))
		t.Cleanup(server.Close)
		upstream.allowed[server.URL] = true
		repo.accounts[accountID] = service.Account{ID: accountID, Name: fmt.Sprint(accountID), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: int(accountID - 7100), GroupIDs: []int64{1, 2}, Credentials: map[string]any{"api_key": "local-only", "base_url": server.URL, "model_whitelist": []any{"gpt-6-astra", "gpt-6-sol"}}, Extra: map[string]any{"openai_text_route_mode": "force_responses"}}
	}
	keysRepo := &routingConcurrentKeys{snapshots: make(map[string][]byte)}
	var groups []service.APIKeyCompositeGroup
	for _, id := range []int64{1, 2} {
		if _, ok := repo.groups[id]; ok {
			groups = append(groups, service.APIKeyCompositeGroup{GroupID: id, Group: &service.Group{ID: id, Name: fmt.Sprint(id), Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1}})
		}
	}
	for i, value := range []string{"sk-local-stress-a", "sk-local-stress-b"} {
		key := service.APIKey{ID: int64(7201 + i), Key: value, UserID: int64(7301 + i), User: &service.User{ID: int64(7301 + i), Status: service.StatusActive, Balance: 100}, Status: service.StatusActive, SmartRouting: true, SmartRoutingCooldownSeconds: cooldown, BillingMode: service.APIKeyBillingModeBalance, CompositeGroups: groups}
		data, err := json.Marshal(key)
		require.NoError(t, err)
		keysRepo.snapshots[value] = data
	}
	cfg := &config.Config{RunMode: config.RunModeSimple, Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxAccountSwitches = 3
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	forward := service.NewOpenAIGatewayService(repo, h.usage, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billing, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	gateway := service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, h.cache, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	keys := service.NewAPIKeyService(keysRepo, nil, nil, nil, nil, nil, cfg)
	handler := NewOpenAIGatewayHandler(forward, service.NewConcurrencyService(nil), billing, keys, nil, nil, nil, nil, cfg)
	h.router = gin.New()
	h.router.Use(middleware.Recovery())
	h.router.Use(middleware.WithSmartRoutingResolver(service.NewSmartRoutingService(gateway, forward), gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)), func(*gin.Context) bool { return true }))
	h.router.POST("/v1/responses", handler.Responses)
	return h
}

func (h *failoverStressHarness) call(key, model, id string, cancelBefore, cancelAfter bool) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"model": model, "input": id, "stream": true})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	if cancelBefore {
		cancel()
	}
	rec := httptest.NewRecorder()
	if cancelAfter {
		h.router.ServeHTTP(&routingConcurrentDisconnectedWriter{ResponseRecorder: rec, cancel: cancel}, req)
	} else {
		h.router.ServeHTTP(rec, req)
	}
	return rec
}

func (h *failoverStressHarness) attempts(id string) []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64(nil), h.visits[id]...)
}

// 固定工人池确保峰值为指定并发数，并复用同一真实网关实例执行多轮，不构造无限 goroutine。
func runFailoverStress(workers, total int, call func(int)) {
	var wg sync.WaitGroup
	jobs := make(chan int)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				call(id)
			}
		}()
	}
	for id := range total {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
}

// 同一脚本可复制到 HEAD 的隔离目录；日志中的实际错误率和调用数用于旧版/修复版对照。
func TestFailoverStressRecoveryDifferential(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared_account_%v", shared), func(t *testing.T) {
			h := newFailoverStressHarness(t, shared, 0)
			const total = 640
			patterns := []string{"healthy", "model404", "quota429", "http503", "busy"}
			results := make([]*httptest.ResponseRecorder, total)
			started := time.Now()
			runFailoverStress(128, total, func(i int) {
				results[i] = h.call("sk-local-stress-a", "gpt-6-astra", fmt.Sprintf("%s-%04d", patterns[i%5], i), false, false)
			})
			var errors, calls, repeated, completed int
			for i, rec := range results {
				id := fmt.Sprintf("%s-%04d", patterns[i%5], i)
				visits := h.attempts(id)
				calls += len(visits)
				seen := map[int64]bool{}
				for _, account := range visits {
					if seen[account] {
						repeated++
					}
					seen[account] = true
				}
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"response.completed"`) {
					errors++
				} else {
					completed++
				}
			}
			logs := h.usage.snapshot()
			t.Logf("FAILOVER_STRESS_METRIC shared=%v logical=%d concurrency=128 completed=%d user_errors=%d upstream_posts=%d same_account_repeats=%d usage_records=%d elapsed=%s", shared, total, completed, errors, calls, repeated, len(logs), time.Since(started))
			require.Zero(t, errors, "可恢复的故障不应流到用户；请先查看对照指标")
			require.Zero(t, repeated, "同一逻辑请求不能跨组再次请求已经失败的同一账号")
			require.Len(t, logs, total, "每次最终成功只能提交一次用量")
			logByID := make(map[string]*service.UsageLog)
			for _, log := range logs {
				require.NotContains(t, logByID, log.RequestID)
				logByID[log.RequestID] = log
				require.Equal(t, 8, log.InputTokens)
				require.Equal(t, 3, log.OutputTokens)
				require.Equal(t, 2, log.CacheReadTokens)
			}
			for i, rec := range results {
				id := fmt.Sprintf("%s-%04d", patterns[i%5], i)
				want := []int64{7101, 7102}
				if i%5 == 0 {
					want = []int64{7101}
				}
				require.Equal(t, want, h.attempts(id), id)
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.completed"`), id)
				require.NotContains(t, rec.Body.String(), "The service is busy")
				log := logByID[fmt.Sprintf("%s-a%d", id, want[len(want)-1])]
				require.NotNil(t, log, id)
				require.Equal(t, want[len(want)-1], log.AccountID)
				wantGroup := int64(1)
				if shared && i%5 != 0 {
					wantGroup = 2
				}
				require.Equal(t, wantGroup, *log.GroupID)
			}
		})
	}
}

// 参数错误、部分输出、只有用量和客户端断连均不能被恢复策略放大为再次生成或重复计量。
func TestFailoverStressNonReplayableBoundaries(t *testing.T) {
	h := newFailoverStressHarness(t, true, 0)
	const total = 640
	patterns := []string{"param", "partial", "usage-only", "cancel-before", "cancel-after"}
	results := make([]*httptest.ResponseRecorder, total)
	runFailoverStress(128, total, func(i int) {
		mode := patterns[i%5]
		results[i] = h.call("sk-local-stress-a", "gpt-6-astra", fmt.Sprintf("%s-%04d", mode, i), mode == "cancel-before", mode == "cancel-after")
	})
	logs := h.usage.snapshot()
	counts := map[string]int{}
	for _, log := range logs {
		counts[log.RequestID]++
	}
	var calls, repeats int
	for i := range total {
		id := fmt.Sprintf("%s-%04d", patterns[i%5], i)
		calls += len(h.attempts(id))
		if len(h.attempts(id)) > 1 {
			repeats += len(h.attempts(id)) - 1
		}
	}
	t.Logf("FAILOVER_STRESS_BOUNDARY logical=%d concurrency=128 upstream_posts=%d excess_posts=%d usage_records=%d", total, calls, repeats, len(logs))
	for i, rec := range results {
		mode := patterns[i%5]
		id := fmt.Sprintf("%s-%04d", mode, i)
		if mode == "cancel-before" {
			require.Empty(t, h.attempts(id), id)
			continue
		}
		require.Equal(t, []int64{7101}, h.attempts(id), id)
		if mode == "param" {
			require.Equal(t, 400, rec.Code, rec.Body.String())
			require.Zero(t, counts[id+"-a7101"])
			continue
		}
		require.Equal(t, 1, counts[id+"-a7101"], "已观测用量需恰好记录一次: "+id)
		if mode != "cancel-after" {
			require.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
		}
	}
	require.Len(t, logs, total*3/5)
	for _, log := range logs {
		require.Equal(t, int64(7101), log.AccountID)
		require.Equal(t, int64(1), *log.GroupID)
		require.Equal(t, 8, log.InputTokens)
		require.Equal(t, 3, log.OutputTokens)
		require.Equal(t, 2, log.CacheReadTokens)
	}
}

// 同组 A 故障后 B 仍然可用；冷却隔离到 Key 和模型，不应关闭整组或扩散到健康流量。
func TestFailoverStressCooldownKeepsHealthySibling(t *testing.T) {
	h := newFailoverStressHarness(t, false, 60)
	warm := h.call("sk-local-stress-a", "gpt-6-astra", "model404-warm", false, false)
	require.Equal(t, 200, warm.Code, warm.Body.String())
	require.Equal(t, []int64{7101, 7102}, h.attempts("model404-warm"))
	const total = 384
	results := make([]*httptest.ResponseRecorder, total)
	runFailoverStress(128, total, func(i int) {
		key, model := "sk-local-stress-a", "gpt-6-astra"
		if i%3 == 1 {
			key = "sk-local-stress-b"
		}
		if i%3 == 2 {
			model = "gpt-6-sol"
		}
		results[i] = h.call(key, model, fmt.Sprintf("healthy-cool-%04d", i), false, false)
	})
	var errors, failedAccountHits int
	for i, rec := range results {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"response.completed"`) {
			errors++
		}
		if i%3 == 0 {
			for _, account := range h.attempts(fmt.Sprintf("healthy-cool-%04d", i)) {
				if account == 7101 {
					failedAccountHits++
				}
			}
		}
	}
	t.Logf("FAILOVER_STRESS_COOLDOWN logical=%d concurrency=128 user_errors=%d known_failed_account_hits=%d usage_records=%d", total, errors, failedAccountHits, len(h.usage.snapshot()))
	require.Zero(t, errors)
	require.Zero(t, failedAccountHits)
	for i := range results {
		want := int64(7101)
		if i%3 == 0 {
			want = 7102
		}
		require.Equal(t, []int64{want}, h.attempts(fmt.Sprintf("healthy-cool-%04d", i)))
	}
	require.Len(t, h.usage.snapshot(), total+1)
}
