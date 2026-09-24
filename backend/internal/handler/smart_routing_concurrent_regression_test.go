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

// 仓储只返回独立快照，避免测试夹具的共享 map 写入掩盖生产请求隔离。
type routingConcurrentAccounts struct {
	service.AccountRepository
	accounts map[int64]service.Account
}

func (r *routingConcurrentAccounts) snapshot(a service.Account) service.Account {
	a.Credentials = cloneCredentialMap(a.Credentials)
	a.Extra = cloneCredentialMap(a.Extra)
	a.GroupIDs = append([]int64(nil), a.GroupIDs...)
	return a
}
func (r *routingConcurrentAccounts) ListAllWithFilters(_ context.Context, platform, _, status, _ string, groupID int64, _ string) ([]service.Account, error) {
	if groupID <= 0 || status != "" {
		return nil, fmt.Errorf("目录查询未限定候选分组")
	}
	if a, ok := r.accounts[groupID]; ok && (platform == "" || a.Platform == platform) {
		return []service.Account{r.snapshot(a)}, nil
	}
	return nil, nil
}
func (r *routingConcurrentAccounts) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	var out []service.Account
	for _, a := range r.accounts {
		if a.Platform == platform {
			out = append(out, r.snapshot(a))
		}
	}
	return out, nil
}
func (r *routingConcurrentAccounts) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	if a, ok := r.accounts[groupID]; ok && a.Platform == platform {
		return []service.Account{r.snapshot(a)}, nil
	}
	return nil, nil
}
func (r *routingConcurrentAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, a := range r.accounts {
		if a.ID == id {
			out := r.snapshot(a)
			return &out, nil
		}
	}
	return nil, fmt.Errorf("账号不存在")
}

type routingConcurrentKeys struct {
	service.APIKeyRepository
	snapshots map[string][]byte
}

func (r *routingConcurrentKeys) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	data, ok := r.snapshots[key]
	if !ok {
		return nil, service.ErrAPIKeyNotFound
	}
	var out service.APIKey
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (*routingConcurrentKeys) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

// 这里记录真实 RecordUsage 提交次数和归属，不模拟余额或订阅数据库事务。
type routingConcurrentUsage struct {
	service.UsageLogRepository
	mu   sync.Mutex
	logs []*service.UsageLog
}

func (r *routingConcurrentUsage) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := *log
	r.logs = append(r.logs, &snapshot)
	return true, nil
}
func (r *routingConcurrentUsage) snapshot() []*service.UsageLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*service.UsageLog(nil), r.logs...)
}

type routingConcurrentHarness struct {
	router   *gin.Engine
	usage    *routingConcurrentUsage
	resolver *service.SmartRoutingService
	mu       sync.Mutex
	visits   map[string][]int64
}

// 假上游仅监听本机，真实网关负责目录、换组、冷却、流转换和用量记录；不测试付费上游承载力。
func newRoutingConcurrentHarness(t *testing.T, passthrough bool, cooldown int) *routingConcurrentHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &routingConcurrentHarness{usage: &routingConcurrentUsage{}, visits: make(map[string][]int64)}
	accounts := &routingConcurrentAccounts{accounts: make(map[int64]service.Account)}
	upstream := &encryptedRoutingLocalUpstream{allowed: make(map[string]bool)}
	keyRepo := &routingConcurrentKeys{snapshots: make(map[string][]byte)}
	var bindings []service.APIKeyCompositeGroup
	for idx := range 3 {
		groupID := int64(idx + 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("读取假上游请求失败: %v", err)
				return
			}
			id := gjson.GetBytes(body, "input").String()
			h.mu.Lock()
			h.visits[id] = append(h.visits[id], groupID)
			h.mu.Unlock()
			if r.URL.Path != "/v1/responses" {
				t.Errorf("不应变更上游端点: %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Request-Id", fmt.Sprintf("%s-g%d", id, groupID))
			if strings.HasPrefix(id, "all-fail") || (strings.HasPrefix(id, "recover") && groupID == 1) {
				_, _ = io.WriteString(w, responsesBusyEvent)
				return
			}
			usage := `"usage":{"input_tokens":10,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}`
			progress := `{"type":"response.in_progress","response":{"id":"resp-` + id + `",` + usage + `}}`
			failed := `{"type":"response.failed","response":{"id":"resp-` + id + `","status":"failed","error":{"code":"server_error","message":"local interrupted"},` + usage + `}}`
			namespace := `{"type":"response.output_item.done","item":{"id":"fc_` + id + `","type":"function_call","call_id":"call_` + id + `","namespace":"functions","name":"execute","arguments":"{}"}}`
			sse := func(events ...string) string {
				var out strings.Builder
				for _, e := range events {
					out.WriteString("data: " + e + "\n\n")
				}
				return out.String()
			}
			switch {
			case strings.HasPrefix(id, "usage-eof"):
				_, _ = io.WriteString(w, sse(progress))
				return
			case strings.HasPrefix(id, "usage-failed"):
				_, _ = io.WriteString(w, sse(failed))
				return
			case strings.HasPrefix(id, "image-eof"):
				_, _ = io.WriteString(w, sse(`{"type":"response.output_item.done","item":{"id":"img_`+id+`","type":"image_generation_call","status":"completed","result":"aW1hZ2U=","size":"1024x1024"}}`))
				return
			case strings.HasPrefix(id, "namespace-read"):
				payload := sse(namespace, progress)
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)+100))
				_, _ = io.WriteString(w, payload)
				return
			}
			completed := `{"type":"response.completed","response":{"id":"resp-` + id + `","status":"completed","output":[{"id":"fc_` + id + `","type":"function_call","call_id":"call_` + id + `","namespace":"functions","name":"execute","arguments":"{}"}],` + usage + `}}`
			_, _ = io.WriteString(w, sse(namespace, completed))
		}))
		t.Cleanup(server.Close)
		upstream.allowed[server.URL] = true
		group := &service.Group{ID: groupID, Name: fmt.Sprintf("parallel-%d", groupID), Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1}
		bindings = append(bindings, service.APIKeyCompositeGroup{GroupID: groupID, Group: group})
		models := []any{"gpt-6-astra", "gpt-6-sol"}
		if groupID == 3 {
			models = []any{"gpt-6-astra", "gpt-6-luna"}
		}
		accounts.accounts[groupID] = service.Account{ID: groupID + 900, Name: group.Name, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
			Credentials: map[string]any{"api_key": "local-only", "base_url": server.URL, "model_whitelist": models}, Extra: map[string]any{"openai_passthrough": passthrough, "openai_text_route_mode": "force_responses"}}
	}
	for idx, keyValue := range []string{"sk-local-parallel-a", "sk-local-parallel-b"} {
		key := service.APIKey{ID: int64(4401 + idx), UserID: int64(4501 + idx), Key: keyValue, Status: service.StatusActive, SmartRouting: true, SmartRoutingCooldownSeconds: cooldown, BillingMode: service.APIKeyBillingModeBalance,
			User: &service.User{ID: int64(4501 + idx), Status: service.StatusActive, Balance: 100}, CompositeGroups: bindings}
		snapshot, err := json.Marshal(key)
		require.NoError(t, err)
		keyRepo.snapshots[keyValue] = snapshot
	}
	cfg := &config.Config{RunMode: config.RunModeSimple, Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
	cfg.Default.RateMultiplier = 1
	cache := &encryptedRoutingCooldownCache{until: make(map[[2]int64]time.Time)}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	forward := service.NewOpenAIGatewayService(accounts, h.usage, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billingCache, upstream, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	gateway := service.NewGatewayService(accounts, nil, nil, nil, nil, nil, nil, cache, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.resolver = service.NewSmartRoutingService(gateway, forward)
	keys := service.NewAPIKeyService(keyRepo, nil, nil, nil, nil, nil, cfg)
	handler := NewOpenAIGatewayHandler(forward, service.NewConcurrencyService(nil), billingCache, keys, nil, nil, nil, nil, cfg)
	h.router = gin.New()
	h.router.Use(middleware.Recovery())
	h.router.Use(middleware.WithSmartRoutingResolver(h.resolver, gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)), func(*gin.Context) bool { return true }))
	h.router.POST("/v1/responses", handler.Responses)
	return h
}

// 写失败模拟客户端断连；仍让上游完成，以验证排水取得终态用量且不重放。
type routingConcurrentDisconnectedWriter struct{ *httptest.ResponseRecorder }

func (*routingConcurrentDisconnectedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (*routingConcurrentDisconnectedWriter) WriteString(string) (int, error) {
	return 0, io.ErrClosedPipe
}
func (h *routingConcurrentHarness) call(key, model, id string, disconnect bool) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true, "input": id, "tools": []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": "function", "name": "execute", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if disconnect {
		h.router.ServeHTTP(&routingConcurrentDisconnectedWriter{rec}, req)
	} else {
		h.router.ServeHTTP(rec, req)
	}
	return rec
}
func (h *routingConcurrentHarness) attempted(id string) []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64(nil), h.visits[id]...)
}
func runRoutingConcurrentCalls(count int, call func(int)) {
	var wg sync.WaitGroup
	wg.Add(count)
	start := make(chan struct{})
	for i := range count {
		go func() { defer wg.Done(); <-start; call(i) }()
	}
	close(start)
	wg.Wait()
}

// 每个并发请求都实际失败一次再恢复，验证候选次序、工具身份与每次仅记录一次用量。
func TestSmartRoutingConcurrentRegressionRecovery(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, count := range []int{60, 120} {
			t.Run(fmt.Sprintf("passthrough_%v/%d", passthrough, count), func(t *testing.T) {
				h := newRoutingConcurrentHarness(t, passthrough, 0)
				responses := make([]*httptest.ResponseRecorder, count)
				runRoutingConcurrentCalls(count, func(i int) {
					responses[i] = h.call("sk-local-parallel-a", "gpt-6-astra", fmt.Sprintf("recover-%03d", i), false)
				})
				logs := h.usage.snapshot()
				require.Len(t, logs, count, "失败尝试不能多记用量，恢复请求不能漏记")
				seen := make(map[string]int)
				for _, log := range logs {
					seen[log.RequestID]++
					require.Equal(t, int64(902), log.AccountID)
					require.Equal(t, int64(2), *log.GroupID)
					require.Equal(t, 8, log.InputTokens)
					require.Equal(t, 3, log.OutputTokens)
					require.Equal(t, 2, log.CacheReadTokens)
				}
				for i, rec := range responses {
					id := fmt.Sprintf("recover-%03d", i)
					require.Equal(t, []int64{1, 2}, h.attempted(id))
					require.Equal(t, 200, rec.Code, rec.Body.String())
					require.NotContains(t, rec.Body.String(), "The service is busy")
					require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.completed"`))
					require.Contains(t, rec.Body.String(), `"namespace":"functions"`)
					require.Contains(t, rec.Body.String(), `"call_id":"call_`+id+`"`)
					require.Equal(t, 1, seen[id+"-g2"])
				}
			})
		}
	}
}

// 混合 token、图片、工具和客户端断连；已经发生消耗的请求不得进入第二组。
func TestSmartRoutingConcurrentRegressionPartialUsage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, count := range []int{60, 120} {
			t.Run(fmt.Sprintf("passthrough_%v/%d", passthrough, count), func(t *testing.T) {
				h := newRoutingConcurrentHarness(t, passthrough, 0)
				responses := make([]*httptest.ResponseRecorder, count)
				modes := []string{"usage-eof", "usage-failed", "image-eof", "namespace-read", "disconnected"}
				runRoutingConcurrentCalls(count, func(i int) {
					mode := modes[i%len(modes)]
					responses[i] = h.call("sk-local-parallel-a", "gpt-6-astra", fmt.Sprintf("%s-%03d", mode, i), mode == "disconnected")
				})
				logs := h.usage.snapshot()
				require.Len(t, logs, count)
				seen := make(map[string]*service.UsageLog)
				for _, log := range logs {
					require.NotContains(t, seen, log.RequestID, "同一响应只能提交一次用量")
					seen[log.RequestID] = log
					require.Equal(t, int64(901), log.AccountID)
					require.Equal(t, int64(1), *log.GroupID)
				}
				for i, rec := range responses {
					mode := modes[i%len(modes)]
					id := fmt.Sprintf("%s-%03d", mode, i)
					require.Equal(t, []int64{1}, h.attempted(id))
					log := seen[id+"-g1"]
					require.NotNil(t, log)
					if mode == "image-eof" {
						require.Equal(t, 1, log.ImageCount)
						require.Contains(t, rec.Body.String(), "aW1hZ2U=")
					} else {
						require.Equal(t, 8, log.InputTokens)
						require.Equal(t, 3, log.OutputTokens)
						require.Equal(t, 2, log.CacheReadTokens)
					}
					if mode == "disconnected" {
						continue
					}
					require.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
					// 原生读取错误使用 error 帧，其余失败复用 handler 的 response.failed；总共只能一个。
					failures := strings.Count(rec.Body.String(), `"type":"response.failed"`) + strings.Count(rec.Body.String(), `"type":"error"`)
					// 透传只有 usage 前导时尚未提交 SSE，可以沿用原 HTTP JSON 错误响应。
					if gjson.Get(rec.Body.String(), "error").IsObject() {
						failures++
						require.GreaterOrEqual(t, rec.Code, 400)
					}
					require.Equal(t, 1, failures, rec.Body.String())
					if mode == "namespace-read" {
						require.Contains(t, rec.Body.String(), `"namespace":"functions"`)
					}
				}
			})
		}
	}
}

// 冷却按 Key+分组共享同组模型，但不能污染另一个 Key；模型目录仍决定候选资格。
func TestSmartRoutingConcurrentRegressionCooldownIsolation(t *testing.T) {
	h := newRoutingConcurrentHarness(t, false, 60)
	warm := h.call("sk-local-parallel-a", "gpt-6-astra", "recover-warm", false)
	require.Equal(t, 200, warm.Code)
	require.Equal(t, []int64{1, 2}, h.attempted("recover-warm"))
	type requestCase struct {
		key, model, id string
		group          int64
	}
	cases := make([]requestCase, 120)
	for i := range cases {
		switch i % 3 {
		case 0:
			cases[i] = requestCase{"sk-local-parallel-a", "gpt-6-sol", fmt.Sprintf("healthy-cool-%03d", i), 2}
		case 1:
			cases[i] = requestCase{"sk-local-parallel-b", "gpt-6-sol", fmt.Sprintf("healthy-key-%03d", i), 1}
		case 2:
			cases[i] = requestCase{"sk-local-parallel-a", "gpt-6-luna", fmt.Sprintf("healthy-model-%03d", i), 3}
		}
	}
	responses := make([]*httptest.ResponseRecorder, len(cases))
	runRoutingConcurrentCalls(len(cases), func(i int) { c := cases[i]; responses[i] = h.call(c.key, c.model, c.id, false) })
	for i, c := range cases {
		require.Equal(t, 200, responses[i].Code, responses[i].Body.String())
		require.Equal(t, []int64{c.group}, h.attempted(c.id))
	}
	exhausted := h.call("sk-local-parallel-a", "gpt-6-astra", "all-fail-warm", false)
	require.NotContains(t, exhausted.Body.String(), "response.completed")
	require.Equal(t, []int64{2, 3}, h.attempted("all-fail-warm"))
	blocked := make([]*httptest.ResponseRecorder, 120)
	runRoutingConcurrentCalls(120, func(i int) {
		key := "sk-local-parallel-a"
		if i%2 == 1 {
			key = "sk-local-parallel-b"
		}
		blocked[i] = h.call(key, "gpt-6-astra", fmt.Sprintf("healthy-blocked-%03d", i), false)
	})
	for i, rec := range blocked {
		id := fmt.Sprintf("healthy-blocked-%03d", i)
		if i%2 == 0 {
			require.Equal(t, 503, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "SMART_ROUTING_ALL_GROUPS_COOLING")
			require.NotEmpty(t, rec.Header().Get("Retry-After"))
			require.Empty(t, h.attempted(id))
		} else {
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Equal(t, []int64{1}, h.attempted(id))
		}
	}
	require.Len(t, h.usage.snapshot(), 181, "失败和全冷却请求不得产生额外成功用量")
}
